package tui

import (
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/zuhailkhan/threadman/internal/llm"
)

// Fields of the LLM setup form, in display order.
const (
	llmFieldProvider = iota
	llmFieldModel
	llmFieldBaseURL
	llmFieldAPIKey
)

// llmSetupForm is the in-progress state of the LLM setup dialog.
type llmSetupForm struct {
	providers []string
	provider  int // index into providers
	model     string
	baseURL   string
	apiKey    string
	focus     int
	err       string
	returnTo  appState
	saved     llm.Config // what is on disk, to carry over an existing key
	hasSaved  bool
}

func (f llmSetupForm) info() llm.ProviderInfo {
	info, _ := llm.Info(f.providers[f.provider])
	return info
}

// fields lists the fields shown for the selected provider; local servers have
// no API key.
func (f llmSetupForm) fields() []int {
	fields := []int{llmFieldProvider, llmFieldModel, llmFieldBaseURL}
	if f.info().KeyEnv != "" {
		fields = append(fields, llmFieldAPIKey)
	}
	return fields
}

// fileKey reports whether the config file holds a key for this provider,
// which happens only when no system keyring was available.
func (f llmSetupForm) fileKey() bool {
	return f.hasSaved && f.saved.Provider == f.info().Name && f.saved.APIKey != ""
}

// keptKey reports whether leaving the key blank keeps one already stored for
// this provider.
func (f llmSetupForm) keptKey() bool {
	return f.fileKey() || llm.HasStoredKey(f.info().Name)
}

// loadProvider fills the text fields for the selected provider: the saved
// values when it is the configured one, otherwise its defaults.
func (f *llmSetupForm) loadProvider() {
	info := f.info()
	f.model, f.baseURL, f.apiKey = info.DefaultModel, "", ""
	if f.hasSaved && f.saved.Provider == info.Name {
		f.model, f.baseURL = f.saved.Model, f.saved.BaseURL
	}
	if f.focus == llmFieldAPIKey && info.KeyEnv == "" {
		f.focus = llmFieldBaseURL
	}
}

func (f *llmSetupForm) text() *string {
	switch f.focus {
	case llmFieldModel:
		return &f.model
	case llmFieldBaseURL:
		return &f.baseURL
	case llmFieldAPIKey:
		return &f.apiKey
	}
	return nil
}

func (f *llmSetupForm) moveFocus(delta int) {
	fields := f.fields()
	for i, field := range fields {
		if field == f.focus {
			f.focus = fields[(i+delta+len(fields))%len(fields)]
			return
		}
	}
	f.focus = fields[0]
}

func (f llmSetupForm) config() llm.Config {
	cfg := llm.Config{
		Provider: f.info().Name,
		Model:    f.model,
		BaseURL:  f.baseURL,
		APIKey:   f.apiKey,
	}
	// A key in the keyring stays there untouched; one in the file has to be
	// carried over or rewriting the file would drop it.
	if strings.TrimSpace(cfg.APIKey) == "" && f.fileKey() {
		cfg.APIKey = f.saved.APIKey
	}
	return cfg
}

// openLLMSetup shows the setup dialog, returning to the current screen when
// it closes.
func (m *Model) openLLMSetup() {
	f := llmSetupForm{providers: llm.Providers(), returnTo: m.state}
	f.saved, f.hasSaved = m.summarizer.Config()
	if f.hasSaved {
		for i, name := range f.providers {
			if name == f.saved.Provider {
				f.provider = i
			}
		}
	}
	f.loadProvider()
	f.focus = llmFieldProvider
	m.llmSetup = f
	m.state = stateLLMSetup
}

