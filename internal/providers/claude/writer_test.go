package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestClaudeWriterAppendMessagesChainsFromLastEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sess-1.jsonl")
	// A session as current Claude Code writes it: bookkeeping lines without a
	// uuid, a subagent sidechain, and no newline after the last line.
	lines := []string{
		`{"type":"session-owner","sessionId":"sess-1"}`,
		`{"type":"user","uuid":"u1","parentUuid":null,"isSidechain":false,"sessionId":"sess-1","cwd":"/work","timestamp":"2026-01-01T00:00:01Z","message":{"role":"user","content":"hello"}}`,
		`{"type":"assistant","uuid":"a1","parentUuid":"u1","isSidechain":false,"sessionId":"sess-1","cwd":"/work","timestamp":"2026-01-01T00:00:02Z","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"hi"}]}}`,
		`{"type":"assistant","uuid":"side","parentUuid":"a1","isSidechain":true,"sessionId":"sess-1","timestamp":"2026-01-01T00:00:03Z","message":{"id":"m2","role":"assistant","content":[{"type":"text","text":"subagent"}]}}`,
		`{"type":"last-prompt","leafUuid":"a1","sessionId":"sess-1"}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}

	w := &Writer{baseDir: dir}
	target := domain.Thread{OriginalID: "sess-1", WorkspacePath: "/work", SourceFilePath: path}
	err := w.AppendMessages(context.Background(), target, []domain.Message{
		{Role: domain.RoleUser, Content: "[threadman handoff h1]\nblock", Timestamp: time.Unix(5000, 0)},
	})
	if err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(all) != len(lines)+1 {
		t.Fatalf("got %d lines, want %d:\n%s", len(all), len(lines)+1, data)
	}
	if all[len(lines)-1] != lines[len(lines)-1] {
		t.Errorf("existing lines were changed")
	}
	var e struct {
		Type       string `json:"type"`
		ParentUUID string `json:"parentUuid"`
		SessionID  string `json:"sessionId"`
		Message    struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(all[len(all)-1]), &e); err != nil {
		t.Fatal(err)
	}
	if e.Type != "user" || e.ParentUUID != "a1" || e.SessionID != "sess-1" || e.Message.Content != "[threadman handoff h1]\nblock" {
		t.Errorf("appended entry = %+v; want a user entry chained from a1, skipping the sidechain", e)
	}

	// The provider reads the appended message as the last of the session.
	p := &Provider{baseDir: dir}
	full, err := p.GetThreadDetails(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(full.Messages); n == 0 || full.Messages[n-1].Content != "[threadman handoff h1]\nblock" {
		t.Errorf("messages after append: %+v", full.Messages)
	}
}

func TestClaudeWriterAppendMessagesNeedsAnEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"session-owner"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := (&Writer{}).AppendMessages(context.Background(), domain.Thread{SourceFilePath: path}, []domain.Message{{Role: domain.RoleUser, Content: "x"}})
	if err == nil {
		t.Error("appending to a session with no conversation entries should fail")
	}
}
