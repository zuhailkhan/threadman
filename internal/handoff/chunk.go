package handoff

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/zuhailkhan/threadman/internal/domain"
	"github.com/zuhailkhan/threadman/internal/llm"
)

// A conversation can be longer than the model's context. When its transcript
// won't fit in one request, it is read in consecutive parts: the model takes
// notes on each part, and the brief is written from the notes. Notes still too
// long to fit are condensed in groups first.

const (
	// bytesPerToken is how many bytes of transcript are counted as one token.
	// Real text runs closer to four, and code and JSON fewer; three keeps the
	// estimate on the safe side.
	bytesPerToken = 3
	// replyReserve is the most of the context kept free for the model's reply.
	replyReserve = 4096
	// promptMargin covers the request's own overhead.
	promptMargin = 512
	// minInputTokens is the least a part is cut down to.
	minInputTokens = 512
	// maxAttempts is how many times a summary is retried with smaller parts
	// after a request turns out too long for the model.
	maxAttempts = 4
	// maxCondenseRounds is how many times notes are condensed before giving up.
	maxCondenseRounds = 4
)

func estimateTokens(s string) int { return len(s)/bytesPerToken + 1 }

const notesHead = `You take notes on part of a long conversation between a user and an AI coding assistant. The conversation is too long to read in one pass, so it is read in consecutive parts, and your notes on each part are combined later into a handoff brief for a different assistant.

Write notes on this part only:
- what the user asked for or decided, including constraints and preferences they stated
- what was done and changed, with exact file paths, commands, identifiers and error messages
- decisions made, each with its reason
- approaches tried or considered and then dropped, each with the reason
- where things stood at the end of this part, and what was planned next
Say when something in this part changes or reverses something said earlier.

Rules:
` + toolRule + `
- Record only what the part shows. Do not guess what came before or after it.
- Write the notes only, as compact bullet points, with no preamble or closing remarks.`

// notesPrompt asks for notes on one part, in at most words words.
func notesPrompt(words int) string {
	return notesHead + fmt.Sprintf("\n- Keep the notes under %d words.", words)
}

const condenseHead = `You condense notes. Below are notes on consecutive parts of a long conversation between a user and an AI coding assistant, in order. Merge them into one set of notes in the same form: compact bullet points covering what the user wanted, what was done and changed, decisions in force with their reasons, approaches dropped with their reasons, and where things stood at the end.

Rules:
- Where a later part changes or reverses something, keep only the final state and say what it replaced.
- Keep exact file paths, commands, identifiers and error messages that still matter.
- Write the notes only, with no preamble or closing remarks.`

// condensePrompt asks for merged notes in at most words words.
func condensePrompt(words int) string {
	return condenseHead + fmt.Sprintf("\n- Keep the notes under %d words.", words)
}

// wordsFor is how many words a reply may run to so that k of them fit in a
// request of budget tokens.
func wordsFor(budget, k int) int {
	return max(60, budget/k*3/4)
}

// summarizer writes one brief, learning the model's context as it goes.
type summarizer struct {
	c llm.Client
	// limit is the model's context in tokens; 0 when it isn't known.
	limit int
}

func newSummarizer(ctx context.Context, c llm.Client) (*summarizer, llm.Limits) {
	var lim llm.Limits
	if l, ok := c.(llm.Limiter); ok {
		lim = l.Limits(ctx)
	}
	return &summarizer{c: c, limit: lim.Context}, lim
}

// budget is how many tokens of input one request can carry besides system,
// or 0 when the context isn't known.
func (s *summarizer) budget(system string) int {
	if s.limit <= 0 {
		return 0
	}
	reserve := min(replyReserve, s.limit/4)
	return max(minInputTokens, s.limit-reserve-estimateTokens(system)-promptMargin)
}

