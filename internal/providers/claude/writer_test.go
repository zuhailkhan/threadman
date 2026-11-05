package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
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

	encodedWS := encodeProjectDir(thread.WorkspacePath)
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

func TestEncodeProjectDir(t *testing.T) {
	cases := map[string]string{
		// These pairs match folders created by Claude Code itself.
		`C:\Users\zuhai\workspace\threadman`:  "C--Users-zuhai-workspace-threadman",
		`C:\Users\zuhai\workspace\hugo_folio`: "C--Users-zuhai-workspace-hugo-folio",
		// OpenCode records Windows paths with forward slashes.
		"C:/Users/zuhai/workspace/threadman": "C--Users-zuhai-workspace-threadman",
		"/home/user/my.project":              "-home-user-my-project",
	}
	for in, want := range cases {
		if got := encodeProjectDir(in); got != want {
			t.Errorf("encodeProjectDir(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClaudeWriterBuildsParentChain(t *testing.T) {
	dir := t.TempDir()
	w := &Writer{baseDir: dir}

	thread := domain.Thread{
		WorkspacePath: "/home/user/myproject",
		Messages: []domain.Message{
			{Role: domain.RoleUser, Content: "one"},
			{Role: domain.RoleAssistant, Content: "two"},
			{Role: domain.RoleUser, Content: "three"},
		},
	}

	sessionID, err := w.WriteThread(context.Background(), thread)
	if err != nil {
		t.Fatalf("WriteThread: %v", err)
	}

	f, err := os.Open(filepath.Join(dir, encodeProjectDir(thread.WorkspacePath), sessionID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	type line struct {
		Type       string  `json:"type"`
		UUID       string  `json:"uuid"`
		ParentUUID *string `json:"parentUuid"`
		SessionID  string  `json:"sessionId"`
		Message    struct {
			Role string `json:"role"`
		} `json:"message"`
	}

	var entries []line
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var l line
		if err := json.Unmarshal(scanner.Bytes(), &l); err != nil {
			t.Fatal(err)
		}
		if l.SessionID != sessionID {
			t.Errorf("%s entry sessionId: got %q, want %q", l.Type, l.SessionID, sessionID)
		}
		if l.Type == "user" || l.Type == "assistant" {
			entries = append(entries, l)
		}
	}

	if len(entries) != 3 {
		t.Fatalf("expected 3 conversation entries, got %d", len(entries))
	}
	if entries[0].ParentUUID != nil {
		t.Errorf("first entry parentUuid: got %q, want null", *entries[0].ParentUUID)
	}
	for i := 1; i < len(entries); i++ {
		if entries[i].ParentUUID == nil || *entries[i].ParentUUID != entries[i-1].UUID {
			t.Errorf("entry %d does not point at entry %d", i, i-1)
		}
	}
	for _, e := range entries {
		if e.Message.Role != e.Type {
			t.Errorf("message.role: got %q, want %q", e.Message.Role, e.Type)
		}
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

func TestClaudeWriterDeleteThread(t *testing.T) {
	dir := t.TempDir()
	w := &Writer{baseDir: dir}

	thread := domain.Thread{
		Provider:      "claude",
		WorkspacePath: "/home/user/myproject",
		Messages: []domain.Message{
			{ID: "m1", Role: domain.RoleUser, Content: "hello", Timestamp: time.Now()},
		},
	}

	sessionID, err := w.WriteThread(context.Background(), thread)
	if err != nil {
		t.Fatalf("WriteThread: %v", err)
	}

	encodedWS := encodeProjectDir(thread.WorkspacePath)
	filePath := filepath.Join(dir, encodedWS, sessionID+".jsonl")
	thread.SourceFilePath = filePath

	if err := w.DeleteThread(context.Background(), thread); err != nil {
		t.Fatalf("DeleteThread: %v", err)
	}

	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Errorf("expected file to be deleted, stat err: %v", err)
	}
}

func TestClaudeWriterDeleteThreadMissingFile(t *testing.T) {
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
