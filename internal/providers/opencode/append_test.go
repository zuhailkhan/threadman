package opencode

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zuhailkhan/threadman/internal/domain"
)

func TestAppendMessagesV1(t *testing.T) {
	dbPath, db := setupTestDB(t)
	defer db.Close()

	w := &Writer{dbPath: dbPath}
	sessionID, err := w.WriteThread(context.Background(), domain.Thread{
		Title:         "Existing",
		WorkspacePath: "/ws",
		Messages: []domain.Message{
			{Role: domain.RoleUser, Content: "first", Timestamp: time.UnixMilli(10_000)},
			{Role: domain.RoleAssistant, Content: "second", Timestamp: time.UnixMilli(10_000)},
		},
	})
	if err != nil {
		t.Fatalf("WriteThread: %v", err)
	}

	// Dated before the existing messages, so it must still sort after them.
	err = w.AppendMessages(context.Background(), domain.Thread{OriginalID: sessionID, WorkspacePath: "/ws", SourceFilePath: dbPath},
		[]domain.Message{{Role: domain.RoleUser, Content: "appended", Timestamp: time.UnixMilli(5_000)}})
	if err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}

	p := &Provider{dbPath: dbPath}
	full, err := p.GetThreadDetails(context.Background(), domain.Thread{OriginalID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range full.Messages {
		got = append(got, m.Content)
	}
	if want := []string{"first", "second", "appended"}; !slices.Equal(got, want) {
		t.Errorf("messages %v, want %v", got, want)
	}

	if err := w.AppendMessages(context.Background(), domain.Thread{OriginalID: "ses_missing", SourceFilePath: dbPath},
		[]domain.Message{{Role: domain.RoleUser, Content: "x"}}); err == nil {
		t.Error("appending to a missing session should fail")
	}
}

// fakeCLI stands in for the opencode CLI's session commands, keeping sessions
// as import documents. Like the real CLI, import exits 0 when the session
// already exists, and export can print a log line before the JSON.
type fakeCLI struct {
	t        *testing.T
	sessions map[string][]byte
	calls    []string
	// failImportWith makes an import of a document with this many messages
	// fail.
	failImportWith int
	// dropOnImport makes import silently store one message fewer.
	dropOnImport bool
}

func (f *fakeCLI) run(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args[1])
	switch args[1] {
	case "export":
		doc, ok := f.sessions[args[2]]
		if !ok {
			return []byte("Session not found"), errors.New("exit status 1")
		}
		return append([]byte("INFO  service=db migrated\n"), doc...), nil
	case "delete":
		if _, ok := f.sessions[args[2]]; !ok {
			return []byte("Session not found: " + args[2]), errors.New("exit status 1")
		}
		delete(f.sessions, args[2])
		return []byte("Session deleted"), nil
	case "import":
		data, err := os.ReadFile(args[len(args)-1])
		if err != nil {
			f.t.Fatal(err)
		}
		var doc struct {
			Info struct {
				ID string `json:"id"`
			} `json:"info"`
			Messages []json.RawMessage `json:"messages"`
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			f.t.Fatal(err)
		}
		if _, ok := f.sessions[doc.Info.ID]; ok {
			return []byte("Session already exists"), nil
		}
		if f.failImportWith != 0 && len(doc.Messages) == f.failImportWith {
			return []byte("Error: Failed to import session: Bad Request"), errors.New("exit status 1")
		}
		if f.dropOnImport && len(doc.Messages) > 0 {
			var full map[string]any
			if err := json.Unmarshal(data, &full); err != nil {
				f.t.Fatal(err)
			}
			full["messages"] = full["messages"].([]any)[1:]
			data, _ = json.Marshal(full)
		}
		f.sessions[doc.Info.ID] = data
		return []byte("Imported session: " + doc.Info.ID), nil
	}
	f.t.Fatalf("unexpected opencode call %v", args)
	return nil, nil
}

