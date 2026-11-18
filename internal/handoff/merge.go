package handoff

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/zuhailkhan/threadman/internal/domain"
	"github.com/zuhailkhan/threadman/internal/llm"
)

// A merge brings several separate threads into one new session. Each thread
// gets a brief of its own, with its tool, project and dates, so none is
// squeezed into a few lines by the others. Only the newest thread's last
// messages are kept word for word: that is where the work stopped.

const (
	// MaxMergeThreads is how many threads one merge takes.
	MaxMergeThreads = 8
	// mergeKeepRecent is how many of the newest thread's messages are kept
	// word for word.
	mergeKeepRecent = 2
	// mergeConcurrency is how many briefs are written at once by a hosted
	// model. A local one writes them one by one: requests sent to it together
	// can share one context, and a long thread would crowd out the others.
	mergeConcurrency = 3
)

const mergeIntro = `You write handoff briefs. Several separate conversations between a user and AI coding assistants, about related work, are being brought together into one new session with a different assistant. You are given one of them. The next assistant will see your brief of it next to briefs of the others, but not the conversations themselves. Write what that assistant needs to know from this conversation in order to continue the work without asking the user to repeat themselves.`

const mergeOnlyRule = `- Summarize only this conversation. Do not guess what the other conversations contain.`

const wholeRule = `- The transcript is the whole conversation. Describe the state as of its end.`

// mergePrompt is the system prompt for one thread's brief. partial is set for
// the newest thread, whose last messages are handed over word for word.
func mergePrompt(partial bool) string {
	scope := wholeRule
	if partial {
		scope = tailRule
	}
	return mergeIntro + "\n\n" + detailedSections + "\n\nRules:\n" + scope + "\n" + mergeOnlyRule + "\n" + toolRule + "\n" + detailedRules
}

// PrepareMerge builds one new session from several threads, each summarized
// by the configured LLM. Each thread holds a conversation's messages in time
// order. The record's target is filled in once the session has been written.
func (s *Summarizer) PrepareMerge(ctx context.Context, threads []domain.Thread) (domain.Thread, domain.Handoff, error) {
	c, cfg, ok, err := s.client()
	if err != nil {
		return domain.Thread{}, domain.Handoff{}, err
	}
	if !ok {
		return domain.Thread{}, domain.Handoff{}, fmt.Errorf("no LLM configured; run 'threadman llm set'")
	}
	return BuildMerge(ctx, c, cfg.Label(), threads, time.Now())
}

// BuildMerge composes the merge of threads, briefed by c, which is named
// summarizer in the record. The threads are put in the order they were last
// active; the new session runs in the newest one's workspace.
func BuildMerge(ctx context.Context, c llm.Client, summarizer string, threads []domain.Thread, now time.Time) (domain.Thread, domain.Handoff, error) {
	switch {
	case len(threads) < 2:
		return domain.Thread{}, domain.Handoff{}, fmt.Errorf("a merge needs at least 2 threads")
	case len(threads) > MaxMergeThreads:
		return domain.Thread{}, domain.Handoff{}, fmt.Errorf("a merge takes at most %d threads", MaxMergeThreads)
	}
	ordered := ByActivity(threads)
	last := len(ordered) - 1
	newest := ordered[last]

	splits := make([]int, len(ordered))
	for i, t := range ordered {
		splits[i] = len(t.Messages)
	}
	splits[last] = splitRecent(newest.Messages, mergeKeepRecent)

	briefs, err := writeBriefs(ctx, c, ordered, splits)
	if err != nil {
		return domain.Thread{}, domain.Handoff{}, err
	}

	h := domain.Handoff{
		ID:             uuid.New().String(),
		SourceThreadID: newest.ID,
		Mode:           "merge",
		Summarizer:     summarizer,
		CreatedAt:      now,
	}
	for _, t := range ordered {
		h.MergedFrom = append(h.MergedFrom, t.ID)
		h.SourceMessageCount += len(t.Messages)
	}
	if n := len(newest.Messages); n > 0 {
		h.SourceLastMessageID = newest.Messages[n-1].ID
	}
	for _, budget := range toolOutputBudgets {
		h.Content = composeMerge(h.ID, ordered, splits, briefs, summarizer, budget)
		if len(h.Content) <= maxBlockBytes {
			break
		}
	}

	out := newest
	out.Title = domain.MergedTitle(threadTitle(newest), len(ordered))
	out.Messages = []domain.Message{{
		ID:        h.ID,
		Role:      domain.RoleUser,
		Content:   h.Content,
		Timestamp: now,
		Kind:      domain.KindHandoff,
		HandoffID: h.ID,
	}}
	return out, h, nil
}

