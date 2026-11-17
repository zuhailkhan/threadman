package codex

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/zuhailkhan/threadman/internal/domain"
	"github.com/zuhailkhan/threadman/internal/ports"
)

const (
	idA = "019a0000-0000-7000-8000-00000000000a"
	idB = "019a0000-0000-7000-8000-00000000000b"
)

func meta(id, extra string) string {
	return `{"timestamp":"2026-01-01T00:00:00.000Z","type":"session_meta","payload":{"id":"` + id +
		`","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/work/proj","originator":"codex_cli_rs","cli_version":"1.0.0","source":"cli"` + extra + `}}`
}

func writeRollout(t *testing.T, home, name string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(home, "sessions", "2026", "01", "01")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	data := []byte(strings.Join(lines, "\n") + "\n")
	if strings.HasSuffix(name, compressedSuffix) {
		enc, err := zstd.NewWriter(nil)
		if err != nil {
			t.Fatal(err)
		}
		data = enc.EncodeAll(data, nil)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func discoverOne(t *testing.T, home string) domain.Thread {
	t.Helper()
	p := &Provider{home: home}
	threads, err := p.DiscoverThreads(context.Background())
	if err != nil || len(threads) != 1 {
		t.Fatalf("discover: %v, %d threads", err, len(threads))
	}
	full, err := p.GetThreadDetails(context.Background(), threads[0])
	if err != nil {
		t.Fatal(err)
	}
	return full
}

func contents(msgs []domain.Message) string {
	var parts []string
	for _, m := range msgs {
		parts = append(parts, string(m.Role)+":"+m.Content)
	}
	return strings.Join(parts, "|")
}

func TestDiscoverMissingDirIsUnavailable(t *testing.T) {
	p := &Provider{home: filepath.Join(t.TempDir(), "missing")}
	_, err := p.DiscoverThreads(context.Background())
	if !errors.Is(err, ports.ErrSourceUnavailable) {
		t.Errorf("got %v, want ErrSourceUnavailable", err)
	}
}

func TestLegacyRollout(t *testing.T) {
	home := t.TempDir()
	writeRollout(t, home, "rollout-2026-01-01T00-00-00-"+idA+".jsonl",
		meta(idA, ""),
		`{"timestamp":"2026-01-01T00:00:01.000Z","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"be nice"}]}}`,
		`{"timestamp":"2026-01-01T00:00:01.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>x</environment_context>"}]}}`,
		`{"timestamp":"2026-01-01T00:00:02.000Z","type":"turn_context","payload":{"cwd":"/work/proj"}}`,
		`{"timestamp":"2026-01-01T00:00:03.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the bug"}]}}`,
		`{"timestamp":"2026-01-01T00:00:03.000Z","type":"event_msg","payload":{"type":"user_message","message":"fix the bug","kind":"plain"}}`,
		`{"timestamp":"2026-01-01T00:00:04.000Z","type":"event_msg","payload":{"type":"agent_reasoning","text":"hmm"}}`,
		`{"timestamp":"2026-01-01T00:00:05.000Z","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{}","call_id":"c1"}}`,
		`{"timestamp":"2026-01-01T00:00:06.000Z","type":"event_msg","payload":{"type":"agent_message","message":"Fixed it."}}`,
		`{"timestamp":"2026-01-01T00:00:06.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Fixed it."}]}}`,
		`{"timestamp":"2026-01-01T00:00:07.000Z","type":"event_msg","payload":{"type":"token_count","info":null}}`,
		`not json`,
	)
	th := discoverOne(t, home)
	if th.ID != "codex-"+idA || th.OriginalID != idA || th.Provider != "codex" {
		t.Errorf("ids: %+v", th)
	}
	if th.Title != "fix the bug" || th.WorkspacePath != "/work/proj" {
		t.Errorf("title/workspace: %q %q", th.Title, th.WorkspacePath)
	}
	if got := th.CreatedAt.Format("15:04:05"); got != "00:00:00" {
		t.Errorf("created = %s", got)
	}
	if got := th.LastSyncedAt.Format("15:04:05"); got != "00:00:07" {
		t.Errorf("last = %s", got)
	}
	if got := contents(th.Messages); got != "user:fix the bug|assistant:Fixed it." {
		t.Errorf("messages = %s", got)
	}
	if got := th.Messages[1].Timestamp.Format("15:04:05"); got != "00:00:06" {
		t.Errorf("timestamp = %s", got)
	}
}

func TestPaginatedRollout(t *testing.T) {
	home := t.TempDir()
	writeRollout(t, home, "rollout-2026-01-01T00-00-00-"+idA+".jsonl",
		meta(idA, `,"history_mode":"paginated"`),
		`{"timestamp":"2026-01-01T00:00:03.000Z","ordinal":1,"type":"event_msg","payload":{"type":"item_completed","thread_id":"`+idA+`","turn_id":"t1","item":{"type":"UserMessage","id":"u1","content":[{"type":"text","text":"hello ","text_elements":[]},{"type":"local_image","path":"/x.png"},{"type":"text","text":"there"}]}}}`,
		`{"timestamp":"2026-01-01T00:00:04.000Z","ordinal":2,"type":"event_msg","payload":{"type":"item_completed","thread_id":"`+idA+`","turn_id":"t1","item":{"type":"CommandExecution","id":"c1","command":"ls"}}}`,
		`{"timestamp":"2026-01-01T00:00:05.000Z","ordinal":3,"type":"event_msg","payload":{"type":"item_completed","thread_id":"`+idA+`","turn_id":"t1","item":{"type":"AgentMessage","id":"a1","content":[{"type":"Text","text":"Hi!"}],"phase":"final_answer"}}}`,
	)
	th := discoverOne(t, home)
	if got := contents(th.Messages); got != "user:hello there|assistant:Hi!" {
		t.Errorf("messages = %s", got)
	}
}

func TestFallsBackToResponseItemsWithoutEvents(t *testing.T) {
	home := t.TempDir()
	writeRollout(t, home, "rollout-2026-01-01T00-00-00-"+idA+".jsonl",
		meta(idA, ""),
		`{"timestamp":"2026-01-01T00:00:01.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<user_instructions>x</user_instructions>"}]}}`,
		`{"timestamp":"2026-01-01T00:00:02.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`,
		`{"timestamp":"2026-01-01T00:00:03.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hi!"}]}}`,
	)
	if got := contents(discoverOne(t, home).Messages); got != "user:hello|assistant:Hi!" {
		t.Errorf("messages = %s", got)
	}
}

func TestStripsContextPrefixFromUserMessage(t *testing.T) {
	home := t.TempDir()
	writeRollout(t, home, "rollout-2026-01-01T00-00-00-"+idA+".jsonl",
		meta(idA, ""),
		`{"timestamp":"2026-01-01T00:00:01.000Z","type":"event_msg","payload":{"type":"user_message","message":"# Context\nopen files: a.go\n\n## My request for Codex:\nrename it\n"}}`,
	)
	if got := contents(discoverOne(t, home).Messages); got != "user:rename it" {
		t.Errorf("messages = %s", got)
	}
}

func TestRollbackDropsTurns(t *testing.T) {
	home := t.TempDir()
	ev := func(kind, msg string) string {
		return `{"timestamp":"2026-01-01T00:00:01.000Z","type":"event_msg","payload":{"type":"` + kind + `","message":"` + msg + `"}}`
	}
	writeRollout(t, home, "rollout-2026-01-01T00-00-00-"+idA+".jsonl",
		meta(idA, ""),
		ev("user_message", "one"), ev("agent_message", "1"),
		ev("user_message", "two"), ev("agent_message", "2"),
		ev("user_message", "three"), ev("agent_message", "3"),
		`{"timestamp":"2026-01-01T00:00:02.000Z","type":"event_msg","payload":{"type":"thread_rolled_back","num_turns":2}}`,
		ev("user_message", "four"),
	)
	if got := contents(discoverOne(t, home).Messages); got != "user:one|assistant:1|user:four" {
		t.Errorf("messages = %s", got)
	}
}

func TestCompressedRollout(t *testing.T) {
	home := t.TempDir()
	writeRollout(t, home, "rollout-2026-01-01T00-00-00-"+idA+".jsonl.zst",
		meta(idA, ""),
		`{"timestamp":"2026-01-01T00:00:01.000Z","type":"event_msg","payload":{"type":"user_message","message":"hello"}}`,
	)
	if got := contents(discoverOne(t, home).Messages); got != "user:hello" {
		t.Errorf("messages = %s", got)
	}
}

func TestNewestRolloutOfAThreadWins(t *testing.T) {
	home := t.TempDir()
	user := func(msg string) string {
		return `{"timestamp":"2026-01-01T00:00:01.000Z","type":"event_msg","payload":{"type":"user_message","message":"` + msg + `"}}`
	}
	writeRollout(t, home, "rollout-2026-01-01T00-00-00-"+idA+".jsonl", meta(idA, ""), user("old"))
	writeRollout(t, home, "rollout-2026-01-01T09-00-00-"+idA+"_"+idB+".jsonl", meta(idA, ""), user("reverted"))
	// A compressed copy left beside its plain file is ignored.
	writeRollout(t, home, "rollout-2026-01-01T09-00-00-"+idA+"_"+idB+".jsonl.zst", meta(idA, ""), user("stale"))
	if got := contents(discoverOne(t, home).Messages); got != "user:reverted" {
		t.Errorf("messages = %s", got)
	}
}

func TestSkipsSessionsCodexStartedItself(t *testing.T) {
	home := t.TempDir()
	user := `{"timestamp":"2026-01-01T00:00:01.000Z","type":"event_msg","payload":{"type":"user_message","message":"hi"}}`
	sub := strings.Replace(meta("019a0000-0000-7000-8000-000000000001", ""), `"source":"cli"`, `"source":{"subagent":"review"}`, 1)
	writeRollout(t, home, "rollout-2026-01-01T00-00-01-019a0000-0000-7000-8000-000000000001.jsonl", sub, user)
	writeRollout(t, home, "rollout-2026-01-01T00-00-02-019a0000-0000-7000-8000-000000000002.jsonl",
		meta("019a0000-0000-7000-8000-000000000002", `,"parent_thread_id":"`+idA+`"`), user)
	writeRollout(t, home, "rollout-2026-01-01T00-00-03-019a0000-0000-7000-8000-000000000003.jsonl",
		meta("019a0000-0000-7000-8000-000000000003", `,"thread_source":"memory_consolidation"`), user)
	writeRollout(t, home, "rollout-2026-01-01T00-00-00-"+idA+".jsonl", meta(idA, `,"thread_source":"user"`), user)
	writeRollout(t, home, "notes.jsonl", user)
	if th := discoverOne(t, home); th.OriginalID != idA {
		t.Errorf("got %s", th.OriginalID)
	}
}

func TestThreadNameFromSessionIndex(t *testing.T) {
	home := t.TempDir()
	writeRollout(t, home, "rollout-2026-01-01T00-00-00-"+idA+".jsonl",
		meta(idA, ""),
		`{"timestamp":"2026-01-01T00:00:01.000Z","type":"event_msg","payload":{"type":"user_message","message":"hello"}}`,
	)
	index := `{"id":"` + idA + `","thread_name":"First name","updated_at":"2026-01-01T00:00:00Z"}` + "\n" +
		`{"id":"` + idB + `","thread_name":"Other","updated_at":"2026-01-01T00:00:00Z"}` + "\n" +
		`{"id":"` + idA + `","thread_name":" Renamed ","updated_at":"2026-01-02T00:00:00Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(home, "session_index.jsonl"), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}
	if th := discoverOne(t, home); th.Title != "Renamed" {
		t.Errorf("title = %q", th.Title)
	}
}

func TestWriterRoundTrip(t *testing.T) {
	home := t.TempDir()
	w := &Writer{home: home}
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	id, err := w.WriteThread(context.Background(), domain.Thread{
		WorkspacePath: "/work/proj",
		Messages: []domain.Message{
			{Role: domain.RoleSystem, Content: "ignored"},
			{Role: domain.RoleUser, Content: "is a < b && \"quoted\"?\nline two", Timestamp: at},
			{Role: domain.RoleAssistant, Content: "Yes."},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	th := discoverOne(t, home)
	if th.OriginalID != id || th.WorkspacePath != "/work/proj" {
		t.Errorf("got %+v", th)
	}
	if got := contents(th.Messages); got != "user:is a < b && \"quoted\"?\nline two|assistant:Yes." {
		t.Errorf("messages = %s", got)
	}
	if !th.Messages[0].Timestamp.Equal(at) {
		t.Errorf("timestamp = %v", th.Messages[0].Timestamp)
	}

	name, ok := parseRolloutName(filepath.Base(th.SourceFilePath))
	if !ok || name.threadID != id {
		t.Errorf("file name %q does not carry the session id", th.SourceFilePath)
	}
	// The model is sent response_items on resume, so each message needs one.
	raw, _ := os.ReadFile(th.SourceFilePath)
	if n := strings.Count(string(raw), `"type":"response_item"`); n != 2 {
		t.Errorf("%d response_items, want 2", n)
	}

	if err := w.DeleteThread(context.Background(), th); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(th.SourceFilePath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("file still there: %v", err)
	}
}

func TestOpenCommand(t *testing.T) {
	cmd := (&Writer{}).OpenCommand("abc", "/work/proj")
	if got := cmd.Args; len(got) != 3 || got[1] != "resume" || got[2] != "abc" || cmd.Dir != "/work/proj" {
		t.Errorf("args = %v dir = %q", got, cmd.Dir)
	}
}

func TestAppendMessages(t *testing.T) {
	home := t.TempDir()
	path := writeRollout(t, home, "rollout-2026-01-01T00-00-00-"+idA+".jsonl",
		meta(idA, ""),
		`{"timestamp":"2026-01-01T00:00:01.000Z","type":"event_msg","payload":{"type":"user_message","message":"hello"}}`,
	)
	// A rollout that does not end in a newline must not have a record glued
	// onto its last line.
	raw, _ := os.ReadFile(path)
	if err := os.WriteFile(path, []byte(strings.TrimRight(string(raw), "\n")), 0o644); err != nil {
		t.Fatal(err)
	}

	w := &Writer{home: home}
	th := discoverOne(t, home)
	err := w.AppendMessages(context.Background(), th, []domain.Message{
		{Role: domain.RoleUser, Content: "catch up"},
		{Role: domain.RoleAssistant, Content: "ok"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := contents(discoverOne(t, home).Messages); got != "user:hello|user:catch up|assistant:ok" {
		t.Errorf("messages = %s", got)
	}
}

func TestAppendMessagesRestoresCompressedRollout(t *testing.T) {
	home := t.TempDir()
	path := writeRollout(t, home, "rollout-2026-01-01T00-00-00-"+idA+".jsonl.zst",
		meta(idA, ""),
		`{"timestamp":"2026-01-01T00:00:01.000Z","type":"event_msg","payload":{"type":"user_message","message":"hello"}}`,
	)
	w := &Writer{home: home}
	th := discoverOne(t, home)
	if err := w.AppendMessages(context.Background(), th, []domain.Message{{Role: domain.RoleUser, Content: "more"}}); err != nil {
		t.Fatal(err)
	}
	after := discoverOne(t, home)
	if after.SourceFilePath != strings.TrimSuffix(path, ".zst") {
		t.Errorf("read from %s, want the plain file", after.SourceFilePath)
	}
	if got := contents(after.Messages); got != "user:hello|user:more" {
		t.Errorf("messages = %s", got)
	}
}

func TestAppendMessagesToPaginatedRollout(t *testing.T) {
	home := t.TempDir()
	path := writeRollout(t, home, "rollout-2026-01-01T00-00-00-"+idA+".jsonl",
		strings.Replace(meta(idA, `,"history_mode":"paginated"`), `"type":"session_meta"`, `"ordinal":0,"type":"session_meta"`, 1),
		`{"timestamp":"2026-01-01T00:00:03.000Z","ordinal":1,"type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","id":"u1","content":[{"type":"text","text":"hello"}]}}}`,
		`{"timestamp":"2026-01-01T00:00:04.000Z","ordinal":7,"type":"event_msg","payload":{"type":"token_count"}}`,
	)
	w := &Writer{home: home}
	err := w.AppendMessages(context.Background(), discoverOne(t, home), []domain.Message{
		{Role: domain.RoleUser, Content: "catch up"},
		{Role: domain.RoleAssistant, Content: "ok"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := contents(discoverOne(t, home).Messages); got != "user:hello|user:catch up|assistant:ok" {
		t.Errorf("messages = %s", got)
	}

	// Every appended record continues the numbering, and the messages sit
	// inside one started and completed turn.
	raw, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")[3:]
	var kinds []string
	for i, l := range lines {
		var rec struct {
			Ordinal *uint64 `json:"ordinal"`
			Type    string  `json:"type"`
			Payload struct {
				Type     string `json:"type"`
				ThreadID string `json:"thread_id"`
			} `json:"payload"`
		}
		if err := json.Unmarshal([]byte(l), &rec); err != nil {
			t.Fatal(err)
		}
		if rec.Ordinal == nil || *rec.Ordinal != uint64(8+i) {
			t.Errorf("line %d: ordinal %v, want %d", i, rec.Ordinal, 8+i)
		}
		if rec.Payload.Type == "item_completed" && rec.Payload.ThreadID != idA {
			t.Errorf("line %d: thread_id %q", i, rec.Payload.ThreadID)
		}
		kinds = append(kinds, rec.Payload.Type)
	}
	if got := strings.Join(kinds, ","); got != "task_started,message,item_completed,message,item_completed,task_complete" {
		t.Errorf("records = %s", got)
	}
}
