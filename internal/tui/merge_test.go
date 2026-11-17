package tui

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zuhailkhan/threadman/internal/domain"
	"github.com/zuhailkhan/threadman/internal/handoff"
	"github.com/zuhailkhan/threadman/internal/ports"
)

func listThread(id, provider, title string, day int) domain.Thread {
	at := time.Date(2026, 11, day, 9, 0, 0, 0, time.UTC)
	return domain.Thread{
		ID: id, Provider: provider, Title: title, WorkspacePath: "/proj/" + id, CreatedAt: at,
		Messages: []domain.Message{
			{ID: id + ":1", Role: domain.RoleUser, Content: "q", Timestamp: at},
			{ID: id + ":2", Role: domain.RoleAssistant, Content: "a", Timestamp: at.Add(time.Minute)},
		},
	}
}

// mergeModel is a thread list, newest first as the list sorts it.
func mergeModel(t *testing.T) Model {
	threads := []domain.Thread{
		listThread("claude-c", "claude", "Third", 3),
		listThread("pi-b", "pi", "Second", 2),
		listThread("opencode-a", "opencode", "First", 1),
	}
	groups := buildGroups(threads)
	return Model{
		state:          stateList,
		width:          120,
		height:         30,
		threads:        threads,
		groups:         groups,
		filteredGroups: groups,
		writers:        []ports.ThreadWriter{&fakeWriter{name: "claude"}, &fakeWriter{name: "pi"}},
		summarizer:     handoff.NewSummarizer(filepath.Join(t.TempDir(), "config.json")),
	}
}

func TestSelectThreadsAndOpenMergeDialog(t *testing.T) {
	m := mergeModel(t)

	// Space selects the row and moves down; the third row is left out.
	m, _ = press(t, m, " ")
	m, _ = press(t, m, " ")
	if len(m.mergeSelected) != 2 || m.cursor != 2 {
		t.Fatalf("selected %v, cursor %d", m.mergeSelected, m.cursor)
	}
	if view := m.View(); !strings.Contains(view, "2 selected") || !strings.Contains(view, "m merge") {
		t.Errorf("status bar doesn't show the selection:\n%s", view)
	}

	m, cmd := press(t, m, "m")
	if m.state != stateMergePicker || cmd == nil {
		t.Fatalf("state after m = %v, LLM check %v", m.state, cmd != nil)
	}
	if len(m.mergeGroups) != 2 || m.mergeGroups[0].Primary.ID != "pi-b" || m.mergeGroups[1].Primary.ID != "claude-c" {
		t.Errorf("merge order should be oldest first: %+v", m.mergeGroups)
	}

	m.llmChecking = false
	view := m.View()
	for _, want := range []string{"Merge 2 threads into a new session in:", "1. [P] Second", "2. [C] Third", "Runs in", "Merging needs an LLM"} {
		if !strings.Contains(view, want) {
			t.Errorf("dialog missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "First") {
		t.Error("an unselected thread is listed in the merge")
	}

	// Without an LLM, Enter does nothing.
	m, cmd = press(t, m, "enter")
	if m.state != stateMergePicker || cmd != nil {
		t.Errorf("merge started without an LLM: state %v", m.state)
	}

	m, _ = press(t, m, "esc")
	if m.state != stateList || len(m.mergeSelected) != 2 {
		t.Errorf("Esc in the dialog should go back keeping the selection: state %v, %v", m.state, m.mergeSelected)
	}
	m, _ = press(t, m, "esc")
	if len(m.mergeSelected) != 0 {
		t.Error("Esc in the list should clear the selection")
	}
}

func TestMergeNeedsTwoThreads(t *testing.T) {
	m := mergeModel(t)
	m, _ = press(t, m, " ")
	m, cmd := press(t, m, "m")
	if m.state != stateList || cmd != nil || !strings.Contains(m.status, "select 2 or more") {
		t.Errorf("state %v, status %q", m.state, m.status)
	}
}

// Space on a selected row unselects it.
func TestSpaceTogglesSelection(t *testing.T) {
	m := mergeModel(t)
	m, _ = press(t, m, " ")
	m, _ = press(t, m, "up")
	m, _ = press(t, m, " ")
	if len(m.mergeSelected) != 0 {
		t.Errorf("selection = %v", m.mergeSelected)
	}
}

func TestMergeResultReturnsToList(t *testing.T) {
	m := mergeModel(t)
	m, _ = press(t, m, " ")
	m, _ = press(t, m, " ")
	m, _ = press(t, m, "m")
	m.merging = true
	m.state = stateHandoffWorking

	// A failure goes back to the list and keeps the selection for a retry.
	next, _ := m.Update(handoffErrMsg{errors.New("model overloaded")})
	failed := asModel(next)
	if failed.state != stateList || len(failed.mergeSelected) != 2 || !strings.Contains(failed.status, "merge error: model overloaded") {
		t.Errorf("after error: state %v, selection %v, status %q", failed.state, failed.mergeSelected, failed.status)
	}

	w := m.writers[1]
	next, cmd := m.Update(handoffWrittenMsg{sessionID: "s1", writer: w, count: 2,
		source: m.mergeGroups[1].Primary, record: domain.Handoff{ID: "h"}})
	m = asModel(next)
	if m.state != stateHandoffConfirm || cmd == nil || len(m.mergeSelected) != 0 {
		t.Fatalf("after merge: state %v, selection %v", m.state, m.mergeSelected)
	}
	if view := m.View(); !strings.Contains(view, "Merged 2 threads into a new pi session.") {
		t.Errorf("confirm dialog:\n%s", view)
	}
	m, _ = press(t, m, "n")
	if m.state != stateList || m.merging {
		t.Errorf("after confirm: state %v, merging %v", m.state, m.merging)
	}
}

// A row that is a conversation across tools is merged as one thread, its
// sessions' messages in time order, run where it was last active.
func TestMergeSourceFlattensConversation(t *testing.T) {
	claude := listThread("claude-a", "claude", "Fix", 1)
	pi := listThread("pi-b", "pi", "", 2)
	pi.CanonicalID = "claude-a"
	src := mergeSource(claude, []domain.Thread{pi, claude})
	if src.ID != "claude-a" || src.Title != "Fix" || src.WorkspacePath != "/proj/pi-b" {
		t.Errorf("source: %+v", src)
	}
	var ids []string
	for _, m := range src.Messages {
		ids = append(ids, m.ID+"@"+m.Provider)
	}
	if got := strings.Join(ids, " "); got != "claude-a:1@claude claude-a:2@claude pi-b:1@pi pi-b:2@pi" {
		t.Errorf("messages: %s", got)
	}
}
