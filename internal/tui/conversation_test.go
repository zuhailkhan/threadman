package tui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zuhailkhan/threadman/internal/domain"
	"github.com/zuhailkhan/threadman/internal/handoff"
	"github.com/zuhailkhan/threadman/internal/ports"
)

var base = time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)

// roundTripGroup is a conversation that started in Claude Code and was
// handed off to OpenCode, which then added two turns.
func roundTripGroup() (claude, opencode domain.Thread) {
	block := domain.HandoffMarker("h-1", "from Claude Code · full transcript · 2 messages") + "\n\n[User]\nc1"
	claude = domain.Thread{ID: "claude-abc", Provider: "claude", OriginalID: "abc", CanonicalID: "claude-abc", WorkspacePath: "/ws/claude", Title: "Fix the parser",
		Messages: []domain.Message{
			{ID: "c1", Role: domain.RoleUser, Content: "c1 fix the parser", Timestamp: base.Add(1 * time.Minute)},
			{ID: "c2", Role: domain.RoleAssistant, Content: "c2 done", Timestamp: base.Add(2 * time.Minute)},
		}}
	opencode = domain.Thread{ID: "opencode-ses_1", Provider: "opencode", OriginalID: "ses_1", CanonicalID: "claude-abc", WorkspacePath: "/ws/oc",
		Messages: []domain.Message{
			{ID: "b", Role: domain.RoleUser, Content: block, Timestamp: base.Add(3 * time.Minute), Kind: domain.KindHandoff, HandoffID: "h-1"},
			{ID: "o4", Role: domain.RoleUser, Content: "o4 now add tests", Timestamp: base.Add(4 * time.Minute)},
			{ID: "o5", Role: domain.RoleAssistant, Content: "o5 tests added", Timestamp: base.Add(5 * time.Minute)},
		}}
	return claude, opencode
}

func TestRenderConversationMergesTools(t *testing.T) {
	claude, opencode := roundTripGroup()
	out := renderConversation([]domain.Thread{opencode, claude})

	at := 0
	for _, want := range []string{"c1 fix the parser", "c2 done", "handed off to OpenCode · from Claude Code", "o4 now add tests", "o5 tests added"} {
		i := strings.Index(out[at:], want)
		if i < 0 {
			t.Fatalf("%q missing or out of order:\n%s", want, out)
		}
		at += i + len(want)
	}
	for _, icon := range []string{providerIcon("claude"), providerIcon("opencode")} {
		if !strings.Contains(out, icon) {
			t.Errorf("messages are not tagged with their tool (%q):\n%s", icon, out)
		}
	}
}

// Handing the conversation back to Claude Code appends only what OpenCode
// added, to the session Claude already has.
func TestHandoffCatchesUpExistingSession(t *testing.T) {
	claude, opencode := roundTripGroup()
	claudeWriter := &fakeWriter{name: "claude"}
	m := Model{
		state:        stateThread,
		width:        100,
		height:       30,
		thread:       claude,
		groupThreads: []domain.Thread{claude, opencode},
		writers:      []ports.ThreadWriter{claudeWriter, &fakeWriter{name: "opencode"}, &fakeWriter{name: "pi"}},
		summarizer:   handoff.NewSummarizer(filepath.Join(t.TempDir(), "config.json")),
	}

	m, _ = press(t, m, "h")
	view := m.View()
	for _, want := range []string{"2 new messages", "up to date", "new session"} {
		if !strings.Contains(view, want) {
			t.Errorf("picker missing %q:\n%s", want, view)
		}
	}
	if m.handoffTargets[m.handoffCursor].writer != claudeWriter {
		t.Fatalf("cursor on %s, want Claude Code", m.handoffTargets[m.handoffCursor].writer.Name())
	}

	// OpenCode is up to date, so the cursor skips it.
	m, _ = press(t, m, "j")
	if got := m.handoffTargets[m.handoffCursor].writer.Name(); got != "pi" {
		t.Errorf("down moved to %s, want pi", got)
	}
	m, _ = press(t, m, "up")

	m, _ = press(t, m, "enter")
	if !strings.Contains(m.View(), "Catch Claude Code up on 2 new messages") {
		t.Errorf("mode dialog:\n%s", m.View())
	}
	m, write := press(t, m, "enter")
	written := workResult(t, write).(handoffWrittenMsg)

	if len(claudeWriter.written) != 0 || len(claudeWriter.appended) != 1 {
		t.Fatalf("want one append and no new session, got %d writes and %d appends", len(claudeWriter.written), len(claudeWriter.appended))
	}
	call := claudeWriter.appended[0]
	if call.target.ID != "claude-abc" || len(call.messages) != 1 || call.messages[0].Kind != domain.KindHandoff {
		t.Fatalf("append = %+v", call)
	}
	block := call.messages[0].Content
	for _, want := range []string{"from OpenCode · full transcript · 2 new messages", "continued in OpenCode", "o4 now add tests", "[Assistant · OpenCode"} {
		if !strings.Contains(block, want) {
			t.Errorf("block missing %q:\n%s", want, block)
		}
	}
	if strings.Contains(block, "c1 fix the parser") {
		t.Errorf("Claude's own turns were sent back to it:\n%s", block)
	}

	if !written.appended || written.sessionID != "abc" || written.workspace != "/ws/claude" || written.count != 2 {
		t.Errorf("written = %+v", written)
	}
	if written.record.TargetProvider != "claude" || written.record.TargetOriginalID != "abc" {
		t.Errorf("record = %+v", written.record)
	}

	next, _ := m.Update(written)
	m = asModel(next)
	if v := m.View(); !strings.Contains(v, "Added 2 messages to Claude Code") {
		t.Errorf("confirm dialog:\n%s", v)
	}
}