// writeBriefs summarizes the part of each thread before its split. A hosted
// model writes a few at a time; a local one writes them one by one, oldest
// first. A thread with nothing before its split gets no brief.
func writeBriefs(ctx context.Context, c llm.Client, threads []domain.Thread, splits []int) ([]string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	briefs := make([]string, len(threads))
	errs := make([]error, len(threads))
	probe, lim := newSummarizer(ctx, c)
	var done atomic.Int32
	parent := progressFunc(ctx)

	one := func(i int, t domain.Thread) {
		partial := splits[i] < len(t.Messages)
		// Each thread learns the context on its own copy, since they can run
		// side by side.
		s := &summarizer{c: c, limit: probe.limit}
		tctx := ctx
		if parent != nil {
			tctx = WithProgress(ctx, func(p Progress) {
				p.Thread, p.Threads, p.Done = i+1, len(threads), int(done.Load())
				parent(p)
			})
		}
		brief, err := s.brief(tctx, mergePrompt(partial), t, splits[i], false)
		done.Add(1)
		if err != nil {
			errs[i] = fmt.Errorf("summarize %q: %w", threadTitle(t), err)
			cancel()
			return
		}
		briefs[i] = brief
	}

	if lim.Local {
		for i, t := range threads {
			if splits[i] == 0 {
				continue
			}
			if err := ctx.Err(); err != nil {
				errs[i] = err
				break
			}
			one(i, t)
		}
	} else {
		sem := make(chan struct{}, mergeConcurrency)
		var wg sync.WaitGroup
		for i, t := range threads {
			if splits[i] == 0 {
				continue
			}
			wg.Add(1)
			go func(i int, t domain.Thread) {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					errs[i] = ctx.Err()
					return
				}
				defer func() { <-sem }()
				one(i, t)
			}(i, t)
		}
		wg.Wait()
	}
	// Report the error that stopped the merge, not the cancellations it caused.
	var first error
	for _, err := range errs {
		if err == nil {
			continue
		}
		if first == nil || (errors.Is(first, context.Canceled) && !errors.Is(err, context.Canceled)) {
			first = err
		}
	}
	return briefs, first
}

