package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zuhailkhan/threadman/internal/domain"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeMeta writes a rollout holding only a session_meta line.
func writeMeta(t *testing.T, home, stamp, id, originator, provider string) {
	t.Helper()
	payload := map[string]any{"id": id, "originator": originator, "source": "cli"}
	if provider != "" {
		payload["model_provider"] = provider
	}
	line, _ := json.Marshal(map[string]any{"type": "session_meta", "payload": payload})
	writeFile(t, filepath.Join(home, "sessions", "2026", "11", "18", "rollout-"+stamp+"-"+id+".jsonl"), string(line)+"\n")
}

func TestSessionProviderFromConfig(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "config.toml"), `# local model
model_provider = "lmstudio"   # set by hand
model = "google/gemma-4-12b"

[tui]
model_provider = "not-top-level"
`)
	writeMeta(t, home, "2026-11-18T07-00-00", "a", "codex-tui", "ollama")
	if got := sessionModelProvider(home); got != "lmstudio" {
		t.Errorf("got %q, want the config's lmstudio", got)
	}
}

func TestSessionProviderFromActiveProfile(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "config.toml"), `model_provider = "openai"
profile = "local"

[profiles.local]
model_provider = 'ollama'

[profiles.other]
model_provider = "azure"
`)
	if got := sessionModelProvider(home); got != "ollama" {
		t.Errorf("got %q, want the active profile's ollama", got)
	}
}

// Without a config setting, the newest session Codex itself wrote decides;
// sessions threadman wrote, and ones with no provider, are skipped.
func TestSessionProviderFromNewestCodexSession(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "config.toml"), "[tui]\nscreen_reader_detection_done = true\n")
	writeMeta(t, home, "2026-11-18T04-00-00", "older", "codex-tui", "ollama")
	writeMeta(t, home, "2026-11-18T05-00-00", "newest-real", "codex_exec", "lmstudio")
	writeMeta(t, home, "2026-11-18T06-00-00", "by-threadman", "threadman", "openai")
	writeMeta(t, home, "2026-11-18T06-30-00", "no-provider", "codex-tui", "")
	if got := sessionModelProvider(home); got != "lmstudio" {
		t.Errorf("got %q, want the newest Codex session's lmstudio", got)
	}
}

func TestSessionProviderDefault(t *testing.T) {
	if got := sessionModelProvider(t.TempDir()); got != "openai" {
		t.Errorf("got %q, want Codex's default openai", got)
	}
}

// A session threadman writes names its provider, so `codex resume` accepts it.
func TestWriteThreadRecordsModelProvider(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "config.toml"), "model_provider = \"lmstudio\"\n")
	w := &Writer{home: home}
	id, err := w.WriteThread(context.Background(), domain.Thread{
		WorkspacePath: home,
		Messages:      []domain.Message{{Role: domain.RoleUser, Content: "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	var path string
	_ = filepath.WalkDir(filepath.Join(home, "sessions"), func(p string, d os.DirEntry, err error) error {
		if err == nil && strings.Contains(d.Name(), id) {
			path = p
		}
		return nil
	})
	meta, ok := readSessionMeta(path)
	if !ok {
		t.Fatalf("no session_meta in %s", path)
	}
	if meta.ModelProvider != "lmstudio" || meta.Originator != "threadman" || meta.ID != id {
		t.Errorf("session_meta = %+v", meta)
	}
}
