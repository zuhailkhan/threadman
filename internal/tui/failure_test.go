package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/zuhailkhan/threadman/internal/llm"
)

// A failed merge is shown in full in a dialog, not only cut down to fit the
// footer, and a context error comes with advice.
func TestMergeFailureIsShownInFull(t *testing.T) {
	m := mergeModel(t)
	m.width, m.height = 100, 30
	m.merging = true
	long := "request (89047 tokens) exceeds the available context size (16384 tokens), try increasing it"
	err := fmt.Errorf(`summarize "fix-threadman-path-restart": %w`, &llm.ContextError{Limit: 16384, Err: errors.New("400 Bad Request: " + long)})

	next, _ := m.Update(handoffErrMsg{err})
	m = asModel(next)
	if m.state != stateList || m.failure == nil {
		t.Fatalf("state %v, failure %v", m.state, m.failure)
	}
	view := m.View()
	flat := strings.Join(strings.Fields(view), " ")
	for _, want := range []string{"Merge failed", "fix-threadman-path-restart", "exceeds the available context size (16384 tokens)", "--context"} {
		if !strings.Contains(flat, want) {
			t.Errorf("dialog missing %q:\n%s", want, view)
		}
	}

	// Esc closes it; the list keys work again.
	m, _ = press(t, m, "esc")
	if m.failure != nil {
		t.Fatal("esc didn't close the dialog")
	}
	if strings.Contains(m.View(), "Merge failed") {
		t.Error("the dialog is still drawn")
	}
}

// Other failures get no context advice.
func TestHandoffFailureWithoutContextAdvice(t *testing.T) {
	m := mergeModel(t)
	m.width, m.height = 100, 30
	next, _ := m.Update(handoffErrMsg{errors.New("401 Unauthorized: bad key")})
	m = asModel(next)
	view := m.View()
	if !strings.Contains(view, "Handoff failed") || !strings.Contains(view, "bad key") || strings.Contains(view, "--context") {
		t.Errorf("dialog:\n%s", view)
	}
}
