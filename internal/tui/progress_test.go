package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/zuhailkhan/threadman/internal/handoff"
)

// workResult runs a command started by startWork and returns what the work
// itself produced: the batch's first command, which closes the progress
// channel when it ends.
func workResult(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		return batch[0]()
	}
	return msg
}

// While a summary runs, the panel shows a spinner, the time taken and how far
// through the parts it is; reports from a cancelled run are ignored.
func TestWorkingStepShowsProgress(t *testing.T) {
	m := mergeModel(t)
	m.width, m.height = 100, 30
	m, _ = press(t, m, " ")
	m, _ = press(t, m, " ")
	m, _ = press(t, m, "m")
	m.llmChecking = false

	block := make(chan struct{})
	cmd := m.startWork(t.Context(), func(ctx context.Context) tea.Cmd {
		return func() tea.Msg { <-block; return nil }
	})
	defer close(block)
	_ = cmd
	m.state, m.merging = stateHandoffWorking, true

	before := m.View()
	if !strings.Contains(before, "0:0") || !strings.Contains(before, spinnerFrames[0]) {
		t.Errorf("no spinner or time before any progress:\n%s", before)
	}

	next, _ := m.Update(spinMsg{start: m.workStart})
	m = asModel(next)
	if m.spin != 1 || !strings.Contains(m.View(), spinnerFrames[1]) {
		t.Errorf("spinner didn't advance (spin %d)", m.spin)
	}

	ch := m.progressCh
	next, follow := m.Update(progressMsg{ch: ch, p: handoff.Progress{Stage: handoff.StageReading, Part: 3, Parts: 9, Thread: 2, Threads: 2, Done: 1}})
	m = asModel(next)
	if follow == nil {
		t.Error("the panel stopped listening for progress")
	}
	view := m.View()
	for _, want := range []string{"Reading part 3 of 9", "Thread 2 of 2", "1/2 done", "━"} {
		if !strings.Contains(view, want) {
			t.Errorf("panel missing %q:\n%s", want, view)
		}
	}

	// A report from an earlier run is dropped, and so is its spinner.
	old := make(chan handoff.Progress)
	next, _ = m.Update(progressMsg{ch: old, p: handoff.Progress{Stage: handoff.StageCondensing, Part: 1, Parts: 2}})
	if asModel(next).progress.Stage != handoff.StageReading {
		t.Error("a stale report replaced the current one")
	}
	if _, c := m.Update(spinMsg{start: m.workStart.Add(-time.Second)}); c != nil {
		t.Error("a stale spinner kept ticking")
	}
}
