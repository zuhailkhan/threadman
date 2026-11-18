package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Each server's "too long" error is recognised, with the limit when it says.
func TestContextErrors(t *testing.T) {
	cases := []struct {
		name  string
		code  int
		body  string
		limit int
	}{
		{"LM Studio prompt", 400, `{"error":{"code":400,"message":"request (89047 tokens) exceeds the available context size (16384 tokens), try increasing it","type":"exceed_context_size_error","n_prompt_tokens":89047,"n_ctx":16384}}`, 16384},
		{"LM Studio reply", 500, `{"error":{"code":500,"message":"Context size has been exceeded.","type":"server_error"}}`, 0},
		{"OpenAI", 400, `{"error":{"message":"This model's maximum context length is 8192 tokens. However, your messages resulted in 9000 tokens.","code":"context_length_exceeded"}}`, 8192},
		{"Ollama", 400, `{"error":"the input length exceeds the context length"}`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.code)
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c, _ := New(Config{Provider: "lmstudio", Model: "m", BaseURL: srv.URL})
			_, err := c.Complete(context.Background(), "s", "u")
			var ce *ContextError
			if !errors.As(err, &ce) {
				t.Fatalf("err = %v, want a ContextError", err)
			}
			if ce.Limit != tc.limit {
				t.Errorf("limit = %d, want %d", ce.Limit, tc.limit)
			}
		})
	}

	// Other errors are left alone.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer srv.Close()
	c, _ := New(Config{Provider: "lmstudio", Model: "m", BaseURL: srv.URL})
	if _, err := c.Complete(context.Background(), "s", "u"); IsContextError(err) {
		t.Errorf("a bad key was taken for a context error: %v", err)
	}
	if !IsContextError(asContextError(errors.New("x"), "prompt is too long: 250000 tokens > 200000 maximum")) {
		t.Error("Anthropic's error not recognised")
	}
	if ce := asContextError(errors.New("x"), "prompt is too long: 250000 tokens > 200000 maximum").(*ContextError); ce.Limit != 200000 {
		t.Errorf("Anthropic limit = %d", ce.Limit)
	}
}

func limitsOf(t *testing.T, cfg Config) Limits {
	t.Helper()
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c.(Limiter).Limits(context.Background())
}

func TestLMStudioReportsLoadedContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"models":[{"key":"google/gemma-4-12b","loaded_instances":[{"id":"google/gemma-4-12b","config":{"context_length":100608,"parallel":1}}]}]}`))
	}))
	defer srv.Close()
	got := limitsOf(t, Config{Provider: "lmstudio", Model: "google/gemma-4-12b", BaseURL: srv.URL + "/v1"})
	if got.Context != 100608 || !got.Local {
		t.Errorf("limits = %+v", got)
	}
}

// Older LM Studio versions only serve the v0 API.
func TestLMStudioFallsBackToV0(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v0/models" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"data":[{"id":"m","state":"loaded","max_context_length":262144,"loaded_context_length":16384}]}`))
	}))
	defer srv.Close()
	if got := limitsOf(t, Config{Provider: "lmstudio", Model: "m", BaseURL: srv.URL + "/v1"}); got.Context != 16384 {
		t.Errorf("limits = %+v, want the loaded length", got)
	}
}

func TestOllamaReportsRunningContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ps" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"models":[{"name":"llama3.2:latest","model":"llama3.2:latest","context_length":8192}]}`))
	}))
	defer srv.Close()
	if got := limitsOf(t, Config{Provider: "ollama", Model: "llama3.2", BaseURL: srv.URL + "/v1"}); got.Context != 8192 || !got.Local {
		t.Errorf("limits = %+v", got)
	}
}

func TestContextSettingWins(t *testing.T) {
	if got := limitsOf(t, Config{Provider: "ollama", Model: "m", BaseURL: "http://127.0.0.1:1/v1", ContextTokens: 32768}); got.Context != 32768 || !got.Local {
		t.Errorf("limits = %+v", got)
	}
	if got := limitsOf(t, Config{Provider: "claude", Model: "claude-opus-5-5", ContextTokens: 50000}); got.Context != 50000 {
		t.Errorf("claude limits = %+v", got)
	}
	if got := limitsOf(t, Config{Provider: "claude", Model: "claude-opus-5-5"}); got.Context != claudeContext || got.Local {
		t.Errorf("claude default = %+v", got)
	}
	if _, err := Save(t.TempDir()+"/c.json", Config{Provider: "ollama", Model: "m", ContextTokens: -1}); err == nil {
		t.Error("a negative context was accepted")
	}
}

// An unreachable local server reports an unknown context rather than failing.
func TestUnreachableServerHasUnknownContext(t *testing.T) {
	if got := limitsOf(t, Config{Provider: "lmstudio", Model: "m", BaseURL: "http://127.0.0.1:1/v1"}); got.Context != 0 || !got.Local {
		t.Errorf("limits = %+v", got)
	}
}