// brief writes the brief of t's messages before split, with system as the
// final prompt. A transcript that fits is sent whole, as before; one that
// doesn't is read in parts. A request the model rejects as too long teaches
// the summarizer a smaller limit, and the brief is tried again in parts.
func (s *summarizer) brief(ctx context.Context, system string, t domain.Thread, split int, catchUp bool) (string, error) {
	whole := estimateTokens(renderTranscript(t, split, catchUp))
	var lastErr error
	for range maxAttempts {
		out, err := s.attempt(ctx, system, t, split, catchUp, whole)
		var ce *llm.ContextError
		if err == nil || !errors.As(err, &ce) {
			return out, err
		}
		lastErr = err
		s.shrink(ce, whole+estimateTokens(system))
	}
	return "", tooLong(lastErr, s.limit)
}

func (s *summarizer) attempt(ctx context.Context, system string, t domain.Thread, split int, catchUp bool, whole int) (string, error) {
	b := s.budget(system)
	if b == 0 || whole <= b {
		report(ctx, Progress{Stage: StageWriting})
		return s.c.Complete(ctx, system, renderTranscript(t, split, catchUp))
	}
	parts := splitTranscript(t, split, catchUp, s.budget(notesHead))
	// Notes on a part are kept to a fraction of it, so several fit together.
	prompt := notesPrompt(wordsFor(s.budget(notesHead), 4))
	notes := make([]string, len(parts))
	for i, part := range parts {
		report(ctx, Progress{Stage: StageReading, Part: i + 1, Parts: len(parts)})
		n, err := s.c.Complete(ctx, prompt, part)
		if err != nil {
			return "", fmt.Errorf("part %d of %d: %w", i+1, len(parts), err)
		}
		notes[i] = n
	}
	return s.fromNotes(ctx, system, t, catchUp, notes)
}

// fromNotes writes the final brief from notes on consecutive parts,
// condensing them in groups while they are too long to send at once.
func (s *summarizer) fromNotes(ctx context.Context, system string, t domain.Thread, catchUp bool, notes []string) (string, error) {
	for range maxCondenseRounds {
		final := renderNotes(t, catchUp, notes)
		if estimateTokens(final) <= s.budget(system) || len(notes) == 1 {
			report(ctx, Progress{Stage: StageWriting})
			return s.c.Complete(ctx, system, final)
		}
		b := s.budget(condenseHead)
		groups := groupNotes(notes, b)
		// Condensed notes are kept short enough that two fit in a group. When
		// no two fit as they are, each is condensed on its own, to that size.
		prompt := condensePrompt(wordsFor(b, 3))
		condensed := make([]string, len(groups))
		for i, g := range groups {
			report(ctx, Progress{Stage: StageCondensing, Part: i + 1, Parts: len(groups)})
			n, err := s.c.Complete(ctx, prompt, renderNotes(t, catchUp, g))
			if err != nil {
				return "", err
			}
			condensed[i] = n
		}
		notes = condensed
	}
	report(ctx, Progress{Stage: StageWriting})
	return s.c.Complete(ctx, system, renderNotes(t, catchUp, notes))
}

// shrink lowers the limit after a request of about sent tokens was too long.
func (s *summarizer) shrink(ce *llm.ContextError, sent int) {
	switch {
	case ce.Limit > 0 && (s.limit == 0 || ce.Limit < s.limit):
		s.limit = ce.Limit
	case s.limit == 0:
		// Nothing known: whatever was sent was too much.
		s.limit = sent
	default:
		s.limit = s.limit * 2 / 3
	}
}

// tooLong explains a summary that still didn't fit after every retry.
func tooLong(err error, limit int) error {
	if limit > 0 {
		return fmt.Errorf("the conversation doesn't fit the model's context (about %d tokens) even in parts; load the model with a larger context, or set it with 'threadman llm set --context <tokens>': %w", limit, err)
	}
	return fmt.Errorf("the conversation doesn't fit the model's context even in parts; load the model with a larger context, or set it with 'threadman llm set --context <tokens>': %w", err)
}

