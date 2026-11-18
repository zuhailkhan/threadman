package tui

import (
	"errors"

	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/zuhailkhan/threadman/internal/handoff"
)

func typeInto(t *testing.T, m Model, text string) Model {
	t.Helper()
	for _, r := range text {
		m, _ = press(t, m, string(r))
	}
	return m
}

func titles(cs []command) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.title
	}
	return out
}

func TestPaletteOpensAndRunsACommand(t *testing.T) {
	m := mergeModel(t)
	m, _ = press(t, m, ":")
	if m.palette == nil {
		t.Fatal(": didn't open the palette")
	}
	if view := m.View(); !strings.Contains(view, "Commands") || !strings.Contains(view, "Rename thread") {
		t.Errorf("palette not drawn:\n%s", view)
	}

	// j and k are typed into the filter, not used to move.
	m = typeInto(t, m, "rename")
	if got := titles(m.paletteCommands()); len(got) == 0 || got[0] != "Rename thread" {
		t.Fatalf("filter \"rename\" gave %v", got)
	}
	m, _ = press(t, m, "enter")
	if m.palette != nil || m.state != stateRename {
		t.Errorf("palette open %v, state %v; want the rename dialog", m.palette != nil, m.state)
	}
}

func TestPaletteCtrlPAndEsc(t *testing.T) {
	m := mergeModel(t)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	m = asModel(next)
	if m.palette == nil {
		t.Fatal("ctrl+p didn't open the palette")
	}
	m = typeInto(t, m, "jk")
	if m.palette.query != "jk" || m.cursor != 0 {
		t.Errorf("query %q, list cursor %d", m.palette.query, m.cursor)
	}
	m, _ = press(t, m, "esc")
	if m.palette != nil || m.state != stateList {
		t.Errorf("esc left palette open %v, state %v", m.palette != nil, m.state)
	}
}

func TestPaletteShowsWhyACommandCantRun(t *testing.T) {
	m := mergeModel(t)
	m, _ = press(t, m, ":")
	m = typeInto(t, m, "merge selected")
	if view := m.View(); !strings.Contains(view, "select 2 or more") {
		t.Errorf("reason not shown:\n%s", view)
	}
	m, _ = press(t, m, "enter")
	if m.palette == nil || m.state != stateList {
		t.Error("an unavailable command ran or closed the palette")
	}
}

func TestPaletteSelectsForMerge(t *testing.T) {
	m := mergeModel(t)
	for range 2 {
		m, _ = press(t, m, ":")
		m = typeInto(t, m, "select or")
		m, _ = press(t, m, "enter")
	}
	if len(m.mergeSelected) != 2 {
		t.Fatalf("selected %v", m.mergeSelected)
	}
	m, _ = press(t, m, ":")
	m = typeInto(t, m, "merge selected")
	m, _ = press(t, m, "enter")
	if m.state != stateMergePicker {
		t.Errorf("state %v, want the merge dialog", m.state)
	}
}

func TestPaletteListsOnlyThisScreensCommands(t *testing.T) {
	m := mergeModel(t)
	list := strings.Join(titles(m.paletteCommands()), "|")
	if strings.Contains(list, "Hand off") || !strings.Contains(list, "Delete thread") {
		t.Errorf("thread list commands: %s", list)
	}

	m.state = stateThread
	list = strings.Join(titles(m.paletteCommands()), "|")
	if !strings.Contains(list, "Hand off") || strings.Contains(list, "Delete thread") {
		t.Errorf("reader commands: %s", list)
	}
}

func TestPaletteMatchesLettersInOrder(t *testing.T) {
	m := mergeModel(t)
	m.state = stateThread
	m.palette = &palette{query: "hof"}
	if got := titles(m.paletteCommands()); len(got) != 1 || got[0] != "Hand off to another tool" {
		t.Errorf("\"hof\" gave %v", got)
	}
	// A title containing the query comes before one that only has its letters.
	m.palette.query = "top"
	if got := titles(m.paletteCommands()); len(got) == 0 || got[0] != "Go to top" {
		t.Errorf("\"top\" gave %v", got)
	}

	// A word starting with the query comes before a word containing it.
	m.state = stateList
	m.palette.query = "arch"
	if got := titles(m.paletteCommands()); len(got) != 3 || got[2] != "Search threads" {
		t.Errorf("\"arch\" gave %v", got)
	}
}

func TestCheckboxesShowOnceAThreadIsPicked(t *testing.T) {
	m := mergeModel(t)
	if view := m.View(); strings.Contains(view, "[ ]") || strings.Contains(view, "[x]") {
		t.Errorf("checkboxes shown with nothing picked:\n%s", view)
	}
	m, _ = press(t, m, " ")
	view := m.View()
	if strings.Count(view, "[x]") != 1 || strings.Count(view, "[ ]") != 2 {
		t.Errorf("want one ticked and two empty boxes:\n%s", view)
	}
	m, _ = press(t, m, "esc")
	if view := m.View(); strings.Contains(view, "[ ]") {
		t.Errorf("checkboxes still shown after clearing:\n%s", view)
	}
}

func TestPaletteCheckLLMReportsTheResult(t *testing.T) {
	check := func(m Model, s handoff.Status) Model {
		t.Helper()
		m, _ = press(t, m, ":")
		m = typeInto(t, m, "check llm")
		m, cmd := press(t, m, "enter")
		if cmd == nil || !strings.Contains(m.View(), "checking the LLM") {
			t.Fatalf("no check started, or nothing shown while it runs")
		}
		next, _ := m.Update(llmStatusMsg{s})
		return asModel(next)
	}

	m := check(mergeModel(t), handoff.Status{Configured: true, Label: "Ollama · llama3.2"})
	if !strings.Contains(m.View(), "Ollama · llama3.2 ready") || m.failure != nil {
		t.Errorf("a working LLM isn't reported:\n%s", m.View())
	}

	m = check(mergeModel(t), handoff.Status{Configured: true, Label: "LM Studio · gemma", Err: errors.New("connection refused")})
	if m.failure == nil || !strings.Contains(m.View(), "connection refused") {
		t.Errorf("the error isn't shown in full:\n%s", m.View())
	}

	// A check threadman starts by itself stays quiet.
	m = mergeModel(t)
	next, _ := m.Update(llmStatusMsg{handoff.Status{Configured: true, Label: "x", Err: errors.New("down")}})
	if m = asModel(next); m.failure != nil || m.status != "" {
		t.Errorf("a background check reported itself: status %q", m.status)
	}
}
