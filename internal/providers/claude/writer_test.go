package claude

import (
	"context"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/zuhailkhan/threadman/internal/domain"
)

func TestClaudeWriterRoundTrip(t *testing.T) {
	dir := t.TempDir()
	w := &Writer{baseDir: dir}
	p := &Provider{baseDir: dir}

	thread := domain.Thread{
		Provider:      "claude",
		OriginalID:    "original-session",
		WorkspacePath: "/home/user/myproject",
		Messages: []domain.Message{
			{ID: "msg-1", Role: domain.RoleUser, Content: "hello there", Timestamp: time.Now()},
			{ID: "msg-2", Role: domain.RoleAssistant, Content: "hi back", Timestamp: time.Now().Add(time.Second)},
		},
	}

	sessionID, err := w.WriteThread(context.Background(), thread)
	if err != nil {
		t.Fatalf("WriteThread: %v", err)
	}
	if sessionID == "" {
		t.Fatal("expected non-empty sessionID")
	}

	encodedWS := url.PathEscape(thread.WorkspacePath)
	filePath := filepath.Join(dir, encodedWS, sessionID+".jsonl")

	stub := domain.Thread{SourceFilePath: filePath, OriginalID: sessionID}
	got, err := p.GetThreadDetails(context.Background(), stub)
	if err != nil {
		t.Fatalf("GetThreadDetails: %v", err)
	}

	if len(got.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got.Messages))
	}
	if got.Messages[0].Content != "hello there" {
		t.Errorf("msg[0] content: got %q, want %q", got.Messages[0].Content, "hello there")
	}
	if got.Messages[0].Role != domain.RoleUser {
		t.Errorf("msg[0] role: got %q, want user", got.Messages[0].Role)
	}
	if got.Messages[1].Content != "hi back" {
		t.Errorf("msg[1] content: got %q, want %q", got.Messages[1].Content, "hi back")
	}
	if got.Messages[1].Role != domain.RoleAssistant {
		t.Errorf("msg[1] role: got %q, want assistant", got.Messages[1].Role)
	}
}

func TestClaudeWriterOpenCommand(t *testing.T) {
	dir := t.TempDir()
	w := &Writer{baseDir: dir}

	cmd := w.OpenCommand("abc-123", "/home/user/proj")
	if cmd.Dir != "/home/user/proj" {
		t.Errorf("Dir: got %q, want %q", cmd.Dir, "/home/user/proj")
	}
	if len(cmd.Args) < 3 {
		t.Fatalf("expected at least 3 args, got %v", cmd.Args)
	}
	if cmd.Args[1] != "--resume" {
		t.Errorf("Args[1]: got %q, want --resume", cmd.Args[1])
	}
	if cmd.Args[2] != "abc-123" {
		t.Errorf("Args[2]: got %q, want abc-123", cmd.Args[2])
	}
}
