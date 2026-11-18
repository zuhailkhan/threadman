package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
)

type anthropicClient struct {
	client  anthropic.Client
	model   string
	context int
}

func newAnthropicClient(cfg Config) *anthropicClient {
	var opts []option.RequestOption
	// With no stored key the SDK resolves credentials itself
	// (ANTHROPIC_API_KEY, ANTHROPIC_AUTH_TOKEN, or an `ant auth login` profile).
	if key := cfg.storedKey(); key != "" {
		opts = append(opts, option.WithAPIKey(key))
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	return &anthropicClient{client: anthropic.NewClient(opts...), model: cfg.Model, context: cfg.ContextTokens}
}

func (c *anthropicClient) Complete(ctx context.Context, system, user string) (string, error) {
	// Streamed because a whole transcript goes in and the reply can take a
	// while; a plain request could hit an HTTP timeout.
	stream := c.client.Messages.NewStreaming(ctx, anthropic.MessageNewParams{
		Model:     c.model,
		MaxTokens: 32000,
		System:    []anthropic.TextBlockParam{{Text: system}},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(user)),
		},
	})

	message := anthropic.Message{}
	for stream.Next() {
		if err := message.Accumulate(stream.Current()); err != nil {
			return "", err
		}
	}
	if err := stream.Err(); err != nil {
		return "", describeAnthropicError(err)
	}

	switch message.StopReason {
	case anthropic.StopReasonRefusal:
		return "", fmt.Errorf("the model declined to summarize this conversation")
	case anthropic.StopReasonMaxTokens:
		return "", fmt.Errorf("the model ran out of room before finishing its reply")
	}

	var text strings.Builder
	for _, block := range message.Content {
		if b, ok := block.AsAny().(anthropic.TextBlock); ok {
			text.WriteString(b.Text)
		}
	}
	out := strings.TrimSpace(text.String())
	if out == "" {
		return "", fmt.Errorf("the model returned an empty reply")
	}
	return out, nil
}

func (c *anthropicClient) Ping(ctx context.Context) error {
	if _, err := c.client.Models.Get(ctx, c.model, anthropic.ModelGetParams{}); err != nil {
		return describeAnthropicError(err)
	}
	return nil
}

func describeAnthropicError(err error) error {
	var apiErr *anthropic.Error
	if !errors.As(err, &apiErr) {
		return err
	}
	switch apiErr.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("Claude API rejected the credentials (HTTP %d)", apiErr.StatusCode)
	case http.StatusNotFound:
		return fmt.Errorf("Claude API does not know this model (HTTP 404)")
	case http.StatusTooManyRequests:
		return fmt.Errorf("Claude API rate limit reached; try again shortly")
	default:
		return asContextError(err, err.Error())
	}
}
