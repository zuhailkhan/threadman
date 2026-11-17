package tui

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/zuhailkhan/threadman/internal/domain"
)

// Resizing the window used to replace the reader with an empty one.
func TestResizeKeepsReaderContent(t *testing.T) {
	claude, opencode := roundTripGroup()
	m := Model{state: stateThread, thread: claude}

	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = asModel(next)
	next, _ = m.Update(groupLoadedMsg{primary: claude, members: []domain.Thread{claude, opencode}, initial: true})
	m = asModel(next)

	next, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = asModel(next)
	if v := m.viewport.View(); !strings.Contains(v, "o5 tests added") {
		t.Errorf("reader is empty after a resize:\n%s", v)
	}
	if m.viewport.Width != 120 {
		t.Errorf("viewport width %d, want 120", m.viewport.Width)
	}
}

func TestLaunchReportsMissingWorkspace(t *testing.T) {
	cmd := exec.Command("claude", "--resume", "abc")
	cmd.Dir = filepath.Join(t.TempDir(), "gone")

	msg := launch(cmd)()
	done, ok := msg.(launchDoneMsg)
	if !ok || done.err == nil || !strings.Contains(done.err.Error(), "no longer exists") {
		t.Fatalf("launch = %#v", msg)
	}

	m := Model{state: stateThread}
	next, _ := m.Update(done)
	if got := asModel(next).status; !strings.Contains(got, "could not run claude --resume abc") || !strings.Contains(got, "no longer exists") {
		t.Errorf("status %q", got)
	}
}

func TestLaunchErrorIsShown(t *testing.T) {
	m := Model{state: stateThread}
	next, _ := m.Update(launchDoneMsg{command: "opencode -s ses_1", err: errors.New(`exec: "opencode": executable file not found`)})
	if got := asModel(next).status; !strings.Contains(got, "could not run opencode -s ses_1") || !strings.Contains(got, "executable file not found") {
		t.Errorf("status %q", got)
	}

	// A session that ran and exited normally leaves the status alone.
	m.status = "kept"
	next, _ = m.Update(launchDoneMsg{command: "claude"})
	if got := asModel(next).status; got != "kept" {
		t.Errorf("status %q after a clean exit", got)
	}
}

func TestListRowTruncatesByCharacter(t *testing.T) {
	g := ThreadGroup{Primary: domain.Thread{
		Provider:      "claude",
		Title:         strings.Repeat("修正", 40),
		WorkspacePath: "/work/" + strings.Repeat("é", 30),
	}}
	row := renderListRow(1, g, 80)
	if !utf8.ValidString(row) {
		t.Errorf("row has a broken character: %q", row)
	}
	if !strings.Contains(row, "...") {
		t.Errorf("long title was not shortened: %q", row)
	}
	if got := truncateRunes("héllo wörld", 8); got != "héllo..." {
		t.Errorf("truncateRunes = %q", got)
	}
	if got := truncateRunes("short", 8); got != "short" {
		t.Errorf("truncateRunes = %q", got)
	}
}
