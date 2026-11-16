// Package handoff prepares what gets written into another provider when a
// thread is handed off: one attributed block holding either the full
// transcript or a summary of it. The source thread is never modified.
//
// The block is a single user message. Replaying another model's turns as the
// receiving model's own would make it treat them as things it said; in a
// labelled block it reads them as an account from another tool.
package handoff

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/zuhailkhan/threadman/internal/domain"
	"github.com/zuhailkhan/threadman/internal/llm"
)

type Mode int

const (
	ModeDetailed Mode = iota
	ModeLight
	ModeFull
)

// Modes is the order the choices are offered in.
var Modes = []Mode{ModeDetailed, ModeLight, ModeFull}

func (m Mode) Label() string {
	switch m {
	case ModeDetailed:
		return "Detailed summary"
	case ModeLight:
		return "Light summary"
	default:
		return "Full transcript"
	}
}

// Name is the mode's stored identifier.
func (m Mode) Name() string {
	switch m {
	case ModeDetailed:
		return "detailed"
	case ModeLight:
		return "light"
	default:
		return "full"
	}
}

// keepRecent is how many of the newest messages a summary handoff copies
// word for word instead of summarizing. The newest turns carry the task in
// progress, which a summary would blur.
func (m Mode) keepRecent() int {
	if m == ModeLight {
		return 2
	}
	return 6
}

// splitRecent returns the index where the word-for-word tail starts. The tail
// is widened to begin on a user message so the new session alternates
// cleanly after the brief and its acknowledgement.
func splitRecent(messages []domain.Message, keep int) int {
	split := len(messages) - keep
	for split > 0 && messages[split].Role != domain.RoleUser {
		split--
	}
	if split < 0 {
		split = 0
	}
	return split
}

// NeedsLLM reports whether the mode can only run with a configured model.
func (m Mode) NeedsLLM() bool {
	return m != ModeFull
}

const detailedPrompt = `You write handoff briefs. A conversation between a user and an AI coding assistant is being moved to a different assistant, which will see only your brief, not the conversation. Write what that assistant needs in order to continue the work without asking the user to repeat themselves.

Use exactly these markdown sections, in this order:

## Goal
What the user is trying to achieve, including constraints and preferences they stated.

## Current state
Where the work stood at the end of the turns you are summarizing, as stated in the conversation. Quote exact file paths, commands, identifiers and error messages where they matter.

## Decisions in force
Each decision that still stands, with the reason it was made. When a decision was later reversed, list only the final one and note what it replaced.

## Rejected approaches
Approaches that were considered or tried and then dropped, each with the reason, so they are not proposed again.

## Open questions
Anything unresolved or waiting on the user.

## Next steps
What was planned next at the end of the turns you are summarizing, most immediate first.

Rules:
- The transcript is the earlier part of the conversation. Its most recent turns are not shown to you; the next assistant receives them word for word right after your brief. Describe the state as of the end of the transcript and do not guess what happened afterwards.
- The transcript contains only the text of each message. Tool calls, file edits and command output are not in it. Report only what the text states; do not infer that a file was changed or a command succeeded unless a message says so.
- Record only decisions that were actually made. Do not invent any. Write "None." under a section that has nothing to report.
- Keep to the decisions and facts that matter for continuing; leave out pleasantries and dead-end chatter.
- Write the brief only, with no preamble or closing remarks.`

const lightPrompt = `You write handoff briefs. A conversation between a user and an AI coding assistant is being moved to a different assistant, which will see only your brief, not the conversation.

Write a brief of at most 200 words covering, in this order: what the user is trying to achieve, where the work stood at the end of the turns you are summarizing, and what was planned next. Mention an approach that was rejected only if the next assistant would be likely to suggest it again.

Rules:
- The transcript is the earlier part of the conversation. Its most recent turns are not shown to you; the next assistant receives them word for word right after your brief. Describe the state as of the end of the transcript and do not guess what happened afterwards.
- The transcript contains only the text of each message. Tool calls, file edits and command output are not in it. Report only what the text states.
- Quote exact file paths, commands and identifiers where they matter.
- Write the brief only, with no preamble or closing remarks.`

// Status is the result of checking the configured LLM.
type Status struct {
	Configured bool
	Label      string // e.g. "Ollama · llama3.2"; empty when not configured
	Err        error  // why the LLM can't be used; nil when it is ready
}