func (f *fakeCLI) messages(id string) []string {
	var doc struct {
		Messages []struct {
			Text    string           `json:"text"`
			Content []contentBlockV2 `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(f.sessions[id], &doc); err != nil {
		f.t.Fatal(err)
	}
	var out []string
	for _, m := range doc.Messages {
		if m.Text != "" {
			out = append(out, m.Text)
		} else if len(m.Content) > 0 {
			out = append(out, m.Content[0].Text)
		}
	}
	return out
}

const existingSession = `{"info":{"id":"ses_1","projectID":"p","title":"Existing","location":{"directory":"/real/ws"},"time":{"created":1,"updated":1},"cost":0,"tokens":{"input":0,"output":0,"reasoning":0,"cache":{"read":0,"write":0}},"model":{"id":"m"},"agent":"build"},
"messages":[{"id":"msg_1","type":"user","time":{"created":9000},"text":"first","files":[],"agents":[]},
{"id":"msg_2","type":"assistant","time":{"created":9001,"completed":9001},"agent":"build","content":[{"type":"text","text":"second"},{"type":"tool","name":"bash"}],"finish":"stop","model":{"id":"m","providerID":"p","variant":"default"},"cost":0.5,"tokens":{"input":10,"output":5,"reasoning":0,"cache":{"read":0,"write":0}}}]}`

func appendV2Setup(t *testing.T) (*Writer, *fakeCLI, *sql.DB, domain.Thread) {
	t.Helper()
	dbPath, db := setupV2DB(t)
	insertSessionV2(t, db, "ses_1", nil, "Existing")
	cli := &fakeCLI{t: t, sessions: map[string][]byte{"ses_1": []byte(existingSession)}}
	w := &Writer{dbPath: dbPath, backupDir: filepath.Join(t.TempDir(), "backups"), run: cli.run}
	return w, cli, db, domain.Thread{OriginalID: "ses_1", WorkspacePath: "/ws", SourceFilePath: dbPath}
}

func TestAppendMessagesV2(t *testing.T) {
	w, cli, _, target := appendV2Setup(t)

	err := w.AppendMessages(context.Background(), target, []domain.Message{
		{Role: domain.RoleUser, Content: "[threadman handoff h1]\nblock", Timestamp: time.UnixMilli(100)},
	})
	if err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}

	if got, want := cli.messages("ses_1"), []string{"first", "second", "[threadman handoff h1]\nblock"}; !slices.Equal(got, want) {
		t.Errorf("messages %q, want %q", got, want)
	}
	var doc struct {
		Info     map[string]json.RawMessage `json:"info"`
		Messages []struct {
			ID   string        `json:"id"`
			Time importMsgTime `json:"time"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(cli.sessions["ses_1"], &doc); err != nil {
		t.Fatal(err)
	}
	// Everything already in the session survives the re-import as it was.
	if !strings.Contains(string(cli.sessions["ses_1"]), `{"type":"tool","name":"bash"}`) || string(doc.Info["agent"]) != `"build"` {
		t.Errorf("existing session data was not kept:\n%s", cli.sessions["ses_1"])
	}
	if last := doc.Messages[2]; !strings.HasPrefix(last.ID, "msg_") || last.Time.Created <= 9001 {
		t.Errorf("appended message %+v must sort after the existing ones", last)
	}

	backups, _ := filepath.Glob(filepath.Join(w.backupDir, "ses_1-*.json"))
	if len(backups) != 1 {
		t.Fatalf("backups: %v", backups)
	}
	if data, _ := os.ReadFile(backups[0]); !json.Valid(data) || !strings.Contains(string(data), `"text":"first"`) || strings.Contains(string(data), "handoff") {
		t.Errorf("backup is not the original session:\n%s", data)
	}
	if want := []string{"export", "delete", "import", "export"}; !slices.Equal(cli.calls, want) {
		t.Errorf("CLI calls %v, want %v", cli.calls, want)
	}
}

func TestAppendMessagesV2RestoresOnImportFailure(t *testing.T) {
	w, cli, _, target := appendV2Setup(t)
	cli.failImportWith = 3

	err := w.AppendMessages(context.Background(), target, []domain.Message{{Role: domain.RoleUser, Content: "block"}})

	if err == nil || !strings.Contains(err.Error(), "restored unchanged") || !strings.Contains(err.Error(), "Bad Request") {
		t.Errorf("err = %v", err)
	}
	if got := cli.messages("ses_1"); !slices.Equal(got, []string{"first", "second"}) {
		t.Errorf("session after failed append: %q", got)
	}
}

func TestAppendMessagesV2RestoresWhenImportLosesMessages(t *testing.T) {
	w, cli, _, target := appendV2Setup(t)
	cli.dropOnImport = true

	err := w.AppendMessages(context.Background(), target, []domain.Message{{Role: domain.RoleUser, Content: "block"}})

	if err == nil || !strings.Contains(err.Error(), "restored") {
		t.Fatalf("err = %v", err)
	}
	// The fake loses a message on every import, the restore included, but it
	// is the original document that was imported back.
	if got := cli.messages("ses_1"); slices.Contains(got, "block") {
		t.Errorf("the appended message survived a failed append: %q", got)
	}
}

func TestAppendMessagesV2RefusesSessionWithSubagents(t *testing.T) {
	w, cli, db, target := appendV2Setup(t)
	insertSessionV2(t, db, "ses_child", "ses_1", "subagent")

	err := w.AppendMessages(context.Background(), target, []domain.Message{{Role: domain.RoleUser, Content: "block"}})

	if err == nil || !strings.Contains(err.Error(), "subagent") {
		t.Errorf("err = %v", err)
	}
	if len(cli.calls) != 0 {
		t.Errorf("the CLI was called: %v", cli.calls)
	}
}

func TestImportFileTreatsExistingSessionAsFailure(t *testing.T) {
	w, _, _, _ := appendV2Setup(t)
	err := w.importFile(context.Background(), "/ws", []byte(existingSession))
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("err = %v", err)
	}
}
