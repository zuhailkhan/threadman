package opencode

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zuhailkhan/threadman/internal/domain"
	"github.com/zuhailkhan/threadman/internal/ports"
)

// setupV2DB mirrors the tables threadman reads from an OpenCode 2.x database.
func setupV2DB(t *testing.T) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec(`
		CREATE TABLE session_v2 (
			id TEXT PRIMARY KEY,
			parent_id TEXT,
			directory TEXT NOT NULL,
			title TEXT,
			model TEXT,
			agent TEXT,
			time_created INTEGER NOT NULL,
			time_updated INTEGER NOT NULL
		);
		CREATE TABLE session_message (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			type TEXT NOT NULL,
			seq INTEGER NOT NULL,
			time_created INTEGER NOT NULL,
			time_updated INTEGER NOT NULL,
			data TEXT NOT NULL
		);
	`)
	if err != nil {
		t.Fatal(err)
	}
	return path, db
}

func insertSessionV2(t *testing.T, db *sql.DB, id string, parentID, title any) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO session_v2 (id, parent_id, directory, title, time_created, time_updated) VALUES (?, ?, '/ws', ?, 1000, 2000)`,
		id, parentID, title,
	)
	if err != nil {
		t.Fatal(err)
	}
}

func insertMessageV2(t *testing.T, db *sql.DB, sessionID, id, msgType string, seq int, data string) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO session_message (id, session_id, type, seq, time_created, time_updated, data) VALUES (?, ?, ?, ?, 1000, 1000, ?)`,
		id, sessionID, msgType, seq, data,
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverThreadsV2SkipsChildSessions(t *testing.T) {
	dbPath, db := setupV2DB(t)
	insertSessionV2(t, db, "ses_top", nil, "Top level")
	insertSessionV2(t, db, "ses_child", "ses_top", "Child")
	insertSessionV2(t, db, "ses_untitled", nil, nil)

	threads, err := (&Provider{dbPath: dbPath}).DiscoverThreads(context.Background())
	if err != nil {
		t.Fatalf("DiscoverThreads: %v", err)
	}

	got := map[string]string{}
	for _, th := range threads {
		got[th.OriginalID] = th.Title
	}
	if len(got) != 2 {
		t.Fatalf("got %v, want ses_top and ses_untitled", got)
	}
	if got["ses_top"] != "Top level" {
		t.Errorf("title: got %q", got["ses_top"])
	}
}

func TestDiscoverThreadsPrefersV2WhenBothSchemasExist(t *testing.T) {
	dbPath, db := setupV2DB(t)
	if _, err := db.Exec(`
		CREATE TABLE session (id TEXT PRIMARY KEY, title TEXT NOT NULL, directory TEXT NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, parent_id TEXT);
		INSERT INTO session VALUES ('ses_migrated', 'Old', '/ws', 1, 2, NULL);
	`); err != nil {
		t.Fatal(err)
	}
	insertSessionV2(t, db, "ses_migrated", nil, "Old")
	insertSessionV2(t, db, "ses_new", nil, "Only in v2")

	threads, err := (&Provider{dbPath: dbPath}).DiscoverThreads(context.Background())
	if err != nil {
		t.Fatalf("DiscoverThreads: %v", err)
	}
	if len(threads) != 2 {
		t.Fatalf("got %d threads, want 2", len(threads))
	}
}

func TestGetThreadDetailsV2(t *testing.T) {
	dbPath, db := setupV2DB(t)
	insertSessionV2(t, db, "ses_1", nil, nil)
	// Inserted out of order: seq decides the conversation order.
	insertMessageV2(t, db, "ses_1", "msg_2", "assistant", 2,
		`{"content":[{"type":"reasoning","text":"hidden"},{"type":"text","text":"hi "},{"type":"tool","name":"bash"},{"type":"text","text":"back"}]}`)
	insertMessageV2(t, db, "ses_1", "msg_1", "user", 1, `{"text":"hello\nthere"}`)
	insertMessageV2(t, db, "ses_1", "msg_3", "idle", 3, `{"outcome":"succeeded"}`)
	insertMessageV2(t, db, "ses_1", "msg_4", "assistant", 4, `{"content":[]}`)

	got, err := (&Provider{dbPath: dbPath}).GetThreadDetails(context.Background(), domain.Thread{OriginalID: "ses_1"})
	if err != nil {
		t.Fatalf("GetThreadDetails: %v", err)
	}

	if len(got.Messages) != 2 {
		t.Fatalf("got %d messages, want 2: %+v", len(got.Messages), got.Messages)
	}
	if got.Messages[0].Role != domain.RoleUser || got.Messages[0].Content != "hello\nthere" {
		t.Errorf("msg[0]: %+v", got.Messages[0])
	}
	if got.Messages[1].Role != domain.RoleAssistant || got.Messages[1].Content != "hi back" {
		t.Errorf("msg[1]: %+v", got.Messages[1])
	}
	if got.Title != "hello there" {
		t.Errorf("fallback title: got %q", got.Title)
	}
}