func (m *Model) handleLLMSetupKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	f := &m.llmSetup
	f.err = ""

	switch msg.String() {
	case "esc":
		m.state = f.returnTo
	case "up", "shift+tab":
		f.moveFocus(-1)
	case "down", "tab":
		f.moveFocus(1)
	case "left", "right":
		if f.focus != llmFieldProvider {
			break
		}
		step := 1
		if msg.String() == "left" {
			step = -1
		}
		f.provider = (f.provider + step + len(f.providers)) % len(f.providers)
		f.loadProvider()
	case "ctrl+d":
		if err := m.summarizer.ClearConfig(); err != nil {
			f.err = err.Error()
			break
		}
		m.state = f.returnTo
		m.status = "LLM configuration removed"
		m.llmChecking = true
		return m, m.checkLLM()
	case "enter":
		saved, err := m.summarizer.SaveConfig(f.config())
		if err != nil {
			f.err = err.Error()
			break
		}
		m.state = f.returnTo
		m.status = "LLM saved: " + saved.Label()
		m.llmChecking = true
		return m, m.checkLLM()
	case "backspace":
		if t := f.text(); t != nil && *t != "" {
			_, size := utf8.DecodeLastRuneInString(*t)
			*t = (*t)[:len(*t)-size]
		}
	default:
		// Runes arrive one at a time when typed and all at once when pasted.
		if t := f.text(); t != nil && (msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace) {
			*t += strings.Map(func(r rune) rune {
				if r == '\n' || r == '\r' || r == '\t' {
					return -1
				}
				return r
			}, string(msg.Runes))
		}
	}
	return m, nil
}

func (m Model) viewLLMSetup() string {
	dialogWidth := 64
	f := m.llmSetup
	info := f.info()

	// value renders a text field: what was typed, or a dimmed placeholder.
	value := func(field int, typed, placeholder string) string {
		cursor := ""
		if f.focus == field {
			cursor = "_"
		}
		if typed == "" {
			return cursor + styleHint.Render(placeholder)
		}
		return typed + cursor
	}

	keyPlaceholder := "not set"
	switch {
	case f.keptKey():
		keyPlaceholder = "saved key kept; type to replace"
	case info.KeyEnv != "" && os.Getenv(info.KeyEnv) != "":
		keyPlaceholder = "using $" + info.KeyEnv
	case info.NeedsKey:
		keyPlaceholder = "required (or set $" + info.KeyEnv + ")"
	case info.Name == llm.ProviderClaude:
		keyPlaceholder = "optional; uses Anthropic default credentials"
	}

	modelPlaceholder := "required"
	if info.DefaultModel != "" {
		modelPlaceholder = info.DefaultModel
	}
	basePlaceholder := info.BaseURL + " (default)"
	if info.BaseURL == "" {
		basePlaceholder = "default"
	}

	rows := map[int][2]string{
		llmFieldProvider: {"Provider", "◀ " + info.Label + " ▶"},
		llmFieldModel:    {"Model", value(llmFieldModel, f.model, modelPlaceholder)},
		llmFieldBaseURL:  {"Base URL", value(llmFieldBaseURL, f.baseURL, basePlaceholder)},
		llmFieldAPIKey:   {"API key", value(llmFieldAPIKey, strings.Repeat("•", utf8.RuneCountInString(f.apiKey)), keyPlaceholder)},
	}

	var inner strings.Builder
	inner.WriteString("\n")
	inner.WriteString("  LLM for handoff summaries\n")
	inner.WriteString("\n")
	for _, field := range f.fields() {
		marker := " "
		if field == f.focus {
			marker = styleCursor.Render("▶")
		}
		inner.WriteString(fmt.Sprintf("%s %-9s  %s\n", marker, rows[field][0], rows[field][1]))
	}
	inner.WriteString("\n")
	if f.err != "" {
		reason := lipgloss.NewStyle().Width(dialogWidth - 4).Render(f.err)
		for _, line := range strings.Split(reason, "\n") {
			inner.WriteString("  " + line + "\n")
		}
		inner.WriteString("\n")
	}
	inner.WriteString(styleHint.Render("  ↑↓ field   ←→ provider   Enter save and check") + "\n")
	inner.WriteString(styleHint.Render("  Esc cancel   Ctrl+D remove configuration") + "\n")
	inner.WriteString("\n")

	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#444444")).
		Width(dialogWidth).
		Render(inner.String())

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog)
}
