package antigravity

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zuhailkhan/threadman/internal/domain"
	"github.com/zuhailkhan/threadman/internal/ports"
)

// Protobuf encoders for building test payloads.

func encVarint(v uint64) []byte {
	var b []byte
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

func encInt(num int, v uint64) []byte {
	return append(encVarint(uint64(num)<<3|wireVarint), encVarint(v)...)
}

func encBytes(num int, data []byte) []byte {
	b := append(encVarint(uint64(num)<<3|wireBytes), encVarint(uint64(len(data)))...)
	return append(b, data...)
}

func cat(parts ...[]byte) []byte {
	var b []byte
	for _, p := range parts {
		b = append(b, p...)
	}
	return b
}

func meta(sec uint64) []byte {
	return encBytes(fieldStepMetadata, cat(
		encBytes(fieldMetaCreatedAt, cat(encInt(1, sec), encInt(2, 500))),
		encInt(3, 4),
		encBytes(12, []byte("some-id")),
	))
}

func userStep(sec uint64, text string) []byte {
	return cat(encInt(1, stepUserInput), encInt(4, 3), meta(sec),
		encBytes(fieldUserInput, cat(encBytes(fieldUserText, []byte(text)), encBytes(3, encBytes(1, []byte(text))))))
}

// responseStep builds a model response. An empty text gives a tool-only
// response: thinking and a tool call, but nothing said to the user.
func responseStep(sec uint64, text string) []byte {
	body := cat(encBytes(3, []byte("thinking")), encBytes(7, encBytes(2, []byte("run_command"))))
	if text != "" {
		body = cat(encBytes(fieldResponseText, []byte(text)), body)
	}
	return cat(encInt(1, stepPlannerResponse), encInt(4, 3), meta(sec), encBytes(fieldPlannerResponse, body))
}

type step struct {
	typ     int
	payload []byte
}

func withDB(t *testing.T, path string, stmts ...func(db *sql.DB) error) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, s := range stmts {
		if err := s(db); err != nil {
			t.Fatal(err)
		}
	}
}

func writeConversation(t *testing.T, base, id string, steps ...step) {
	t.Helper()
	dir := filepath.Join(base, "conversations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	withDB(t, filepath.Join(dir, id+".db"), func(db *sql.DB) error {
		if _, err := db.Exec("CREATE TABLE steps (idx integer PRIMARY KEY, step_type integer, step_payload blob)"); err != nil {
			return err
		}
		for i, s := range steps {
			if _, err := db.Exec("INSERT INTO steps VALUES (?, ?, ?)", i, s.typ, s.payload); err != nil {
				return err
			}
		}
		return nil
	})
}

func writeSummary(t *testing.T, base, id, title, uris, parent string) {
	t.Helper()
	withDB(t, filepath.Join(base, "conversation_summaries.db"), func(db *sql.DB) error {
		if _, err := db.Exec("CREATE TABLE IF NOT EXISTS conversation_summaries (conversation_id text PRIMARY KEY, title text, workspace_uris text, parent_conversation_id text, last_modified_time datetime)"); err != nil {
			return err
		}
		_, err := db.Exec("INSERT INTO conversation_summaries VALUES (?, ?, ?, ?, ?)", id, title, uris, parent, "2026-01-01 00:10:00.5+00:00")
		return err
	})
}

func TestDiscoverMissingDirIsUnavailable(t *testing.T) {
	p := &Provider{baseDir: filepath.Join(t.TempDir(), "missing")}
	_, err := p.DiscoverThreads(context.Background())
	if !errors.Is(err, ports.ErrSourceUnavailable) {
		t.Errorf("got %v, want ErrSourceUnavailable", err)
	}
}