// Ready reports whether summaries can be generated.
func (s Status) Ready() bool {
	return s.Configured && s.Err == nil
}

// Summarizer builds handoff copies using the LLM configured at cfgPath. The
// config is re-read on every call, so changes made while the app is running
// are picked up.
type Summarizer struct {
	cfgPath string
}

func NewSummarizer(cfgPath string) *Summarizer {
	return &Summarizer{cfgPath: cfgPath}
}

// Config returns the saved LLM settings. ok is false when there are none or
// the file can't be read.
func (s *Summarizer) Config() (cfg llm.Config, ok bool) {
	cfg, ok, err := llm.Load(s.cfgPath)
	return cfg, ok && err == nil
}

// SaveConfig validates and stores new LLM settings.
func (s *Summarizer) SaveConfig(cfg llm.Config) (llm.Config, error) {
	return llm.Save(s.cfgPath, cfg)
}

// ClearConfig removes the LLM settings.
func (s *Summarizer) ClearConfig() error {
	return llm.Clear(s.cfgPath)
}

func (s *Summarizer) client() (llm.Client, llm.Config, bool, error) {
	cfg, ok, err := llm.Load(s.cfgPath)
	if err != nil || !ok {
		return nil, cfg, ok, err
	}
	c, err := llm.New(cfg)
	return c, cfg, true, err
}

// Check reports whether an LLM is configured and reachable.
func (s *Summarizer) Check(ctx context.Context) Status {
	c, cfg, ok, err := s.client()
	if !ok {
		return Status{Err: err}
	}
	st := Status{Configured: true, Label: cfg.Label(), Err: err}
	if err == nil {
		st.Err = c.Ping(ctx)
	}
	return st
}

// Prepare builds the handoff of t in the given mode: the thread to write into
// the target provider, holding a single handoff block, and the record of it.
// The record's target is filled in once the session has been written.
func (s *Summarizer) Prepare(ctx context.Context, t domain.Thread, mode Mode) (domain.Thread, domain.Handoff, error) {
	return s.prepare(ctx, t, mode, false)
}

// PrepareCatchUp is Prepare for a target that already holds the earlier part
// of the conversation: t holds only the messages it missed.
func (s *Summarizer) PrepareCatchUp(ctx context.Context, t domain.Thread, mode Mode) (domain.Thread, domain.Handoff, error) {
	return s.prepare(ctx, t, mode, true)
}

func (s *Summarizer) prepare(ctx context.Context, t domain.Thread, mode Mode, catchUp bool) (domain.Thread, domain.Handoff, error) {
	if !mode.NeedsLLM() {
		return build(ctx, nil, "", t, mode, time.Now(), catchUp)
	}
	c, cfg, ok, err := s.client()
	if err != nil {
		return domain.Thread{}, domain.Handoff{}, err
	}
	if !ok {
		return domain.Thread{}, domain.Handoff{}, fmt.Errorf("no LLM configured; run 'threadman llm set'")
	}
	return build(ctx, c, cfg.Label(), t, mode, time.Now(), catchUp)
}

// Build composes the handoff of t. In the summary modes the older messages are
// replaced by a brief written by c, named summarizer in the record, and the
// newest few are kept word for word; c is not used for ModeFull. A thread too
// short to have older messages is handed over in full, without calling c.
func Build(ctx context.Context, c llm.Client, summarizer string, t domain.Thread, mode Mode, now time.Time) (domain.Thread, domain.Handoff, error) {
	return build(ctx, c, summarizer, t, mode, now, false)
}

// BuildCatchUp is Build for a target that already holds the earlier part of
// the conversation: t holds only the messages it missed, and the block says
// so.
func BuildCatchUp(ctx context.Context, c llm.Client, summarizer string, t domain.Thread, mode Mode, now time.Time) (domain.Thread, domain.Handoff, error) {
	return build(ctx, c, summarizer, t, mode, now, true)
}