func TestIngestFromHookV2(t *testing.T) {
	dbPath, db := setupV2DB(t)
	insertSessionV2(t, db, "ses_1", nil, "Hooked")
	insertMessageV2(t, db, "ses_1", "msg_1", "user", 1, `{"text":"hello"}`)

	got, err := (&Provider{dbPath: dbPath}).IngestFromHook(context.Background(), ports.HookPayload{SessionID: "ses_1"})
	if err != nil {
		t.Fatalf("IngestFromHook: %v", err)
	}
	if got.Title != "Hooked" || len(got.Messages) != 1 {
		t.Errorf("got %+v", got)
	}
}

func TestWriteThreadV2ImportsThroughCLI(t *testing.T) {
	dbPath, _ := setupV2DB(t)

	var (
		args     []string
		doc      importDocument
		filePath string
	)
	w := &Writer{dbPath: dbPath, run: func(_ context.Context, a ...string) ([]byte, error) {
		args = a
		filePath = a[len(a)-1]
		data, err := os.ReadFile(filePath)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatal(err)
		}
		return []byte("Imported session"), nil
	}}

	ts := time.UnixMilli(5000)
	sessionID, err := w.WriteThread(context.Background(), domain.Thread{
		Title:         "Handed off",
		WorkspacePath: "/ws",
		Messages: []domain.Message{
			{Role: domain.RoleUser, Content: "one", Timestamp: ts},
			{Role: domain.RoleAssistant, Content: "two", Timestamp: ts},
			{Role: domain.RoleSystem, Content: "three", Timestamp: ts},
		},
	})
	if err != nil {
		t.Fatalf("WriteThread: %v", err)
	}

	if !strings.HasPrefix(sessionID, "ses_") || doc.Info.ID != sessionID {
		t.Errorf("session ID: returned %q, imported %q", sessionID, doc.Info.ID)
	}
	if want := []string{"session", "import", "--directory", "/ws"}; !slices.Equal(args[:4], want) {
		t.Errorf("args: got %v", args)
	}
	if doc.Info.ProjectID == "" {
		t.Error("projectID is required by opencode session import")
	}
	if doc.Info.Title != "Handed off" || doc.Info.Location.Directory != "/ws" {
		t.Errorf("info: %+v", doc.Info)
	}

	if len(doc.Messages) != 3 {
		t.Fatalf("got %d messages, want 3", len(doc.Messages))
	}
	user, assistant, system := doc.Messages[0], doc.Messages[1], doc.Messages[2]
	if user.Type != "user" || user.Text != "one" {
		t.Errorf("user: %+v", user)
	}
	if assistant.Type != "assistant" || assistant.Content[0].Text != "two" {
		t.Errorf("assistant: %+v", assistant)
	}
	if system.Type != "user" || system.Text != "three" {
		t.Errorf("system: %+v", system)
	}
	for i, m := range doc.Messages {
		if !strings.HasPrefix(m.ID, "msg_") {
			t.Errorf("message ID %q needs the msg_ prefix", m.ID)
		}
		if i > 0 && m.Time.Created <= doc.Messages[i-1].Time.Created {
			t.Errorf("message %d does not sort after message %d", i, i-1)
		}
	}

	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Errorf("import file left behind: %v", err)
	}
}

func TestWriteThreadV2UsesLatestSessionModel(t *testing.T) {
	dbPath, db := setupV2DB(t)
	_, err := db.Exec(`
		INSERT INTO session_v2 (id, directory, model, agent, time_created, time_updated) VALUES
			('ses_old', '/ws', '{"id":"old","providerID":"p"}', 'plan', 1, 1),
			('ses_new', '/ws', '{"id":"new","providerID":"p"}', 'build', 1, 2),
			('ses_empty', '/ws', NULL, NULL, 1, 3)
	`)
	if err != nil {
		t.Fatal(err)
	}

	var doc struct {
		Info struct {
			Model struct {
				ID string `json:"id"`
			} `json:"model"`
			Agent string `json:"agent"`
		} `json:"info"`
	}
	w := &Writer{dbPath: dbPath, run: func(_ context.Context, a ...string) ([]byte, error) {
		data, err := os.ReadFile(a[len(a)-1])
		if err != nil {
			t.Fatal(err)
		}
		return nil, json.Unmarshal(data, &doc)
	}}

	if _, err := w.WriteThread(context.Background(), domain.Thread{WorkspacePath: "/ws"}); err != nil {
		t.Fatalf("WriteThread: %v", err)
	}
	if doc.Info.Model.ID != "new" || doc.Info.Agent != "build" {
		t.Errorf("got model %q agent %q, want new and build", doc.Info.Model.ID, doc.Info.Agent)
	}
}