func TestHandoffWhenEveryToolIsUpToDate(t *testing.T) {
	claude, _ := roundTripGroup()
	m := Model{
		state:        stateThread,
		thread:       claude,
		groupThreads: []domain.Thread{claude},
		writers:      []ports.ThreadWriter{&fakeWriter{name: "claude"}},
	}
	m, _ = press(t, m, "h")
	if m.state != stateThread || !strings.Contains(m.status, "up to date") {
		t.Errorf("state %v, status %q", m.state, m.status)
	}
}

// A handoff that adds a session to the open conversation reloads it.
func TestOpenConversationPicksUpNewSession(t *testing.T) {
	claude, opencode := roundTripGroup()
	m := Model{state: stateThread, thread: claude, groupThreads: []domain.Thread{claude}}

	next, cmd := m.Update(backgroundThreadsMsg{claude, opencode})
	m = asModel(next)
	if cmd == nil {
		t.Fatal("the open conversation was not reloaded")
	}

	next, _ = m.Update(groupLoadedMsg{primary: claude, members: []domain.Thread{claude, opencode}})
	m = asModel(next)
	if len(m.groupThreads) != 2 {
		t.Errorf("group after reload: %d sessions, want 2", len(m.groupThreads))
	}

	// A reload for a conversation that is no longer open is ignored.
	m.state = stateList
	next, _ = m.Update(groupLoadedMsg{primary: claude, members: []domain.Thread{claude}})
	if got := asModel(next); len(got.groupThreads) != 2 {
		t.Error("a stale reload replaced the conversation")
	}
}

// Claude Code deleted its session; threadman kept the copy.
func TestConversationWithDeletedSession(t *testing.T) {
	claude, opencode := roundTripGroup()
	claude.SourceMissing = true
	claudeWriter := &fakeWriter{name: "claude"}
	m := Model{
		state:        stateThread,
		width:        120,
		height:       30,
		thread:       claude,
		groupThreads: []domain.Thread{claude, opencode},
		writers:      []ports.ThreadWriter{claudeWriter, &fakeWriter{name: "opencode"}},
		summarizer:   handoff.NewSummarizer(filepath.Join(t.TempDir(), "config.json")),
	}

	if v := m.View(); !strings.Contains(v, "deleted in Claude Code, kept by threadman") {
		t.Errorf("header does not say the session is threadman's copy:\n%s", v)
	}

	// It can't be resumed.
	m, _ = press(t, m, "r")
	m, _ = press(t, m, "enter")
	if !strings.Contains(m.status, "Claude Code no longer has this session") {
		t.Errorf("resume status %q", m.status)
	}

	// Handing back to Claude Code makes a new session with the whole
	// conversation, Claude's own deleted turns included.
	m.state = stateThread
	m, _ = press(t, m, "h")
	target := m.handoffTargets[0]
	if target.writer != claudeWriter || target.session != nil || target.status() != "new session" {
		t.Fatalf("Claude target = %+v (%s)", target, target.status())
	}
	m, _ = press(t, m, "enter")
	m, write := press(t, m, "enter")
	written := workResult(t, write).(handoffWrittenMsg)
	if written.appended || len(claudeWriter.written) != 1 {
		t.Fatalf("want a new session, got appended=%v writes=%d", written.appended, len(claudeWriter.written))
	}
	block := claudeWriter.written[0].Messages[0].Content
	for _, want := range []string{"c1 fix the parser", "o4 now add tests", "4 messages"} {
		if !strings.Contains(block, want) {
			t.Errorf("block missing %q:\n%s", want, block)
		}
	}
}

