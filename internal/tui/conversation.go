package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/zuhailkhan/threadman/internal/conversation"
	"github.com/zuhailkhan/threadman/internal/domain"
	"github.com/zuhailkhan/threadman/internal/handoff"
	"github.com/zuhailkhan/threadman/internal/ports"
)

// handoffTarget is one choice in the handoff dialog: a tool, the session the
// conversation already has there if any, and what that tool has missed.
type handoffTarget struct {
	writer  ports.ThreadWriter
	session *domain.Thread
	missed  []conversation.Entry
}

// usable reports whether handing off to the target would carry anything.
func (t handoffTarget) usable() bool {
	return len(t.missed) > 0
}

func (t handoffTarget) status() string {
	switch {
	case t.session == nil:
		return "new session"
	case len(t.missed) == 0:
		return "up to date"
	case len(t.missed) == 1:
		return "1 new message"
	default:
		return fmt.Sprintf("%d new messages", len(t.missed))
	}
}

// handoffTargetsFor lists every tool the open conversation can be handed to.
// A tool that already has a session in it is caught up on what it missed; any
// other gets a new session with the whole conversation.
func (m Model) handoffTargetsFor() []handoffTarget {
	entries := conversation.Timeline(m.groupThreads)
	targets := make([]handoffTarget, 0, len(m.writers))
	for _, w := range m.writers {
		t := handoffTarget{writer: w, missed: conversation.Missed(entries, w.Name())}
		if s, ok := conversation.Session(m.groupThreads, w.Name()); ok {
			t.session = &s
		}
		targets = append(targets, t)
	}
	return targets
}

func firstUsableTarget(targets []handoffTarget) (int, bool) {
	for i, t := range targets {
		if t.usable() {
			return i, true
		}
	}
	return 0, false
}

// groupLoadedMsg carries the sessions of the open conversation, read fresh.
type groupLoadedMsg struct {
	primary domain.Thread
	members []domain.Thread
	// initial is set when the conversation is being opened, not refreshed.
	initial bool
}

// loadGroup reads every session of a conversation from its tool. A session
// that can't be read, for example because the tool deleted it, keeps its
// stored copy.
func (m Model) loadGroup(primary domain.Thread, members []domain.Thread, initial bool) tea.Cmd {
	svc := m.svc
	return func() tea.Msg {
		ctx := context.Background()
		out := make([]domain.Thread, 0, len(members))
		for _, mem := range members {
			full, err := svc.GetThread(ctx, mem.ID)
			if err != nil {
				full = mem
			}
			out = append(out, full)
		}
		p := primary
		for _, t := range out {
			if t.ID == primary.ID {
				p = t
			}
		}
		return groupLoadedMsg{primary: p, members: out, initial: initial}
	}
}

func sameMembers(a, b []domain.Thread) bool {
	if len(a) != len(b) {
		return false
	}
	ids := make(map[string]bool, len(a))
	for _, t := range a {
		ids[t.ID] = true
	}
	for _, t := range b {
		if !ids[t.ID] {
			return false
		}
	}
	return true
}

func containsThread(threads []domain.Thread, id string) bool {
	for _, t := range threads {
		if t.ID == id {
			return true
		}
	}
	return false
}

// writeHandoff hands the open conversation to the chosen tool: appended to
// the session it already has there, or written as a new session. The
// conversation's sessions are only read.
func (m Model) writeHandoff(ctx context.Context, target handoffTarget, mode handoff.Mode) tea.Cmd {
	root := m.thread
	s := m.summarizer
	return func() tea.Msg {
		timeout := 30 * time.Second
		if mode.NeedsLLM() {
			timeout = 10 * time.Minute
		}
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		w := target.writer
		src := root
		src.Messages = conversation.Messages(target.missed)

		if target.session != nil {
			session := *target.session
			src.WorkspacePath = session.WorkspacePath
			out, record, err := s.PrepareCatchUp(ctx, src, mode)
			if err != nil {
				return handoffErrMsg{err}
			}
			if err := ctx.Err(); err != nil {
				return handoffErrMsg{err}
			}
			if err := w.AppendMessages(ctx, session, out.Messages); err != nil {
				return handoffErrMsg{err}
			}
			record.TargetProvider = w.Name()
			record.TargetOriginalID = session.OriginalID
			return handoffWrittenMsg{sessionID: session.OriginalID, writer: w, record: record,
				workspace: session.WorkspacePath, appended: true, count: len(src.Messages)}
		}

		out, record, err := s.Prepare(ctx, src, mode)
		if err != nil {
			return handoffErrMsg{err}
		}
		if err := ctx.Err(); err != nil {
			return handoffErrMsg{err}
		}
		id, err := w.WriteThread(ctx, out)
		if err != nil {
			return handoffErrMsg{err}
		}
		record.TargetProvider = w.Name()
		record.TargetOriginalID = id
		return handoffWrittenMsg{sessionID: id, writer: w, record: record,
			workspace: src.WorkspacePath, count: len(src.Messages)}
	}
}

// recordHandoffLink stores the handoff, then re-reads the target tool so the
// conversation shows the block without waiting for the next sync.
func (m Model) recordHandoffLink(record domain.Handoff) tea.Cmd {
	source := m.thread
	svc := m.svc
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := svc.RecordHandoff(ctx, source, record); err != nil {
			return handoffLinkErrMsg{err}
		}
		if r, _ := svc.SyncProvider(ctx, record.TargetProvider); r.Err != nil {
			return handoffLinkErrMsg{fmt.Errorf("re-read %s: %w", record.TargetProvider, r.Err)}
		}
		return nil
	}
}

// renderConversation lays a conversation's sessions out as one timeline. When
// more than one tool took part, each message is tagged with its tool, and
// each handoff shows as a divider naming the tool it went to.
func renderConversation(threads []domain.Thread) string {
	entries := conversation.Timeline(threads)
	providers := map[string]bool{}
	for _, t := range threads {
		providers[t.Provider] = true
	}
	multi := len(providers) > 1

	var b strings.Builder
	sep := styleSep.Render(strings.Repeat("─", 80))
	for _, e := range entries {
		m := e.Message
		if m.Kind == domain.KindHandoff {
			b.WriteString(handoffDivider(e.Provider, m) + "\n")
			continue
		}
		tag := ""
		if multi {
			tag = providerStyle(e.Provider).Render(providerIcon(e.Provider)) + " "
		}
		switch m.Role {
		case domain.RoleUser:
			b.WriteString(tag + styleYou.Render("You:") + "\n")
		case domain.RoleAssistant:
			b.WriteString(tag + styleAssistant.Render("Assistant:") + "\n")
		default:
			b.WriteString(tag + string(m.Role) + ":\n")
		}
		b.WriteString(m.Content + "\n")
		b.WriteString(sep + "\n")
	}
	return b.String()
}

// renderThread lays out a single session.
func renderThread(t domain.Thread) string {
	return renderConversation([]domain.Thread{t})
}

// handoffDivider stands in for a handoff block threadman wrote into a session.
// The block repeats messages that are on the timeline in their own right, so
// only its description is shown.
func handoffDivider(provider string, m domain.Message) string {
	label := "handed off to " + domain.ProviderLabel(provider)
	if _, detail, ok := domain.ParseHandoffMarker(m.Content); ok && detail != "" {
		label += " · " + detail
	}
	return styleHint.Render("──── " + label + " ────")
}
