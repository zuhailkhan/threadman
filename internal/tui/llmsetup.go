package tui

import (
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
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
	// A context window set with 'threadman llm set --context' belongs to the
	// model it was set for, and the form has no field for it.
	if f.hasSaved && f.saved.Provider == cfg.Provider && f.saved.Model == strings.TrimSpace(cfg.Model) {
		cfg.ContextTokens = f.saved.ContextTokens
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
	f := m.llmSetup
	info := f.info()

	// value renders a text field: what was typed, or a dimmed placeholder.
	value := func(field int, typed, placeholder string) string {
		cursor := ""
		if f.focus == field {
			cursor = styleCursor.Render("▏")
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

	pw := dialogWidth(64, m.width)
	iw := pw - 4
	lines := []string{styleHint.Render("Summarizes threads for handoffs and merges."), ""}
	for _, field := range f.fields() {
		r := choiceRow(field == f.focus)
		r.add(styleFaint, fmt.Sprintf("%-10s", rows[field][0]))
		lines = append(lines, r.String()+rows[field][1])
	}
	lines = append(lines, "")
	if f.err != "" {
		lines = append(lines, wrapStyled(f.err, iw, styleErr)...)
		lines = append(lines, "")
	}
	lines = append(lines,
		renderKeys([]keyHint{{"↑↓", "field"}, {"←→", "provider"}, {"↵", "save and check"}}).String(),
		renderKeys([]keyHint{{"esc", "cancel"}, {"ctrl+d", "remove"}}).String())
	return overlay(m.baseFor(f.returnTo), panel("LLM", styleCursor, colorFaint, lines, pw), m.width, m.height)
}
