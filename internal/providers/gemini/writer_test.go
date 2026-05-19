package gemini

import (
	"context"
	"os"
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

	chatsDir := filepath.Join(dir, filepath.Base(thread.WorkspacePath), "chats")
	entries, err := os.ReadDir(chatsDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected 1 file in chats dir, got %v (err: %v)", len(entries), err)
	}
	filePath := filepath.Join(chatsDir, entries[0].Name())

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

	cmd := w.OpenCommand("abc-uuid", "/home/user/proj")
	if cmd.Dir != "/home/user/proj" {
		t.Errorf("Dir: got %q, want %q", cmd.Dir, "/home/user/proj")
	}
	if len(cmd.Args) < 3 {
		t.Fatalf("expected at least 3 args, got %v", cmd.Args)
	}
	if cmd.Args[1] != "--resume" {
		t.Errorf("Args[1]: got %q, want --resume", cmd.Args[1])
	}
	if cmd.Args[2] != "abc-uuid" {
		t.Errorf("Args[2]: got %q, want abc-uuid", cmd.Args[2])
	}
}

func TestGeminiWriterDeleteThread(t *testing.T) {
	dir := t.TempDir()
	w := &Writer{baseDir: dir}

	thread := domain.Thread{
		Provider:      "gemini",
		WorkspacePath: "/home/user/myproject",
		Messages: []domain.Message{
			{ID: "m1", Role: domain.RoleUser, Content: "hello", Timestamp: time.Now()},
		},
	}

	sessionID, err := w.WriteThread(context.Background(), thread)
	if err != nil {
		t.Fatalf("WriteThread: %v", err)
	}
	_ = sessionID

	chatsDir := filepath.Join(dir, filepath.Base(thread.WorkspacePath), "chats")
	entries, err := os.ReadDir(chatsDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected 1 file in chats dir")
	}
	filePath := filepath.Join(chatsDir, entries[0].Name())
	thread.SourceFilePath = filePath

	if err := w.DeleteThread(context.Background(), thread); err != nil {
		t.Fatalf("DeleteThread: %v", err)
	}

	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Errorf("expected file to be deleted, stat err: %v", err)
	}
}

func TestGeminiWriterDeleteThreadMissingFile(t *testing.T) {
	dir := t.TempDir()
	w := &Writer{baseDir: dir}

	thread := domain.Thread{
		SourceFilePath: filepath.Join(dir, "nonexistent.jsonl"),
	}

	err := w.DeleteThread(context.Background(), thread)
	if err == nil {
		t.Error("expected error deleting nonexistent file, got nil")
	}
}
