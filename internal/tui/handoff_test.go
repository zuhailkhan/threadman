package tui

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/zuhailkhan/threadman/internal/domain"
	"github.com/zuhailkhan/threadman/internal/handoff"
	"github.com/zuhailkhan/threadman/internal/ports"
)

type fakeWriter struct {
	name    string
	written []domain.Thread
}

func (w *fakeWriter) Name() string { return w.name }
func (w *fakeWriter) WriteThread(ctx context.Context, t domain.Thread) (string, error) {
	w.written = append(w.written, t)
	return "new-session", nil
}
func (w *fakeWriter) OpenCommand(sessionID, workspacePath string) *exec.Cmd {
	return exec.Command("true")
}
func (w *fakeWriter) DeleteThread(ctx context.Context, t domain.Thread) error { return nil }

func press(t *testing.T, m Model, key string) (Model, tea.Cmd) {
	t.Helper()
	var msg tea.KeyMsg
	switch key {
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	case "up":
		msg = tea.KeyMsg{Type: tea.KeyUp}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
	next, cmd := m.Update(msg)
	return asModel(next), cmd
}

// asModel unwraps Update's result, which is a pointer after a key press and a
// value otherwise.
func asModel(m tea.Model) Model {
	if p, ok := m.(*Model); ok {
		return *p
	}
	return m.(Model)
}

// With no LLM configured the mode dialog still appears, the summary modes are
// unavailable, and a full-transcript handoff writes the thread unchanged.
func TestHandoffModeDialogWithoutLLM(t *testing.T) {
	target := &fakeWriter{name: "pi"}
	thread := domain.Thread{
		ID:       "claude-abc",
		Provider: "claude",
		Messages: []domain.Message{{Role: domain.RoleUser, Content: "hello"}},
	}
	m := Model{
		state:        stateThread,
		width:        100,
		height:       30,
		thread:       thread,
		groupThreads: []domain.Thread{thread},
		writers:      []ports.ThreadWriter{&fakeWriter{name: "claude"}, target},
		summarizer:   handoff.NewSummarizer(filepath.Join(t.TempDir(), "config.json")),
	}

	m, _ = press(t, m, "h")
	if m.state != stateHandoffPicker {
		t.Fatalf("state after h = %v", m.state)
	}

	m, check := press(t, m, "enter")
	if m.state != stateHandoffMode || !m.llmChecking {
		t.Fatalf("state after picking a target = %v (checking %v)", m.state, m.llmChecking)
	}
	if got := m.View(); !strings.Contains(got, "checking") {
		t.Errorf("dialog should show the check in progress:\n%s", got)
	}

	next, _ := m.Update(check())
	m = asModel(next)
	view := m.View()
	for _, want := range []string{"Detailed summary", "Light summary", "Full transcript", "not configured", "press L"} {
		if !strings.Contains(view, want) {
			t.Errorf("mode dialog missing %q:\n%s", want, view)
		}
	}
	if handoff.Modes[m.handoffModeCursor] != handoff.ModeFull {
		t.Fatalf("cursor should rest on the full transcript, got %v", handoff.Modes[m.handoffModeCursor])
	}

	m, _ = press(t, m, "up")
	if handoff.Modes[m.handoffModeCursor] != handoff.ModeFull {
		t.Fatal("cursor moved onto an unavailable summary mode")
	}

	m, write := press(t, m, "enter")
	if m.state != stateHandoffWorking {
		t.Fatalf("state after choosing a mode = %v", m.state)
	}
	written := write()
	next, record := m.Update(written)
	m = asModel(next)
	if m.state != stateHandoffConfirm || m.handoffSessionID != "new-session" {
		t.Fatalf("state after write = %v, session %q", m.state, m.handoffSessionID)
	}
	if record == nil {
		t.Error("the handoff was not recorded")
	}

	// The transcript goes over as one attributed block.
	if len(target.written) != 1 || len(target.written[0].Messages) != 1 {
		t.Fatalf("want one session holding one block, got %+v", target.written)
	}
	block := target.written[0].Messages[0]
	if block.Kind != domain.KindHandoff || block.Role != domain.RoleUser || !strings.Contains(block.Content, "[User]\nhello") {
		t.Fatalf("full transcript not written as a handoff block: %+v", block)
	}

	// The record names the session it was written to.
	h := written.(handoffWrittenMsg).record
	if h.ID != block.HandoffID || h.TargetProvider != "pi" || h.TargetOriginalID != "new-session" || h.Mode != "full" {
		t.Errorf("record = %+v", h)
	}
}

// A received handoff block shows as a divider, not as something the user typed.
func TestRenderThreadShowsHandoffAsDivider(t *testing.T) {
	block := domain.HandoffMarker("h-1", "from Claude Code · full transcript · 4 messages") + "\n\n[User]\nsecret detail"
	out := renderThread(domain.Thread{Messages: []domain.Message{
		{Role: domain.RoleUser, Content: block, Kind: domain.KindHandoff, HandoffID: "h-1"},
		{Role: domain.RoleUser, Content: "now add tests"},
	}})

	if !strings.Contains(out, "handoff received · from Claude Code · full transcript · 4 messages") {
		t.Errorf("divider missing:\n%s", out)
	}
	if strings.Contains(out, "secret detail") || strings.Count(out, "You:") != 1 {
		t.Errorf("the block was rendered as a message:\n%s", out)
	}
}

func TestHandoffCancel(t *testing.T) {
	target := &fakeWriter{name: "pi"}
	m := Model{
		state:         stateHandoffMode,
		width:         100,
		height:        30,
		thread:        domain.Thread{Messages: []domain.Message{{Role: domain.RoleUser, Content: "hello"}}},
		handoffTarget: target,
		summarizer:    handoff.NewSummarizer(filepath.Join(t.TempDir(), "config.json")),
	}
	m.handoffModeCursor = m.firstUsableMode()

	m, write := press(t, m, "enter")
	m, _ = press(t, m, "esc")
	if m.state != stateThread || m.status != "handoff cancelled" {
		t.Fatalf("after esc: state %v, status %q", m.state, m.status)
	}

	// The write was already in flight; its result must not surface an error
	// or write a session.
	next, _ := m.Update(write())
	m = asModel(next)
	if m.state != stateThread || m.status != "handoff cancelled" {
		t.Errorf("late result changed the screen: state %v, status %q", m.state, m.status)
	}
	if len(target.written) != 0 {
		t.Errorf("cancelled handoff still wrote a session")
	}
}