func build(ctx context.Context, c llm.Client, summarizer string, t domain.Thread, mode Mode, now time.Time, catchUp bool) (domain.Thread, domain.Handoff, error) {
	split := 0
	summary := ""
	if mode.NeedsLLM() {
		split = splitRecent(t.Messages, mode.keepRecent())
		if split == 0 {
			mode = ModeFull
		}
	}
	if mode.NeedsLLM() {
		system := detailedPrompt
		if mode == ModeLight {
			system = lightPrompt
		}
		var err error
		summary, err = c.Complete(ctx, system, renderTranscript(t, split, catchUp))
		if err != nil {
			return domain.Thread{}, domain.Handoff{}, err
		}
	} else {
		summarizer = ""
	}

	h := domain.Handoff{
		ID:                 uuid.New().String(),
		SourceThreadID:     t.ID,
		Mode:               mode.Name(),
		Summarizer:         summarizer,
		SourceMessageCount: len(t.Messages),
		CreatedAt:          now,
	}
	if n := len(t.Messages); n > 0 {
		h.SourceLastMessageID = t.Messages[n-1].ID
	}
	// Tool output is what makes a block large. Each smaller budget is tried
	// in turn until the block fits; every call keeps its name and input.
	for _, budget := range toolOutputBudgets {
		h.Content = composeBlock(h.ID, t, mode, split, summary, summarizer, catchUp, budget)
		if len(h.Content) <= maxBlockBytes {
			break
		}
	}

	out := t
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

// composeBlock writes the handoff block: a marker line threadman recognises on
// sync, a note on where the content came from, then the brief and the
// messages handed over word for word.
func composeBlock(id string, t domain.Thread, mode Mode, split int, summary, summarizer string, catchUp bool, toolOutput int) string {
	source := sourceLabel(t)
	total := len(t.Messages)
	noun := "message"
	if catchUp {
		noun = "new message"
	}

	var b strings.Builder
	b.WriteString(domain.HandoffMarker(id, fmt.Sprintf("from %s · %s · %s", source, strings.ToLower(mode.Label()), plural(total, noun))))
	b.WriteString("\n\n")
	if catchUp {
		fmt.Fprintf(&b, "threadman wrote this message, not the user. This conversation continued in %s after it was last here; this carries over what happened there so you can pick it up. ", source)
	} else {
		fmt.Fprintf(&b, "threadman wrote this message, not the user. It carries over a conversation that took place in %s so you can continue it here. ", source)
	}
	b.WriteString("You wrote none of it: the assistant turns below came from another model, so treat what they say as an account to check against the code, not as your own earlier statements.\n\n")
	if mode.NeedsLLM() {
		model := "a language model"
		if summarizer != "" {
			model += " (" + summarizer + ")"
		}
		of := "the conversation's"
		if catchUp {
			of = "the"
		}
		fmt.Fprintf(&b, "The brief covers the first %d of %s %d %ss and was written by %s; it can be incomplete or wrong. ", split, of, total, noun, model)
		fmt.Fprintf(&b, "The last %s follow it word for word, and where the two disagree, the messages are right.\n\n", plural(total-split, "message"))
	}
	if t.ID != "" {
		fmt.Fprintf(&b, "To read the original conversation in full, run: threadman show %s\n\n", t.ID)
	}
	if hasTools(t.Messages[split:]) {
		switch {
		case toolOutput == 0:
			b.WriteString("Lines starting with → are tool calls the assistant made. Their output is left out to keep this message small; check results against the files.\n\n")
		case toolOutput < toolOutputBudgets[0]:
			fmt.Fprintf(&b, "Lines starting with → are tool calls the assistant made, with their output below them cut to %d characters to keep this message small.\n\n", toolOutput)
		default:
			fmt.Fprintf(&b, "Lines starting with → are tool calls the assistant made, with their output below them, cut to %d characters each.\n\n", toolOutput)
		}
	}

	if mode.NeedsLLM() {
		b.WriteString("## Brief\n\n")
		b.WriteString(strings.TrimSpace(summary))
		b.WriteString("\n\n")
		fmt.Fprintf(&b, "## Last %s\n\n", plural(total-split, "message"))
	} else {
		b.WriteString("## Conversation\n\n")
	}
	for _, m := range t.Messages[split:] {
		writeTurn(&b, m, t.Provider, toolOutput)
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// sourceLabel names the tools the handed-over messages were written in, in
// the order they first appear.
func sourceLabel(t domain.Thread) string {
	var names []string
	seen := map[string]bool{}
	for _, m := range t.Messages {
		p := m.Provider
		if p == "" {
			p = t.Provider
		}
		if !seen[p] {
			seen[p] = true
			names = append(names, domain.ProviderLabel(p))
		}
	}
	switch len(names) {
	case 0:
		return domain.ProviderLabel(t.Provider)
	case 1:
		return names[0]
	default:
		return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	}
}

// A handoff block carries every tool call but less of each result than
// threadman stores: a long session can run thousands of calls, and the block
// has to fit in the receiving model's context.
const (
	blockToolInput  = 300
	blockToolOutput = 800
	// maxBlockBytes is what a block aims to stay under, about 75k tokens.
	maxBlockBytes = 300_000
	// The summarizer only needs to know what was done, not what came back.
	briefToolInput  = 200
	briefToolOutput = 200
)

// toolOutputBudgets are the per-call output limits tried, largest first.
var toolOutputBudgets = []int{blockToolOutput, 200, 0}

func hasTools(messages []domain.Message) bool {
	for _, m := range messages {
		if len(m.Tools) > 0 {
			return true
		}
	}
	return false
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// writeTurn writes one message under a label naming who wrote it. An
// assistant turn names the tool it was written in: the message's own
// provider, or the thread's.
func writeTurn(b *strings.Builder, m domain.Message, threadProvider string, toolOutput int) {
	body := strings.TrimSpace(m.Content)
	var label string
	switch {
	case m.Kind == domain.KindHandoff:
		// A block this thread itself received earlier: keep its content but
		// not its marker, which belongs to that earlier handoff.
		_, detail, _ := domain.ParseHandoffMarker(body)
		label = "Handoff received"
		if detail != "" {
			label += " · " + detail
		}
		if _, rest, ok := strings.Cut(body, "\n"); ok {
			body = strings.TrimSpace(rest)
		} else {
			body = ""
		}
	case m.Role == domain.RoleUser:
		label = "User"
	case m.Role == domain.RoleAssistant:
		p := m.Provider
		if p == "" {
			p = threadProvider
		}
		label = "Assistant · " + domain.ProviderLabel(p)
	default:
		label = string(m.Role)
	}
	b.WriteString("[" + label)
	if !m.Timestamp.IsZero() {
		b.WriteString(" · " + m.Timestamp.Format("2006-01-02 15:04"))
	}
	b.WriteString("]\n")
	if body != "" {
		b.WriteString(body)
		b.WriteString("\n")
	}
	if len(m.Tools) > 0 {
		if body != "" {
			b.WriteString("\n")
		}
		domain.WriteTools(b, m.Tools, blockToolInput, toolOutput)
	}
	b.WriteString("\n")
}

// renderTranscript lays the older part of the thread out for the summarizer,
// numbering turns so the brief can keep them in order. Only the messages
// before split are included: the rest are handed over word for word, and a
// model that can see them tends to repeat them in the brief.
func renderTranscript(t domain.Thread, split int, catchUp bool) string {
	var b strings.Builder
	title := t.CustomTitle
	if title == "" {
		title = t.Title
	}
	if title != "" {
		fmt.Fprintf(&b, "Conversation title: %s\n", title)
	}
	if t.WorkspacePath != "" {
		fmt.Fprintf(&b, "Working directory: %s\n", t.WorkspacePath)
	}
	if catchUp {
		b.WriteString("The next assistant already knows this conversation up to the start of the transcript below. The transcript holds only what happened since, in another tool; summarize that, not the conversation as a whole.\n")
	}
	b.WriteString("\n<transcript>\n")
	for i, m := range t.Messages[:split] {
		role := string(m.Role)
		if m.Kind == domain.KindHandoff {
			role = "handoff received from another tool"
		}
		fmt.Fprintf(&b, "[turn %d · %s", i+1, role)
		if !m.Timestamp.IsZero() {
			fmt.Fprintf(&b, " · %s", m.Timestamp.Format("2006-01-02 15:04"))
		}
		b.WriteString("]\n")
		if content := strings.TrimSpace(m.Content); content != "" {
			b.WriteString(content)
			b.WriteString("\n")
		}
		domain.WriteTools(&b, m.Tools, briefToolInput, briefToolOutput)
		b.WriteString("\n")
	}
	b.WriteString("</transcript>")
	return b.String()
}