func TestWriteThreadV2ReportsImportFailure(t *testing.T) {
	dbPath, _ := setupV2DB(t)
	w := &Writer{dbPath: dbPath, run: func(context.Context, ...string) ([]byte, error) {
		return []byte("Error: Failed to import session: Bad Request\n    at stack"), errors.New("exit status 1")
	}}

	_, err := w.WriteThread(context.Background(), domain.Thread{WorkspacePath: "/ws"})

	if err == nil || !strings.Contains(err.Error(), "Bad Request") {
		t.Errorf("got %v, want the CLI's error", err)
	}
	if err != nil && strings.Contains(err.Error(), "at stack") {
		t.Errorf("error includes the stack trace: %v", err)
	}
}

func TestDeleteThreadV2(t *testing.T) {
	dbPath, _ := setupV2DB(t)

	cases := []struct {
		name    string
		out     string
		runErr  error
		wantErr bool
	}{
		{name: "deleted", out: "Session ses_1 deleted"},
		{name: "already gone", out: "Session not found: ses_1", runErr: errors.New("exit status 1")},
		{name: "real failure", out: "connection refused", runErr: errors.New("exit status 1"), wantErr: true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var args []string
			w := &Writer{dbPath: dbPath, run: func(_ context.Context, a ...string) ([]byte, error) {
				args = a
				return []byte(c.out), c.runErr
			}}

			err := w.DeleteThread(context.Background(), domain.Thread{SourceFilePath: dbPath, OriginalID: "ses_1"})

			if (err != nil) != c.wantErr {
				t.Errorf("err = %v, wantErr %v", err, c.wantErr)
			}
			if want := []string{"session", "delete", "ses_1"}; !slices.Equal(args, want) {
				t.Errorf("args: got %v, want %v", args, want)
			}
		})
	}
}

// TestRealOpencodeV2 runs the real opencode binary against a throwaway database.
// It is opt-in because it needs opencode installed. --standalone keeps it away
// from the background service, which would write to the user's real database.
func TestRealOpencodeV2(t *testing.T) {
	if os.Getenv("THREADMAN_OPENCODE_IT") == "" {
		t.Skip("set THREADMAN_OPENCODE_IT=1 to run against the real opencode CLI")
	}
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skip("opencode not installed")
	}

	ctx := context.Background()
	data := t.TempDir()
	workspace := t.TempDir()
	dbPath := filepath.Join(data, "opencode.db")

	run := func(ctx context.Context, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "opencode", append([]string{"--standalone"}, args...)...)
		cmd.Env = append(os.Environ(), "OPENCODE_DB="+dbPath, "XDG_DATA_HOME="+data)
		return cmd.CombinedOutput()
	}

	// Any command creates the database and runs OpenCode's migrations.
	if out, err := run(ctx, "session", "list"); err != nil {
		t.Fatalf("create database: %v: %s", err, out)
	}

	w := &Writer{dbPath: dbPath, run: run}
	p := &Provider{dbPath: dbPath}

	now := time.Now()
	sessionID, err := w.WriteThread(ctx, domain.Thread{
		Title:         "Handed off",
		WorkspacePath: workspace,
		Messages: []domain.Message{
			{Role: domain.RoleUser, Content: "first", Timestamp: now},
			{Role: domain.RoleAssistant, Content: "second", Timestamp: now},
			{Role: domain.RoleUser, Content: "third", Timestamp: now},
		},
	})
	if err != nil {
		t.Fatalf("WriteThread: %v", err)
	}

	threads, err := p.DiscoverThreads(ctx)
	if err != nil {
		t.Fatalf("DiscoverThreads: %v", err)
	}
	if len(threads) != 1 || threads[0].OriginalID != sessionID || threads[0].Title != "Handed off" {
		t.Fatalf("discovered %+v, want session %s", threads, sessionID)
	}

	full, err := p.GetThreadDetails(ctx, threads[0])
	if err != nil {
		t.Fatalf("GetThreadDetails: %v", err)
	}
	var contents []string
	for _, m := range full.Messages {
		contents = append(contents, m.Content)
	}
	if want := []string{"first", "second", "third"}; !slices.Equal(contents, want) {
		t.Fatalf("messages: got %v, want %v", contents, want)
	}

	if err := w.DeleteThread(ctx, threads[0]); err != nil {
		t.Fatalf("DeleteThread: %v", err)
	}
	if threads, err = p.DiscoverThreads(ctx); err != nil || len(threads) != 0 {
		t.Fatalf("after delete: %+v, %v", threads, err)
	}
	if err := w.DeleteThread(ctx, domain.Thread{SourceFilePath: dbPath, OriginalID: sessionID}); err != nil {
		t.Errorf("second delete should be a no-op: %v", err)
	}
}
