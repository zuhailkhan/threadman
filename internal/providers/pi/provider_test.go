package pi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zuhailkhan/threadman/internal/ports"
)

func writeSession(t *testing.T, lines ...string) *Provider {
	t.Helper()
	base := t.TempDir()
	dir := filepath.Join(base, "--proj--")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "2026-01-01T00-00-00-000Z_abc.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Provider{baseDir: base}
}

const header = `{"type":"session","version":3,"id":"sess-1","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/work/proj"}`

func TestDiscoverMissingDirIsUnavailable(t *testing.T) {
	p := &Provider{baseDir: filepath.Join(t.TempDir(), "missing")}
	_, err := p.DiscoverThreads(context.Background())
	if !errors.Is(err, ports.ErrSourceUnavailable) {
		t.Errorf("got %v, want ErrSourceUnavailable", err)
	}
}

func TestBasicThread(t *testing.T) {
	p := writeSession(t,
		header,
		`{"type":"model_change","id":"m1","parentId":null,"timestamp":"2026-01-01T00:00:01.000Z","provider":"x","modelId":"y"}`,
		`{"type":"message","id":"s1","parentId":"m1","timestamp":"2026-01-01T00:00:02.000Z","message":{"role":"system","content":""}}`,
		`{"type":"message","id":"u1","parentId":"s1","timestamp":"2026-01-01T00:00:03.000Z","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`,
		`{"type":"message","id":"a1","parentId":"u1","timestamp":"2026-01-01T00:00:04.000Z","message":{"role":"assistant","content":[{"type":"thinking","text":"hmm"},{"type":"text","text":"Hi!"},{"type":"toolCall","name":"bash"}]}}`,
		`{"type":"message","id":"r1","parentId":"a1","timestamp":"2026-01-01T00:00:05.000Z","message":{"role":"toolResult","content":[{"type":"text","text":"out"}]}}`,
	)
	threads, err := p.DiscoverThreads(context.Background())
	if err != nil || len(threads) != 1 {
		t.Fatalf("discover: %v, %d threads", err, len(threads))
	}
	th := threads[0]
	if th.ID != "pi-sess-1" || th.OriginalID != "sess-1" || th.Provider != "pi" {
		t.Errorf("ids: %+v", th)
	}
	if th.Title != "hello" || th.WorkspacePath != "/work/proj" {
		t.Errorf("title/workspace: %q %q", th.Title, th.WorkspacePath)
	}
	if got := th.LastSyncedAt.Format("15:04:05"); got != "00:00:05" {
		t.Errorf("last = %s", got)
	}
	full, err := p.GetThreadDetails(context.Background(), th)
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Messages) != 2 || full.Messages[0].Content != "hello" || full.Messages[1].Content != "Hi!" {
		t.Errorf("messages: %+v", full.Messages)
	}
}

func TestActiveBranchAndSessionName(t *testing.T) {
	p := writeSession(t,
		header,
		`{"type":"message","id":"u1","parentId":null,"timestamp":"2026-01-01T00:00:01.000Z","message":{"role":"user","content":"first"}}`,
		`{"type":"message","id":"a1","parentId":"u1","timestamp":"2026-01-01T00:00:02.000Z","message":{"role":"assistant","content":[{"type":"text","text":"abandoned"}]}}`,
		`{"type":"message","id":"a2","parentId":"u1","timestamp":"2026-01-01T00:00:03.000Z","message":{"role":"assistant","content":[{"type":"text","text":"kept"}]}}`,
		`{"type":"session_info","id":"i1","parentId":"a2","timestamp":"2026-01-01T00:00:04.000Z","name":"My thread"}`,
	)
	threads, _ := p.DiscoverThreads(context.Background())
	if threads[0].Title != "My thread" {
		t.Errorf("title = %q", threads[0].Title)
	}
	full, _ := p.GetThreadDetails(context.Background(), threads[0])
	if len(full.Messages) != 2 || full.Messages[1].Content != "kept" {
		t.Errorf("want active branch only, got %+v", full.Messages)
	}
}

func TestNonSessionFileSkipped(t *testing.T) {
	p := writeSession(t, `{"type":"message","id":"x"}`)
	threads, err := p.DiscoverThreads(context.Background())
	if err != nil || len(threads) != 0 {
		t.Errorf("got %v, %d threads", err, len(threads))
	}
}