func TestReadsConversation(t *testing.T) {
	base := t.TempDir()
	writeConversation(t, base, "conv-1",
		step{stepUserInput, userStep(1000, "fix the bug")},
		step{stepPlannerResponse, responseStep(1001, "")},
		step{132, cat(encInt(1, 132), meta(1002), encBytes(140, []byte("tool output")))},
		step{stepPlannerResponse, responseStep(1003, "Fixed it.")},
	)
	writeSummary(t, base, "conv-1", "Bug Fix", `["file:///home/me/proj"]`, "")
	p := &Provider{baseDir: base}
	ctx := context.Background()

	threads, err := p.DiscoverThreads(ctx)
	if err != nil || len(threads) != 1 {
		t.Fatalf("discover: %v, %d threads", err, len(threads))
	}
	th := threads[0]
	if th.ID != "antigravity-conv-1" || th.OriginalID != "conv-1" || th.Provider != "antigravity" {
		t.Errorf("ids: %+v", th)
	}
	if th.Title != "Bug Fix" {
		t.Errorf("title = %q", th.Title)
	}
	if want := filepath.FromSlash("/home/me/proj"); th.WorkspacePath != want {
		t.Errorf("workspace = %q, want %q", th.WorkspacePath, want)
	}
	if th.CreatedAt.Unix() != 1000 {
		t.Errorf("created = %v", th.CreatedAt)
	}
	// The summary's modified time is later than the last step.
	if got := th.LastSyncedAt.UTC().Format("15:04:05"); got != "00:10:00" {
		t.Errorf("last = %s", got)
	}

	full, err := p.GetThreadDetails(ctx, th)
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Messages) != 2 {
		t.Fatalf("got %d messages, want 2: %+v", len(full.Messages), full.Messages)
	}
	if m := full.Messages[0]; m.Role != domain.RoleUser || m.Content != "fix the bug" || m.Timestamp.Unix() != 1000 {
		t.Errorf("user message: %+v", m)
	}
	if m := full.Messages[1]; m.Role != domain.RoleAssistant || m.Content != "Fixed it." || m.ID != "step-3" {
		t.Errorf("assistant message: %+v", m)
	}
	if full.Title != "Bug Fix" {
		t.Errorf("details lost the title: %q", full.Title)
	}
}

func TestWithoutSummaryFallsBackToFirstMessage(t *testing.T) {
	base := t.TempDir()
	writeConversation(t, base, "conv-1", step{stepUserInput, userStep(1000, "hello\nthere")})
	threads, err := (&Provider{baseDir: base}).DiscoverThreads(context.Background())
	if err != nil || len(threads) != 1 {
		t.Fatalf("discover: %v, %d threads", err, len(threads))
	}
	if threads[0].Title != "hello there" || threads[0].WorkspacePath != "" {
		t.Errorf("got %+v", threads[0])
	}
}

func TestSkipsChildConversations(t *testing.T) {
	base := t.TempDir()
	writeConversation(t, base, "parent", step{stepUserInput, userStep(1000, "hi")})
	writeConversation(t, base, "child", step{stepUserInput, userStep(1000, "subtask")})
	writeSummary(t, base, "parent", "Parent", `[]`, "")
	writeSummary(t, base, "child", "Child", `[]`, "parent")
	threads, err := (&Provider{baseDir: base}).DiscoverThreads(context.Background())
	if err != nil || len(threads) != 1 || threads[0].OriginalID != "parent" {
		t.Fatalf("got %v, %+v", err, threads)
	}
}

func TestFirstWorkspace(t *testing.T) {
	cases := map[string]string{
		`["file:///C:/Users/me/my%20proj"]`:    filepath.FromSlash("C:/Users/me/my proj"),
		`["file:///home/me/proj","file:///x"]`: filepath.FromSlash("/home/me/proj"),
		`[]`:                                   "",
		`not json`:                             "",
		`["https://x.dev/y"]`:                  "",
	}
	for in, want := range cases {
		if got := firstWorkspace(in); got != want {
			t.Errorf("firstWorkspace(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestMalformedPayloadIsIgnored(t *testing.T) {
	for _, b := range [][]byte{nil, {0xff}, {0x9a, 0x01, 0x7f, 'x'}, {0x2a, 0x05, 0x0a}} {
		if s := pbString(b, fieldUserInput, fieldUserText); s != "" {
			t.Errorf("pbString(%x) = %q", b, s)
		}
		if !stepTime(b).IsZero() {
			t.Errorf("stepTime(%x) not zero", b)
		}
	}
}

func TestWriterIsReadOnly(t *testing.T) {
	w := NewWriter()
	if !w.ReadOnly() {
		t.Error("want read-only")
	}
	if _, err := w.WriteThread(context.Background(), domain.Thread{}); err == nil {
		t.Error("WriteThread should fail")
	}
	cmd := w.OpenCommand("abc", "")
	if got := cmd.Args; len(got) != 3 || got[1] != "--conversation" || got[2] != "abc" {
		t.Errorf("args = %v", got)
	}
}
