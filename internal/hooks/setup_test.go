package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMergeCommandHookPreservesExistingHookFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	existing := `{
  "theme": "dark",
  "hooks": {
    "SessionStart": [{"matcher": "^startup$", "hooks": [{"type": "command", "command": "keep-me", "timeout": 10}]}],
    "Stop": [{"matcher": "", "hooks": [{"type": "command", "command": "other", "async": true}]}]
  }
}`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	if err := mergeCommandHook(path, "Stop", "threadman ingest --provider claude"); err != nil {
		t.Fatalf("merge: %v", err)
	}

	got := readSettings(t, path)
	if got["theme"] != "dark" {
		t.Errorf("top-level key lost: %v", got["theme"])
	}
	hooks := got["hooks"].(map[string]any)

	start := hooks["SessionStart"].([]any)[0].(map[string]any)
	if start["matcher"] != "^startup$" {
		t.Errorf("matcher lost: %v", start["matcher"])
	}
	if timeout := start["hooks"].([]any)[0].(map[string]any)["timeout"]; timeout != float64(10) {
		t.Errorf("timeout lost: %v", timeout)
	}

	stop := hooks["Stop"].([]any)
	if len(stop) != 2 {
		t.Fatalf("Stop matchers: got %d, want 2", len(stop))
	}
	if async := stop[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["async"]; async != true {
		t.Errorf("async lost: %v", async)
	}
}

func TestMergeCommandHookIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "settings.json")
	for range 2 {
		if err := mergeCommandHook(path, "Stop", "threadman ingest --provider claude"); err != nil {
			t.Fatalf("merge: %v", err)
		}
	}

	stop := readSettings(t, path)["hooks"].(map[string]any)["Stop"].([]any)
	if len(stop) != 1 {
		t.Errorf("Stop matchers: got %d, want 1", len(stop))
	}
}

func readSettings(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
