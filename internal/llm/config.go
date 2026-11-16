// Package llm talks to the language model the user has configured for
// threadman's own use (summarizing a thread on handoff). It is unrelated to
// the coding-agent providers whose threads threadman manages.
package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zalando/go-keyring"
)

// keyringService is the name API keys are filed under in the system keyring
// (Windows Credential Manager, macOS Keychain, Secret Service on Linux); the
// account name is the provider.
const keyringService = "threadman"

// HasStoredKey reports whether the system keyring holds a key for provider.
func HasStoredKey(provider string) bool {
	key, err := keyring.Get(keyringService, provider)
	return err == nil && key != ""
}

const (
	ProviderClaude   = "claude"
	ProviderOpenAI   = "openai"
	ProviderDeepSeek = "deepseek"
	ProviderOllama   = "ollama"
	ProviderLMStudio = "lmstudio"
)

type preset struct {
	label        string
	baseURL      string
	keyEnv       string // env var read when the config holds no key
	needsKey     bool
	defaultModel string
}

var presets = map[string]preset{
	ProviderClaude:   {label: "Claude", keyEnv: "ANTHROPIC_API_KEY", defaultModel: "claude-opus-5-5"},
	ProviderOpenAI:   {label: "ChatGPT", baseURL: "https://api.openai.com/v1", keyEnv: "OPENAI_API_KEY", needsKey: true},
	ProviderDeepSeek: {label: "DeepSeek", baseURL: "https://api.deepseek.com/v1", keyEnv: "DEEPSEEK_API_KEY", needsKey: true, defaultModel: "deepseek-chat"},
	ProviderOllama:   {label: "Ollama", baseURL: "http://localhost:11434/v1"},
	ProviderLMStudio: {label: "LM Studio", baseURL: "http://localhost:1234/v1"},
}

// Providers lists the supported provider names in display order.
func Providers() []string {
	return []string{ProviderClaude, ProviderOpenAI, ProviderDeepSeek, ProviderOllama, ProviderLMStudio}
}

// ProviderInfo describes a provider's defaults, for building a settings UI.
type ProviderInfo struct {
	Name         string
	Label        string
	BaseURL      string // default API address; empty when the SDK decides
	KeyEnv       string // environment variable holding the key; empty when no key is used
	NeedsKey     bool
	DefaultModel string
}

// Info returns the defaults for a provider name.
func Info(name string) (ProviderInfo, bool) {
	p, ok := presets[name]
	if !ok {
		return ProviderInfo{}, false
	}
	return ProviderInfo{
		Name:         name,
		Label:        p.label,
		BaseURL:      p.baseURL,
		KeyEnv:       p.keyEnv,
		NeedsKey:     p.needsKey,
		DefaultModel: p.defaultModel,
	}, true
}

type Config struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	BaseURL  string `json:"base_url,omitempty"`
	APIKey   string `json:"api_key,omitempty"`
}

// file is the on-disk shape; the llm block is nested so the file can hold
// other settings later.
type file struct {
	LLM *Config `json:"llm,omitempty"`
}

// DefaultPath is where threadman keeps its settings.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "threadman", "config.json"), nil
}

// Load returns the configured LLM. ok is false when none is configured.
func Load(path string) (cfg Config, ok bool, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, false, nil
	}
	if err != nil {
		return Config{}, false, err
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return Config{}, false, fmt.Errorf("parse %s: %w", path, err)
	}
	if f.LLM == nil || f.LLM.Provider == "" {
		return Config{}, false, nil
	}
	return *f.LLM, true, nil
}

// Save validates cfg, fills in provider defaults and stores it. An API key
// goes into the system keyring and is left out of the file; only when no
// keyring is available does it stay in the file, which is readable by the
// user alone. A blank key leaves any key already stored for the provider in
// place. The returned Config is what was written to the file.
func Save(path string, cfg Config) (Config, error) {
	cfg, err := cfg.normalized()
	if err != nil {
		return Config{}, err
	}
	if cfg.APIKey != "" && keyring.Set(keyringService, cfg.Provider, cfg.APIKey) == nil {
		cfg.APIKey = ""
	}
	return cfg, write(path, file{LLM: &cfg})
}

// Clear removes the LLM configuration and every API key threadman stored.
func Clear(path string) error {
	for _, provider := range Providers() {
		// Missing entries and a missing keyring are both fine here.
		_ = keyring.Delete(keyringService, provider)
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return write(path, file{})
}

func write(path string, f file) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0600)
}

func (c Config) normalized() (Config, error) {
	c.Provider = strings.ToLower(strings.TrimSpace(c.Provider))
	c.Model = strings.TrimSpace(c.Model)
	c.BaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	c.APIKey = strings.TrimSpace(c.APIKey)

	p, ok := presets[c.Provider]
	if !ok {
		return Config{}, fmt.Errorf("unknown provider %q (choose one of: %s)", c.Provider, strings.Join(Providers(), ", "))
	}
	if c.Model == "" {
		c.Model = p.defaultModel
	}
	if c.Model == "" {
		return Config{}, fmt.Errorf("provider %s needs a model name", c.Provider)
	}
	return c, nil
}

// Label is a short human-readable name, e.g. "Ollama · llama3.2".
func (c Config) Label() string {
	label := presets[c.Provider].label
	if label == "" {
		label = c.Provider
	}
	return label + " · " + c.Model
}

func (c Config) baseURL() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return presets[c.Provider].baseURL
}

// storedKey returns the key threadman itself holds for the provider: from the
// config file if one is there, otherwise from the system keyring.
func (c Config) storedKey() string {
	if c.APIKey != "" {
		return c.APIKey
	}
	if key, err := keyring.Get(keyringService, c.Provider); err == nil {
		return key
	}
	return ""
}

// apiKey returns the stored key, falling back to the provider's conventional
// environment variable.
func (c Config) apiKey() string {
	if key := c.storedKey(); key != "" {
		return key
	}
	if env := presets[c.Provider].keyEnv; env != "" {
		return os.Getenv(env)
	}
	return ""
}

// KeySource describes where the API key comes from, without revealing it.
func (c Config) KeySource() string {
	p := presets[c.Provider]
	switch {
	case c.APIKey != "":
		return "config file (no system keyring available)"
	case p.keyEnv != "" && HasStoredKey(c.Provider):
		return "system keyring"
	case p.keyEnv != "" && os.Getenv(p.keyEnv) != "":
		return "$" + p.keyEnv
	case p.needsKey:
		return "missing (set $" + p.keyEnv + " or pass --api-key)"
	case c.Provider == ProviderClaude:
		return "Anthropic SDK default credentials"
	default:
		return "not needed"
	}
}
