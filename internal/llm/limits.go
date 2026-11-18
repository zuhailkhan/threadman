package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Limits is what the configured model can take in one request.
type Limits struct {
	// Context is how many tokens the prompt and the reply can hold together;
	// 0 when it isn't known.
	Context int
	// Local is set for a model running on this machine. Requests sent to it at
	// the same time can share one context, so they should be sent one by one.
	Local bool
}

// Limiter is a Client that can say what its model can take.
type Limiter interface {
	Limits(ctx context.Context) Limits
}

// Context windows assumed for hosted providers that don't report one. They
// are the smallest of the provider's current models, so a summary is split
// into parts sooner than it needs to be rather than later.
const (
	claudeContext   = 200_000
	openAIContext   = 128_000
	deepSeekContext = 64_000
)

func (c *openAIClient) Limits(ctx context.Context) Limits {
	local := c.provider == ProviderLMStudio || c.provider == ProviderOllama
	if c.context > 0 {
		return Limits{Context: c.context, Local: local}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	switch c.provider {
	case ProviderLMStudio:
		return Limits{Context: c.lmStudioContext(ctx), Local: true}
	case ProviderOllama:
		return Limits{Context: c.ollamaContext(ctx), Local: true}
	case ProviderOpenAI:
		return Limits{Context: openAIContext}
	case ProviderDeepSeek:
		return Limits{Context: deepSeekContext}
	}
	return Limits{}
}

func (c *anthropicClient) Limits(context.Context) Limits {
	if c.context > 0 {
		return Limits{Context: c.context}
	}
	return Limits{Context: claudeContext}
}

// serverRoot is the server's address without the OpenAI-compatible /v1 path,
// where LM Studio and Ollama serve their own APIs.
func (c *openAIClient) serverRoot() string {
	return strings.TrimSuffix(strings.TrimRight(c.baseURL, "/"), "/v1")
}

// lmStudioContext is the context length the model is loaded with, or 0 when
// it isn't loaded (LM Studio then loads it on the first request, with its
// default length).
func (c *openAIClient) lmStudioContext(ctx context.Context) int {
	var v1 struct {
		Models []struct {
			Key       string `json:"key"`
			Instances []struct {
				ID     string `json:"id"`
				Config struct {
					ContextLength int `json:"context_length"`
				} `json:"config"`
			} `json:"loaded_instances"`
		} `json:"models"`
	}
	if c.getJSON(ctx, c.serverRoot()+"/api/v1/models", &v1) == nil {
		for _, m := range v1.Models {
			for _, in := range m.Instances {
				if (in.ID == c.model || m.Key == c.model) && in.Config.ContextLength > 0 {
					return in.Config.ContextLength
				}
			}
		}
	}
	var v0 struct {
		Data []struct {
			ID     string `json:"id"`
			State  string `json:"state"`
			Loaded int    `json:"loaded_context_length"`
		} `json:"data"`
	}
	if c.getJSON(ctx, c.serverRoot()+"/api/v0/models", &v0) == nil {
		for _, m := range v0.Data {
			if m.ID == c.model && m.State == "loaded" && m.Loaded > 0 {
				return m.Loaded
			}
		}
	}
	return 0
}

// ollamaContext is the context length of the running model, or 0 when it
// isn't running.
func (c *openAIClient) ollamaContext(ctx context.Context) int {
	var ps struct {
		Models []struct {
			Name          string `json:"name"`
			Model         string `json:"model"`
			ContextLength int    `json:"context_length"`
		} `json:"models"`
	}
	if c.getJSON(ctx, c.serverRoot()+"/api/ps", &ps) != nil {
		return 0
	}
	for _, m := range ps.Models {
		for _, name := range []string{m.Name, m.Model} {
			if (name == c.model || name == c.model+":latest") && m.ContextLength > 0 {
				return m.ContextLength
			}
		}
	}
	return 0
}

func (c *openAIClient) getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// ContextError is a request the model couldn't take because the prompt, or
// the prompt and the reply together, didn't fit in its context.
type ContextError struct {
	// Limit is the context size the server reported, in tokens; 0 when it
	// didn't say.
	Limit int
	Err   error
}

func (e *ContextError) Error() string { return e.Err.Error() }
func (e *ContextError) Unwrap() error { return e.Err }

// IsContextError reports whether err is a request too long for the model.
func IsContextError(err error) bool {
	var ce *ContextError
	return errors.As(err, &ce)
}

// What servers say when a request is too long for the context: LM Studio and
// llama.cpp, OpenAI and compatibles, Ollama, and Anthropic.
var contextErrorText = regexp.MustCompile(`(?i)exceed_context|exceeds? the available context|context size|context length|context window|context_length_exceeded|prompt is too long|too many tokens`)

// The context size, where the server says it.
var contextLimitPatterns = []*regexp.Regexp{
	regexp.MustCompile(`"n_ctx"\s*:\s*(\d+)`),
	regexp.MustCompile(`(?i)context size \((\d+) tokens\)`),
	regexp.MustCompile(`(?i)maximum context length is (\d+)`),
	regexp.MustCompile(`(?i)>\s*(\d+)\s+maximum`),
}

// asContextError wraps err as a ContextError when text says the request was
// too long; otherwise it returns err unchanged.
func asContextError(err error, text string) error {
	if err == nil || !contextErrorText.MatchString(text) {
		return err
	}
	ce := &ContextError{Err: err}
	for _, re := range contextLimitPatterns {
		if m := re.FindStringSubmatch(text); m != nil {
			if n, convErr := strconv.Atoi(m[1]); convErr == nil && n > 0 {
				ce.Limit = n
				break
			}
		}
	}
	return ce
}
