package handoff

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zuhailkhan/threadman/internal/domain"
)

type fakeLLM struct {
	system, user string
	calls        int
	reply        string
	err          error
}

func (f *fakeLLM) Complete(ctx context.Context, system, user string) (string, error) {
	f.calls++
	f.system, f.user = system, user
	return f.reply, f.err
}

func (f *fakeLLM) Ping(ctx context.Context) error { return nil }

// sampleThread builds a thread of n alternating messages, "user 1",
// "assistant 2", "user 3", and so on.
func sampleThread(n int) domain.Thread {
	t := domain.Thread{
		ID:            "claude-abc",
		Provider:      "claude",
		OriginalID:    "abc",
		Title:         "Fix the parser",
		WorkspacePath: "/work/proj",
	}
	start := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	for i := 1; i <= n; i++ {
		role := domain.RoleUser
		if i%2 == 0 {
			role = domain.RoleAssistant
		}
		t.Messages = append(t.Messages, domain.Message{
			Role:      role,
			Content:   fmt.Sprintf("%s %d", role, i),
			Timestamp: start.Add(time.Duration(i) * time.Minute),
		})
	}
	return t
}

func TestSummarizeKeepsRecentTurns(t *testing.T) {
	src := sampleThread(10)
	llm := &fakeLLM{reply: "  ## Goal\nfix tabs  "}

	out, err := Summarize(context.Background(), llm, src, ModeDetailed)
	if err != nil {
		t.Fatal(err)
	}

	if len(src.Messages) != 10 || src.Messages[0].Content != "user 1" {
		t.Fatal("source thread was modified")
	}
	if out.ID != src.ID || out.WorkspacePath != src.WorkspacePath || out.Title != src.Title {
		t.Errorf("thread metadata not carried over: %+v", out)
	}

	// Detailed keeps the last 6: a brief, an acknowledgement, then turns 5-10.
	if len(out.Messages) != 8 {
		t.Fatalf("got %d messages, want 8: %+v", len(out.Messages), out.Messages)
	}
	if out.Messages[0].Role != domain.RoleUser || out.Messages[1].Role != domain.RoleAssistant {
		t.Fatalf("want a user brief then an assistant acknowledgement, got %+v", out.Messages[:2])
	}
	for i, m := range out.Messages[2:] {
		if m != src.Messages[4+i] {
			t.Errorf("kept message %d = %+v, want %+v unchanged", i, m, src.Messages[4+i])
		}
	}
	for i := 1; i < len(out.Messages); i++ {
		if out.Messages[i].Role == out.Messages[i-1].Role {
			t.Errorf("messages %d and %d have the same role", i-1, i)
		}
		if out.Messages[i].Timestamp.Before(out.Messages[i-1].Timestamp) {
			t.Errorf("message %d is dated before message %d", i, i-1)
		}
	}

	brief := out.Messages[0].Content
	for _, want := range []string{"## Goal\nfix tabs", "handed off from claude", "detailed summary", "first 4 of its 10 messages", "last 6 messages follow", "threadman show claude-abc"} {
		if !strings.Contains(brief, want) {
			t.Errorf("brief missing %q:\n%s", want, brief)
		}
	}

	// The model is sent only the older turns.
	for _, want := range []string{"Working directory: /work/proj", "[turn 1 · user · 2026-11-16 10:01]", "assistant 4"} {
		if !strings.Contains(llm.user, want) {
			t.Errorf("transcript missing %q:\n%s", want, llm.user)
		}
	}
	if strings.Contains(llm.user, "user 5") || strings.Contains(llm.user, "assistant 10") {
		t.Errorf("turns 5-10 are handed over as they are and must not be sent to the model:\n%s", llm.user)
	}
	if llm.system != detailedPrompt {
		t.Error("detailed mode did not use the detailed prompt")
	}
}

func TestSummarizeLightKeepsFewer(t *testing.T) {
	src := sampleThread(10)
	llm := &fakeLLM{reply: "short"}

	out, err := Summarize(context.Background(), llm, src, ModeLight)
	if err != nil {
		t.Fatal(err)
	}
	if llm.system != lightPrompt {
		t.Error("light mode did not use the light prompt")
	}
	// Light keeps the last 2: brief, acknowledgement, turns 9-10.
	if len(out.Messages) != 4 || out.Messages[2].Content != "user 9" || out.Messages[3].Content != "assistant 10" {
		t.Fatalf("got %+v", out.Messages)
	}
}

// The kept tail must start on a user message, so it is widened when the
// plain cut would land on an assistant message.
func TestSummarizeTailStartsOnUser(t *testing.T) {
	src := sampleThread(9) // ends on a user message; last 6 would start on assistant 4
	out, err := Summarize(context.Background(), &fakeLLM{reply: "s"}, src, ModeDetailed)
	if err != nil {
		t.Fatal(err)
	}
	if out.Messages[2].Content != "user 3" || len(out.Messages) != 2+7 {
		t.Fatalf("tail should start at user 3 and hold 7 messages, got %+v", out.Messages[2:])
	}
}

// A thread with nothing older than the kept tail is handed over whole.
func TestSummarizeShortThread(t *testing.T) {
	src := sampleThread(6)
	llm := &fakeLLM{reply: "unused"}

	out, err := Summarize(context.Background(), llm, src, ModeDetailed)
	if err != nil {
		t.Fatal(err)
	}
	if llm.calls != 0 {
		t.Error("the LLM was called for a thread with nothing to summarize")
	}
	if len(out.Messages) != 6 || out.Messages[0] != src.Messages[0] {
		t.Errorf("short thread should pass through unchanged, got %+v", out.Messages)
	}
}

func TestSummarizeError(t *testing.T) {
	boom := errors.New("boom")
	if _, err := Summarize(context.Background(), &fakeLLM{err: boom}, sampleThread(10), ModeLight); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
}

func TestSummarizerWithoutConfig(t *testing.T) {
	s := NewSummarizer(filepath.Join(t.TempDir(), "config.json"))

	st := s.Check(context.Background())
	if st.Configured || st.Ready() || st.Err != nil {
		t.Errorf("Check = %+v, want not configured and no error", st)
	}

	src := sampleThread(10)
	full, err := s.Prepare(context.Background(), src, ModeFull)
	if err != nil || len(full.Messages) != 10 || full.Messages[0] != src.Messages[0] {
		t.Errorf("full transcript should pass through without an LLM: %v", err)
	}
	if _, err := s.Prepare(context.Background(), src, ModeDetailed); err == nil {
		t.Error("summary without an LLM should fail")
	}
}
