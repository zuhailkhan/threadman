package opencode

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/zuhailkhan/threadman/internal/domain"
)

func setupTestDB(t *testing.T) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
        CREATE TABLE project (
            id TEXT PRIMARY KEY,
            worktree TEXT NOT NULL,
            sandboxes TEXT NOT NULL DEFAULT '[]',
            time_created INTEGER NOT NULL DEFAULT 0,
            time_updated INTEGER NOT NULL DEFAULT 0
        );
        INSERT INTO project (id, worktree) VALUES ('global', '/');
        CREATE TABLE session (
            id TEXT PRIMARY KEY,
            project_id TEXT NOT NULL,
            slug TEXT NOT NULL,
            version TEXT NOT NULL DEFAULT '',
            title TEXT NOT NULL,
            directory TEXT NOT NULL,
            time_created INTEGER NOT NULL,
            time_updated INTEGER NOT NULL,
            parent_id TEXT,
            FOREIGN KEY (project_id) REFERENCES project(id) ON DELETE CASCADE
        );
        CREATE TABLE message (
            id TEXT PRIMARY KEY,
            session_id TEXT NOT NULL,
            data TEXT NOT NULL,
            time_created INTEGER NOT NULL,
            time_updated INTEGER NOT NULL,
            FOREIGN KEY (session_id) REFERENCES session(id) ON DELETE CASCADE
        );
        CREATE TABLE part (
            id TEXT PRIMARY KEY,
            message_id TEXT NOT NULL,
            session_id TEXT NOT NULL,
            data TEXT NOT NULL,
            time_created INTEGER NOT NULL,
            time_updated INTEGER NOT NULL,
            FOREIGN KEY (message_id) REFERENCES message(id) ON DELETE CASCADE
        );
    `)
	if err != nil {
		t.Fatal(err)
	}
	return path, db
}

func TestOpenCodeWriterRoundTrip(t *testing.T) {
	dbPath, db := setupTestDB(t)
	defer db.Close()

	w := &Writer{dbPath: dbPath}
	p := &Provider{dbPath: dbPath}

	thread := domain.Thread{
		Provider:      "opencode",
		Title:         "Test session",
		WorkspacePath: "/home/user/proj",
		Messages: []domain.Message{
			{ID: "m1", Role: domain.RoleUser, Content: "first message", Timestamp: time.Now()},
			{ID: "m2", Role: domain.RoleAssistant, Content: "first reply", Timestamp: time.Now().Add(time.Second)},
		},
	}

	sessionID, err := w.WriteThread(context.Background(), thread)
	if err != nil {
		t.Fatalf("WriteThread: %v", err)
	}
	if sessionID == "" {
		t.Fatal("expected non-empty sessionID")
	}

	stub := domain.Thread{OriginalID: sessionID, SourceFilePath: dbPath}
	got, err := p.GetThreadDetails(context.Background(), stub)
	if err != nil {
		t.Fatalf("GetThreadDetails: %v", err)
	}

	if len(got.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got.Messages))
	}
	if got.Messages[0].Content != "first message" {
		t.Errorf("msg[0]: got %q", got.Messages[0].Content)
	}
	if got.Messages[0].Role != domain.RoleUser {
		t.Errorf("msg[0] role: got %q", got.Messages[0].Role)
	}
	if got.Messages[1].Content != "first reply" {
		t.Errorf("msg[1]: got %q", got.Messages[1].Content)
	}
	if got.Messages[1].Role != domain.RoleAssistant {
		t.Errorf("msg[1] role: got %q", got.Messages[1].Role)
	}
}

func TestOpenCodeWriterOpenCommand(t *testing.T) {
	w := &Writer{dbPath: "/tmp/test.db"}
	sessionID := "ses_abc123"
	cmd := w.OpenCommand(sessionID, "/home/user/proj")
	if cmd.Dir != "/home/user/proj" {
		t.Errorf("Dir: got %q, want %q", cmd.Dir, "/home/user/proj")
	}
	if len(cmd.Args) < 3 {
		t.Fatalf("expected at least 3 args, got %d: %v", len(cmd.Args), cmd.Args)
	}
	if cmd.Args[1] != "-s" {
		t.Errorf("Args[1]: got %q, want -s", cmd.Args[1])
	}
	if cmd.Args[2] != sessionID {
		t.Errorf("Args[2]: got %q, want %q", cmd.Args[2], sessionID)
	}
}

func TestOpenCodeWriterSessionIDFormat(t *testing.T) {
	dbPath, db := setupTestDB(t)
	defer db.Close()
	w := &Writer{dbPath: dbPath}
	thread := domain.Thread{
		Provider:      "opencode",
		Title:         "format test",
		WorkspacePath: "/ws",
		Messages: []domain.Message{
			{ID: "m1", Role: domain.RoleUser, Content: "hello", Timestamp: time.Now()},
		},
	}
	sessionID, err := w.WriteThread(context.Background(), thread)
	if err != nil {
		t.Fatalf("WriteThread: %v", err)
	}
	if !strings.HasPrefix(sessionID, "ses_") {
		t.Errorf("session ID %q does not start with ses_", sessionID)
	}
}

func TestOpenCodeWriterDeleteThread(t *testing.T) {
	dbPath, db := setupTestDB(t)
	defer db.Close()

	w := &Writer{dbPath: dbPath}

	thread := domain.Thread{
		Provider:       "opencode",
		Title:          "to delete",
		WorkspacePath:  "/home/user/proj",
		SourceFilePath: dbPath,
		Messages: []domain.Message{
			{ID: "m1", Role: domain.RoleUser, Content: "hello", Timestamp: time.Now()},
		},
	}

	sessionID, err := w.WriteThread(context.Background(), thread)
	if err != nil {
		t.Fatalf("WriteThread: %v", err)
	}
	thread.OriginalID = sessionID

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM session WHERE id = ?`, sessionID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected session to exist before delete, got count %d", count)
	}

	if err := w.DeleteThread(context.Background(), thread); err != nil {
		t.Fatalf("DeleteThread: %v", err)
	}

	if err := db.QueryRow(`SELECT COUNT(*) FROM session WHERE id = ?`, sessionID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("expected session to be deleted, count = %d", count)
	}

	if err := db.QueryRow(`SELECT COUNT(*) FROM message WHERE session_id = ?`, sessionID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("expected messages to be deleted (cascade), count = %d", count)
	}
}

func TestOpenCodeWriterDeleteThreadNotFound(t *testing.T) {
	dbPath, db := setupTestDB(t)
	defer db.Close()

	w := &Writer{dbPath: dbPath}
	thread := domain.Thread{
		SourceFilePath: dbPath,
		OriginalID:     "ses_doesnotexist",
	}

	err := w.DeleteThread(context.Background(), thread)
	if err != nil {
		t.Errorf("expected nil error for missing session (delete is idempotent), got: %v", err)
	}
}