func TestReaderShowsToolCallsOnOneLine(t *testing.T) {
	out := renderThread(domain.Thread{Provider: "claude", Messages: []domain.Message{
		{Role: domain.RoleAssistant, Tools: []domain.ToolCall{
			{Name: "Bash", Input: "go test ./...\n  -run X", Output: "secret output"},
			{Name: "Read", Input: "a.go", Error: true},
		}},
	}})
	for _, want := range []string{"Assistant", "▸ Bash  go test ./...", "▸ Read  a.go  (failed)"} {
		if !strings.Contains(out, want) {
			t.Errorf("reader missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "secret output") || strings.Contains(out, "-run X") {
		t.Errorf("reader shows more than one line per call:\n%s", out)
	}
}

func modeDialog(t *testing.T, messages []domain.Message, llmReady bool) Model {
	t.Helper()
	thread := domain.Thread{ID: "claude-abc", Provider: "claude", Messages: messages}
	m := Model{
		state:        stateThread,
		width:        120,
		height:       40,
		thread:       thread,
		groupThreads: []domain.Thread{thread},
		writers:      []ports.ThreadWriter{&fakeWriter{name: "claude"}, &fakeWriter{name: "pi"}},
		summarizer:   handoff.NewSummarizer(filepath.Join(t.TempDir(), "config.json")),
	}
	m, _ = press(t, m, "h")
	m, _ = press(t, m, "enter")
	if m.state != stateHandoffMode {
		t.Fatalf("state %v, want the mode dialog", m.state)
	}
	// Stand in for the LLM check finishing.
	m.llmChecking = false
	if llmReady {
		m.llmStatus = handoff.Status{Configured: true, Label: "Ollama · llama3.2"}
	}
	m.handoffModeCursor = m.firstUsableMode()
	return m
}

func TestModeDialogShowsFullTranscriptSize(t *testing.T) {
	m := modeDialog(t, []domain.Message{{Role: domain.RoleUser, Content: strings.Repeat("a", 4000)}}, false)
	view := m.View()
	if !strings.Contains(view, "Full transcript") || !strings.Contains(view, "~1k tokens") {
		t.Errorf("dialog does not show the size:\n%s", view)
	}
	if strings.Contains(view, "more than many") {
		t.Errorf("a small transcript got the size warning:\n%s", view)
	}
}

func TestModeDialogRecommendsSummaryForLargeTranscript(t *testing.T) {
	large := []domain.Message{{Role: domain.RoleUser, Content: strings.Repeat("words ", 80_000)}}

	m := modeDialog(t, large, false)
	view := m.View()
	for _, want := range []string{"Full transcript", "~120k tokens", "~120k tokens, more than many", "A summary is", "press L to set one up"} {
		if !strings.Contains(view, want) {
			t.Errorf("without an LLM, dialog missing %q:\n%s", want, view)
		}
	}
	if handoff.Modes[m.handoffModeCursor] != handoff.ModeFull {
		t.Error("without an LLM the full transcript is the only choice")
	}

	m = modeDialog(t, large, true)
	view = m.View()
	if !strings.Contains(view, "~120k tokens, more than many") || strings.Contains(view, "press L") {
		t.Errorf("with an LLM ready:\n%s", view)
	}
	if handoff.Modes[m.handoffModeCursor] != handoff.ModeDetailed {
		t.Errorf("cursor on %v, want the detailed summary", handoff.Modes[m.handoffModeCursor])
	}
}
