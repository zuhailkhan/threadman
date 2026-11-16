package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// Keep tests away from the real system keyring.
func TestMain(m *testing.M) {
	keyring.MockInit()
	os.Exit(m.Run())
}

func TestKeyGoesToKeyring(t *testing.T) {
	keyring.MockInit()
	t.Setenv("OPENAI_API_KEY", "from-env")
	path := filepath.Join(t.TempDir(), "config.json")

	saved, err := Save(path, Config{Provider: "openai", Model: "m", APIKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if saved.APIKey != "" || strings.Contains(string(data), "secret") {
		t.Fatalf("key written to the config file: %s", data)
	}
	if !HasStoredKey("openai") || saved.KeySource() != "system keyring" {
		t.Fatalf("key not in keyring; source = %q", saved.KeySource())
	}

	loaded, _, _ := Load(path)
	c, err := New(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.(*openAIClient).apiKey; got != "secret" {
		t.Errorf("client key = %q, want the keyring's over the environment's", got)
	}

	// Saving again with a blank key keeps the stored one.
	if _, err := Save(path, Config{Provider: "openai", Model: "m2"}); err != nil {
		t.Fatal(err)
	}
	if !HasStoredKey("openai") {
		t.Error("blank key wiped the stored key")
	}

	if err := Clear(path); err != nil {
		t.Fatal(err)
	}
	if HasStoredKey("openai") {
		t.Error("Clear left the key in the keyring")
	}
}

func TestKeyFallsBackToFile(t *testing.T) {
	keyring.MockInitWithError(errors.New("no keyring"))
	defer keyring.MockInit()
	path := filepath.Join(t.TempDir(), "config.json")

	saved, err := Save(path, Config{Provider: "deepseek", APIKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if saved.APIKey != "secret" || !strings.HasPrefix(saved.KeySource(), "config file") {
		t.Fatalf("without a keyring the key should stay in the file: %+v, %q", saved, saved.KeySource())
	}
	loaded, _, _ := Load(path)
	c, err := New(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if c.(*openAIClient).apiKey != "secret" {
		t.Error("file key not used")
	}
}

func TestSaveLoadClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")

	if _, ok, err := Load(path); err != nil || ok {
		t.Fatalf("Load on missing file = ok %v, err %v; want not configured", ok, err)
	}

	saved, err := Save(path, Config{Provider: " Ollama ", Model: "llama3.2", BaseURL: "http://host:1/v1/"})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Provider != "ollama" || saved.BaseURL != "http://host:1/v1" {
		t.Fatalf("Save did not normalize: %+v", saved)
	}

	got, ok, err := Load(path)
	if err != nil || !ok {
		t.Fatalf("Load = ok %v, err %v", ok, err)
	}
	if got != saved {
		t.Fatalf("Load = %+v, want %+v", got, saved)
	}
	if got.Label() != "Ollama · llama3.2" {
		t.Fatalf("Label = %q", got.Label())
	}

	if err := Clear(path); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := Load(path); ok {
		t.Fatal("still configured after Clear")
	}
}

func TestSaveValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")

	if _, err := Save(path, Config{Provider: "gemini", Model: "x"}); err == nil {
		t.Error("unknown provider accepted")
	}
	if _, err := Save(path, Config{Provider: "openai"}); err == nil {
		t.Error("openai without a model accepted")
	}
	cfg, err := Save(path, Config{Provider: "claude"})
	if err != nil || cfg.Model == "" {
		t.Errorf("claude should get a default model: %+v, %v", cfg, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Error(err)
	}
}

func TestNewRequiresKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	if _, err := New(Config{Provider: "openai", Model: "m"}); err == nil {
		t.Error("openai without a key accepted")
	}
	t.Setenv("OPENAI_API_KEY", "from-env")
	c, err := New(Config{Provider: "openai", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if c.(*openAIClient).apiKey != "from-env" {
		t.Error("env key not used")
	}
	if _, err := New(Config{Provider: "ollama", Model: "m"}); err != nil {
		t.Errorf("ollama should not need a key: %v", err)
	}
}

func TestOpenAIComplete(t *testing.T) {
	var gotAuth string
	var gotBody struct {
		Model    string        `json:"model"`
		Messages []chatMessage `json:"messages"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"<think>hmm</think>\n the summary "},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	c, err := New(Config{Provider: "deepseek", Model: "deepseek-chat", BaseURL: srv.URL + "/v1", APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.Complete(context.Background(), "sys", "usr")
	if err != nil {
		t.Fatal(err)
	}
	if out != "the summary" {
		t.Errorf("out = %q", out)
	}
	if gotAuth != "Bearer k" {
		t.Errorf("auth = %q", gotAuth)
	}
	if gotBody.Model != "deepseek-chat" || len(gotBody.Messages) != 2 ||
		gotBody.Messages[0] != (chatMessage{"system", "sys"}) || gotBody.Messages[1] != (chatMessage{"user", "usr"}) {
		t.Errorf("body = %+v", gotBody)
	}
}

func TestOpenAIErrors(t *testing.T) {
	cases := []struct {
		name string
		code int
		body string
		want string
	}{
		{name: "nested error", code: 401, body: `{"error":{"message":"bad key"}}`, want: "bad key"},
		{name: "plain error", code: 404, body: `{"error":"model not found"}`, want: "model not found"},
		{name: "truncated", code: 200, body: `{"choices":[{"message":{"content":"partial"},"finish_reason":"length"}]}`, want: "ran out of room"},
		{name: "empty", code: 200, body: `{"choices":[{"message":{"content":""},"finish_reason":"stop"}]}`, want: "empty reply"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.code)
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c, _ := New(Config{Provider: "ollama", Model: "m", BaseURL: srv.URL})
			_, err := c.Complete(context.Background(), "s", "u")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestOpenAIPing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"llama3.2:latest"},{"id":"qwen"}]}`))
	}))

	for model, wantOK := range map[string]bool{"llama3.2": true, "qwen": true, "missing": false} {
		c, _ := New(Config{Provider: "ollama", Model: model, BaseURL: srv.URL})
		if err := c.Ping(context.Background()); (err == nil) != wantOK {
			t.Errorf("Ping with model %q: err = %v, want ok %v", model, err, wantOK)
		}
	}

	srv.Close()
	c, _ := New(Config{Provider: "ollama", Model: "qwen", BaseURL: srv.URL})
	if err := c.Ping(context.Background()); err == nil || !strings.Contains(err.Error(), "cannot reach") {
		t.Errorf("Ping against a stopped server: err = %v", err)
	}
}
