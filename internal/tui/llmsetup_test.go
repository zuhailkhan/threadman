package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/zalando/go-keyring"
	"github.com/zuhailkhan/threadman/internal/handoff"
	"github.com/zuhailkhan/threadman/internal/llm"
)

// Keep tests away from the real system keyring.
func TestMain(m *testing.M) {
	keyring.MockInit()
	os.Exit(m.Run())
}

func pressKey(t *testing.T, m Model, k tea.KeyType) Model {
	t.Helper()
	next, _ := m.Update(tea.KeyMsg{Type: k})
	return asModel(next)
}

func typeText(t *testing.T, m Model, text string) Model {
	t.Helper()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text)})
	return asModel(next)
}

func TestLLMSetupForm(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	s := handoff.NewSummarizer(filepath.Join(t.TempDir(), "config.json"))
	m := Model{state: stateList, width: 100, height: 30, summarizer: s}

	m, _ = press(t, m, "L")
	if m.state != stateLLMSetup {
		t.Fatalf("state after L = %v", m.state)
	}
	if view := m.View(); !strings.Contains(view, "Claude") || !strings.Contains(view, "API key") {
		t.Errorf("form should open on Claude with an API key field:\n%s", view)
	}

	// Claude → ChatGPT, which has no default model: saving must be refused.
	m = pressKey(t, m, tea.KeyRight)
	m = pressKey(t, m, tea.KeyEnter)
	if m.state != stateLLMSetup || !strings.Contains(m.View(), "needs a model name") {
		t.Fatalf("saving without a model should show an error:\n%s", m.View())
	}

	// ChatGPT → DeepSeek → Ollama, which has no API key field.
	m = pressKey(t, m, tea.KeyRight)
	m = pressKey(t, m, tea.KeyRight)
	if view := m.View(); !strings.Contains(view, "Ollama") || strings.Contains(view, "API key") {
		t.Errorf("Ollama should hide the API key field:\n%s", view)
	}

	m = pressKey(t, m, tea.KeyDown)
	m = typeText(t, m, "llama3.2x")
	m = pressKey(t, m, tea.KeyBackspace)
	m = pressKey(t, m, tea.KeyDown)
	m = typeText(t, m, "http://box:11434/v1") // pasted in one go

	next, check := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = asModel(next)
	if m.state != stateList || check == nil || !m.llmChecking {
		t.Fatalf("after save: state %v, checking %v", m.state, m.llmChecking)
	}
	got, ok := s.Config()
	want := llm.Config{Provider: "ollama", Model: "llama3.2", BaseURL: "http://box:11434/v1"}
	if !ok || got != want {
		t.Fatalf("saved config = %+v (ok %v), want %+v", got, ok, want)
	}

	// Reopening shows the saved values; Ctrl+D removes them.
	m, _ = press(t, m, "L")
	if view := m.View(); !strings.Contains(view, "llama3.2") || !strings.Contains(view, "http://box:11434/v1") {
		t.Errorf("form should reopen with the saved values:\n%s", view)
	}
	m = pressKey(t, m, tea.KeyCtrlD)
	if _, ok := s.Config(); ok || m.state != stateList {
		t.Errorf("Ctrl+D should remove the configuration and close the form")
	}
}

// Leaving the key blank when editing keeps the key already on disk.
func TestLLMSetupKeepsSavedKey(t *testing.T) {
	s := handoff.NewSummarizer(filepath.Join(t.TempDir(), "config.json"))
	if _, err := s.SaveConfig(llm.Config{Provider: "deepseek", Model: "deepseek-chat", APIKey: "secret"}); err != nil {
		t.Fatal(err)
	}
	m := Model{state: stateList, width: 100, height: 30, summarizer: s}

	m, _ = press(t, m, "L")
	view := m.View()
	if !strings.Contains(view, "DeepSeek") || !strings.Contains(view, "saved key kept") || strings.Contains(view, "secret") {
		t.Fatalf("form should open on the saved provider without showing the key:\n%s", view)
	}
	m = pressKey(t, m, tea.KeyDown)
	m = typeText(t, m, "-v2")
	m = pressKey(t, m, tea.KeyEnter)

	got, _ := s.Config()
	if got.Model != "deepseek-chat-v2" || got.APIKey != "" {
		t.Errorf("saved config = %+v", got)
	}
	if key, err := keyring.Get("threadman", "deepseek"); err != nil || key != "secret" {
		t.Errorf("stored key = %q, %v; want it kept", key, err)
	}
}
