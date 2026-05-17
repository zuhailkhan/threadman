package opencode

import (
	"context"
	"database/sql"
	"path/filepath"
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
        CREATE TABLE session (
            id TEXT PRIMARY KEY,
            title TEXT NOT NULL,
            directory TEXT NOT NULL,
            time_created INTEGER NOT NULL,
            time_updated INTEGER NOT NULL,
            parent_id TEXT
        );
        CREATE TABLE message (
            id TEXT PRIMARY KEY,
            session_id TEXT NOT NULL,
            data TEXT NOT NULL,
            time_created INTEGER NOT NULL
        );
        CREATE TABLE part (
            id TEXT PRIMARY KEY,
            message_id TEXT NOT NULL,
            session_id TEXT NOT NULL,
            data TEXT NOT NULL,
            time_created INTEGER NOT NULL
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
	cmd := w.OpenCommand("any-id", "/home/user/proj")
	if cmd.Dir != "/home/user/proj" {
		t.Errorf("Dir: got %q, want %q", cmd.Dir, "/home/user/proj")
	}
	if cmd.Args[0] != "opencode" {
		t.Errorf("Args[0]: got %q, want opencode", cmd.Args[0])
	}
}
