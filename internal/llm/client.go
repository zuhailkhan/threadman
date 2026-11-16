package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

// Client is a configured language model.
type Client interface {
	// Complete sends one system prompt and one user message and returns the
	// model's text reply.
	Complete(ctx context.Context, system, user string) (string, error)
	// Ping checks that the server is reachable, the credentials are accepted
	// and the model exists, without generating anything.
	Ping(ctx context.Context) error
}

// New builds a client for cfg.
func New(cfg Config) (Client, error) {
	cfg, err := cfg.normalized()
	if err != nil {
		return nil, err
	}
	if cfg.Provider == ProviderClaude {
		return newAnthropicClient(cfg), nil
	}
	key := cfg.apiKey()
	if key == "" && presets[cfg.Provider].needsKey {
		return nil, fmt.Errorf("no API key for %s: set $%s or run 'threadman llm set --api-key ...'", cfg.Provider, presets[cfg.Provider].keyEnv)
	}
	return &openAIClient{baseURL: cfg.baseURL(), apiKey: key, model: cfg.Model, http: http.DefaultClient}, nil
}

// openAIClient speaks the OpenAI chat-completions API, which ChatGPT,
// DeepSeek, Ollama and LM Studio all serve.
type openAIClient struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Reasoning models served locally (e.g. deepseek-r1 on Ollama) put their
// scratch work inline in the reply.
var thinkBlock = regexp.MustCompile(`(?s)<think>.*?</think>`)

func (c *openAIClient) Complete(ctx context.Context, system, user string) (string, error) {
	body, err := json.Marshal(map[string]any{
		"model": c.model,
		"messages": []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		"stream": false,
	})
	if err != nil {
		return "", err
	}

	var out struct {
		Choices []struct {
			Message      chatMessage `json:"message"`
			FinishReason string      `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := c.do(ctx, http.MethodPost, "/chat/completions", body, &out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("%s returned no choices", c.baseURL)
	}
	text := strings.TrimSpace(thinkBlock.ReplaceAllString(out.Choices[0].Message.Content, ""))
	if out.Choices[0].FinishReason == "length" {
		return "", fmt.Errorf("the model ran out of room before finishing its reply (the conversation may be too long for %s)", c.model)
	}
	if text == "" {
		return "", fmt.Errorf("the model returned an empty reply")
	}
	return text, nil
}

func (c *openAIClient) Ping(ctx context.Context) error {
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/models", nil, &out); err != nil {
		return err
	}
	if len(out.Data) == 0 {
		return nil // server is up but doesn't list models; nothing more to check
	}
	for _, m := range out.Data {
		// Ollama lists "llama3.2:latest" for a model configured as "llama3.2".
		if m.ID == c.model || m.ID == c.model+":latest" {
			return nil
		}
	}
	return fmt.Errorf("model %q is not available on the server", c.model)
}

func (c *openAIClient) do(ctx context.Context, method, path string, body []byte, out any) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("cannot reach %s: %w", c.baseURL, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s: %s", resp.Status, errorMessage(data))
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("unexpected response from %s: %w", c.baseURL, err)
	}
	return nil
}

// errorMessage pulls the message out of an OpenAI-style error body, which is
// either {"error": {"message": "..."}} or {"error": "..."}.
func errorMessage(data []byte) string {
	var obj struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &obj) == nil && len(obj.Error) > 0 {
		var nested struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(obj.Error, &nested) == nil && nested.Message != "" {
			return nested.Message
		}
		var plain string
		if json.Unmarshal(obj.Error, &plain) == nil && plain != "" {
			return plain
		}
	}
	msg := strings.TrimSpace(string(data))
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return msg
}
