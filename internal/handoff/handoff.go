// Package handoff prepares the copy of a thread that gets written into
// another provider: either the full transcript or a summary of it. The source
// thread is never modified.
package handoff

import (
	"context"
	"fmt"
	"strings"
	"time"

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

// Prepare returns the thread to write into the target provider. For ModeFull
// that is the thread itself; for the summary modes it is a copy whose
// messages are replaced by a summary.
func (s *Summarizer) Prepare(ctx context.Context, t domain.Thread, mode Mode) (domain.Thread, error) {
	if !mode.NeedsLLM() {
		return t, nil
	}
	c, _, ok, err := s.client()
	if err != nil {
		return domain.Thread{}, err
	}
	if !ok {
		return domain.Thread{}, fmt.Errorf("no LLM configured; run 'threadman llm set'")
	}
	return Summarize(ctx, c, t, mode)
}

// Summarize returns a copy of t in which the older messages are replaced by a
// summary written by c and the newest few are kept word for word. A thread
// too short to have older messages is returned as it is, without calling c.
func Summarize(ctx context.Context, c llm.Client, t domain.Thread, mode Mode) (domain.Thread, error) {
	split := splitRecent(t.Messages, mode.keepRecent())
	if split == 0 {
		return t, nil
	}

	system := detailedPrompt
	if mode == ModeLight {
		system = lightPrompt
	}

	summary, err := c.Complete(ctx, system, renderTranscript(t, split))
	if err != nil {
		return domain.Thread{}, err
	}

	// The brief and its acknowledgement are dated just before the kept
	// messages so the session stays in time order.
	at := t.Messages[split].Timestamp
	if at.IsZero() {
		at = time.Now()
	}
	out := t
	out.Messages = make([]domain.Message, 0, 2+len(t.Messages)-split)
	out.Messages = append(out.Messages,
		domain.Message{Role: domain.RoleUser, Content: briefMessage(t, mode, split, summary), Timestamp: at},
		domain.Message{Role: domain.RoleAssistant, Content: "I've read the handoff brief. The most recent messages of the original conversation follow.", Timestamp: at},
	)
	out.Messages = append(out.Messages, t.Messages[split:]...)
	return out, nil
}

// renderTranscript lays the thread out for the model, numbering turns so the
// brief can keep them in order. Only the messages before split are included:
// the rest are handed over word for word, and a model that can see them tends
// to repeat them in the brief.
func renderTranscript(t domain.Thread, split int) string {
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
	b.WriteString("\n<transcript>\n")
	for i, m := range t.Messages[:split] {
		fmt.Fprintf(&b, "[turn %d · %s", i+1, m.Role)
		if !m.Timestamp.IsZero() {
			fmt.Fprintf(&b, " · %s", m.Timestamp.Format("2006-01-02 15:04"))
		}
		b.WriteString("]\n")
		b.WriteString(strings.TrimSpace(m.Content))
		b.WriteString("\n\n")
	}
	b.WriteString("</transcript>")
	return b.String()
}

// briefMessage wraps the summary in the opening message of the new session.
func briefMessage(t domain.Thread, mode Mode, split int, summary string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "This session continues a conversation handed off from %s by threadman. ", t.Provider)
	fmt.Fprintf(&b, "Below is a %s of the first %d of its %d messages, written by a language model; it can be incomplete or wrong. ",
		strings.ToLower(mode.Label()), split, len(t.Messages))
	fmt.Fprintf(&b, "The last %d messages follow this brief word for word; where they disagree with it, they are right.\n\n", len(t.Messages)-split)
	if t.ID != "" {
		fmt.Fprintf(&b, "The original conversation is unchanged. To read it in full, run: threadman show %s\n\n", t.ID)
	}
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimSpace(summary))
	b.WriteString("\n")
	return b.String()
}