// composeMerge writes the merge block: the marker, a note on what it holds,
// then one section per thread, oldest first, the newest ending with its last
// messages word for word.
func composeMerge(id string, threads []domain.Thread, splits []int, briefs []string, summarizer string, toolOutput int) string {
	last := len(threads) - 1
	newest := threads[last]
	total := 0
	seen := map[string]bool{}
	var toolNames []string
	for _, t := range threads {
		total += len(t.Messages)
		for _, n := range toolLabels(t) {
			if !seen[n] {
				seen[n] = true
				toolNames = append(toolNames, n)
			}
		}
	}
	toolList := joinAnd(toolNames)
	tail := newest.Messages[splits[last]:]

	var b strings.Builder
	b.WriteString(domain.HandoffMarker(id, domain.MergeDetail(len(threads), toolList, total)))
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "threadman wrote this message, not the user. It brings together %d separate conversations the user had about related work, in %s, so you can continue them here as one. ", len(threads), toolList)
	b.WriteString("You wrote none of it: the assistant turns came from other models, so treat what they say as an account to check against the code, not as your own earlier statements.\n\n")
	b.WriteString("The conversations are below in the order they happened, oldest first. They ran separately and did not see each other, so where they disagree, the later one is more likely to be current. ")
	model := "a language model"
	if summarizer != "" {
		model += " (" + summarizer + ")"
	}
	fmt.Fprintf(&b, "Each brief was written by %s and can be incomplete or wrong. ", model)
	if len(tail) > 0 && splits[last] > 0 {
		fmt.Fprintf(&b, "The newest conversation's last %s follow its brief word for word; where they disagree with a brief, the messages are right.", plural(len(tail), "message"))
	}
	b.WriteString("\n\n")
	if hasTools(tail) {
		switch {
		case toolOutput == 0:
			b.WriteString("Lines starting with → are tool calls the assistant made. Their output is left out to keep this message small; check results against the files.\n\n")
		default:
			fmt.Fprintf(&b, "Lines starting with → are tool calls the assistant made, with their output below them, cut to %d characters each.\n\n", toolOutput)
		}
	}

	for i, t := range threads {
		fmt.Fprintf(&b, "## Conversation %d of %d · %s", i+1, len(threads), sourceLabel(t))
		if title := threadTitle(t); title != "" {
			b.WriteString(" · " + title)
		}
		if i == last {
			b.WriteString(" · newest")
		}
		b.WriteString("\n\n")
		if t.WorkspacePath != "" {
			fmt.Fprintf(&b, "Project: %s\n", t.WorkspacePath)
		}
		if span := dateSpan(t.Messages); span != "" {
			fmt.Fprintf(&b, "When: %s · %s\n", span, plural(len(t.Messages), "message"))
		} else {
			fmt.Fprintf(&b, "Messages: %d\n", len(t.Messages))
		}
		if t.ID != "" {
			fmt.Fprintf(&b, "Full conversation: threadman show %s\n", t.ID)
		}
		b.WriteString("\n")
		if splits[i] > 0 {
			b.WriteString("### Brief\n\n")
			b.WriteString(nestHeadings(strings.TrimSpace(briefs[i])))
			b.WriteString("\n\n")
		}
		if i == last && len(tail) > 0 {
			if splits[i] > 0 {
				fmt.Fprintf(&b, "### Last %s\n\n", plural(len(tail), "message"))
			} else {
				b.WriteString("### Conversation\n\n")
			}
			for _, m := range tail {
				writeTurn(&b, m, t.Provider, toolOutput)
			}
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// nestHeadings moves a brief's markdown headings below the "### Brief" it sits
// under, so its "## Goal" doesn't read as a sibling of the conversations.
// Lines inside code fences are left alone.
func nestHeadings(brief string) string {
	lines := strings.Split(brief, "\n")
	fenced := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
			continue
		}
		if !fenced && strings.HasPrefix(line, "#") {
			level := len(line) - len(strings.TrimLeft(line, "#"))
			if level < 4 && strings.HasPrefix(line[level:], " ") {
				lines[i] = "####" + line[level:]
			}
		}
	}
	return strings.Join(lines, "\n")
}

// ByActivity returns threads ordered by when they were last active, oldest
// first. A thread's activity is its newest message, or when it was created.
func ByActivity(threads []domain.Thread) []domain.Thread {
	out := append([]domain.Thread(nil), threads...)
	sort.SliceStable(out, func(i, j int) bool {
		return lastActive(out[i]).Before(lastActive(out[j]))
	})
	return out
}

func lastActive(t domain.Thread) time.Time {
	last := t.CreatedAt
	for _, m := range t.Messages {
		if m.Timestamp.After(last) {
			last = m.Timestamp
		}
	}
	return last
}

// dateSpan is when a thread's messages were written, e.g.
// "2026-11-01 09:12 to 2026-11-03 18:40".
func dateSpan(messages []domain.Message) string {
	var first, last time.Time
	for _, m := range messages {
		if m.Timestamp.IsZero() {
			continue
		}
		if first.IsZero() || m.Timestamp.Before(first) {
			first = m.Timestamp
		}
		if m.Timestamp.After(last) {
			last = m.Timestamp
		}
	}
	const layout = "2006-01-02 15:04"
	switch {
	case first.IsZero():
		return ""
	case first.Format(layout) == last.Format(layout):
		return first.Format(layout)
	default:
		return first.Format(layout) + " to " + last.Format(layout)
	}
}

func threadTitle(t domain.Thread) string {
	if t.CustomTitle != "" {
		return t.CustomTitle
	}
	return t.Title
}
