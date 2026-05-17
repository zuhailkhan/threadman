package gemini

import (
	"context"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/zuhailkhan/threadman/internal/domain"
)

func TestGeminiWriterRoundTrip(t *testing.T) {
	dir := t.TempDir()
	w := &Writer{baseDir: dir}
	p := &Provider{baseDir: dir}

	thread := domain.Thread{
		Provider:      "gemini",
		WorkspacePath: "/home/user/myproject",
		Messages: []domain.Message{
			{ID: "m1", Role: domain.RoleUser, Content: "question here", Timestamp: time.Now()},
			{ID: "m2", Role: domain.RoleAssistant, Content: "answer here", Timestamp: time.Now().Add(time.Second)},
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
	filePath := filepath.Join(dir, encodedWS, "chats", sessionID+".jsonl")

	stub := domain.Thread{SourceFilePath: filePath}
	got, err := p.GetThreadDetails(context.Background(), stub)
	if err != nil {
		t.Fatalf("GetThreadDetails: %v", err)
	}

	if len(got.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got.Messages))
	}
	if got.Messages[0].Content != "question here" {
		t.Errorf("msg[0]: got %q", got.Messages[0].Content)
	}
	if got.Messages[0].Role != domain.RoleUser {
		t.Errorf("msg[0] role: got %q", got.Messages[0].Role)
	}
	if got.Messages[1].Content != "answer here" {
		t.Errorf("msg[1]: got %q", got.Messages[1].Content)
	}
	if got.Messages[1].Role != domain.RoleAssistant {
		t.Errorf("msg[1] role: got %q", got.Messages[1].Role)
	}
}

func TestGeminiWriterOpenCommand(t *testing.T) {
	dir := t.TempDir()
	w := &Writer{baseDir: dir}

	cmd := w.OpenCommand("any-id", "/home/user/proj")
	if cmd.Dir != "/home/user/proj" {
		t.Errorf("Dir: got %q, want %q", cmd.Dir, "/home/user/proj")
	}
	if cmd.Args[0] != "gemini" {
		t.Errorf("Args[0]: got %q, want gemini", cmd.Args[0])
	}
}