// transcriptHeader is what every part of a transcript starts with.
func transcriptHeader(t domain.Thread, catchUp bool) string {
	var b strings.Builder
	if title := threadTitle(t); title != "" {
		fmt.Fprintf(&b, "Conversation title: %s\n", title)
	}
	if t.WorkspacePath != "" {
		fmt.Fprintf(&b, "Working directory: %s\n", t.WorkspacePath)
	}
	if catchUp {
		b.WriteString("The next assistant already knows this conversation up to the start of the transcript below. The transcript holds only what happened since, in another tool; summarize that, not the conversation as a whole.\n")
	}
	return b.String()
}

// splitTranscript cuts the transcript of t's messages before split into parts
// of at most budget tokens, at turn boundaries. A single turn longer than a
// part is cut down, keeping its start and end.
func splitTranscript(t domain.Thread, split int, catchUp bool, budget int) []string {
	header := transcriptHeader(t, catchUp)
	// Room for the header and the "part k of n" line.
	room := max(minInputTokens, budget-estimateTokens(header)-40)

	type span struct {
		from, to int
		body     strings.Builder
		tokens   int
	}
	var spans []*span
	cur := &span{from: 1}
	for i, m := range t.Messages[:split] {
		turn := renderTurn(i, m)
		if n := estimateTokens(turn); n > room {
			turn = cutTurn(turn, room*bytesPerToken)
		}
		n := estimateTokens(turn)
		if cur.tokens > 0 && cur.tokens+n > room {
			spans = append(spans, cur)
			cur = &span{from: i + 1}
		}
		cur.body.WriteString(turn)
		cur.tokens += n
		cur.to = i + 1
	}
	if cur.tokens > 0 {
		spans = append(spans, cur)
	}

	parts := make([]string, len(spans))
	for k, sp := range spans {
		parts[k] = fmt.Sprintf("%sThis is part %d of %d of the transcript: turns %d to %d of %d.\n\n<transcript>\n%s</transcript>",
			header, k+1, len(spans), sp.from, sp.to, split, sp.body.String())
	}
	return parts
}

// cutTurn shortens a rendered turn to about limit bytes, keeping its start
// and its end, which usually hold the request and the outcome.
func cutTurn(turn string, limit int) string {
	if len(turn) <= limit {
		return turn
	}
	head := limit * 2 / 3
	tail := limit - head
	for head > 0 && !isRuneStartByte(turn[head]) {
		head--
	}
	start := len(turn) - tail
	for start < len(turn) && !isRuneStartByte(turn[start]) {
		start++
	}
	return turn[:head] + fmt.Sprintf("\n[… %d characters of this turn left out …]\n", start-head) + turn[start:]
}

func isRuneStartByte(b byte) bool { return b&0xC0 != 0x80 }

// renderNotes lays notes on consecutive parts out for the next request.
func renderNotes(t domain.Thread, catchUp bool, notes []string) string {
	var b strings.Builder
	b.WriteString(transcriptHeader(t, catchUp))
	b.WriteString("The conversation was too long to read in one pass. Below are notes on its consecutive parts, in order, taken from the transcript; read them as the transcript.\n")
	for i, n := range notes {
		fmt.Fprintf(&b, "\n<notes part=\"%d of %d\">\n%s\n</notes>\n", i+1, len(notes), strings.TrimSpace(n))
	}
	return b.String()
}

// groupNotes packs consecutive notes into groups of at most budget tokens.
func groupNotes(notes []string, budget int) [][]string {
	var groups [][]string
	var cur []string
	size := 0
	for _, n := range notes {
		t := estimateTokens(n) + 20
		if len(cur) > 0 && size+t > budget {
			groups = append(groups, cur)
			cur, size = nil, 0
		}
		cur = append(cur, n)
		size += t
	}
	if len(cur) > 0 {
		groups = append(groups, cur)
	}
	return groups
}
