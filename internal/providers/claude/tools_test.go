package claude

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zuhailkhan/threadman/internal/domain"
)

// Claude Code writes one assistant message as several entries sharing its
// message ID, and each tool's result in a later user entry.
func TestGetThreadDetailsCapturesToolCalls(t *testing.T) {
	lines := []string{
		`{"type":"user","uuid":"u1","timestamp":"2026-01-01T00:00:01Z","message":{"role":"user","content":"run the tests"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-01-01T00:00:02Z","message":{"id":"msg_1","content":[{"type":"text","text":"Running them."}]}}`,
		`{"type":"assistant","uuid":"a2","timestamp":"2026-01-01T00:00:02Z","message":{"id":"msg_1","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"go test ./...","description":"run tests"}}]}}`,
		`{"type":"assistant","uuid":"a3","timestamp":"2026-01-01T00:00:02Z","message":{"id":"msg_1","content":[{"type":"tool_use","id":"toolu_2","name":"Read","input":{"file_path":"/w/missing.go"}}]}}`,
		`{"type":"user","uuid":"r1","timestamp":"2026-01-01T00:00:03Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok  example.com/p  0.3s","is_error":false}]}}`,
		`{"type":"user","uuid":"r2","timestamp":"2026-01-01T00:00:03Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_2","content":[{"type":"text","text":"File does not exist."}],"is_error":true}]}}`,
		// The same entry written twice must not double the text.
		`{"type":"assistant","uuid":"a1","timestamp":"2026-01-01T00:00:02Z","message":{"id":"msg_1","content":[{"type":"text","text":"Running them."}]}}`,
		// A turn that is only a tool call.
		`{"type":"assistant","uuid":"a4","timestamp":"2026-01-01T00:00:04Z","message":{"id":"msg_2","content":[{"type":"tool_use","id":"toolu_3","name":"Glob","input":{"pattern":"**/*.go"}}]}}`,
		`{"type":"user","uuid":"r3","timestamp":"2026-01-01T00:00:05Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_3","content":"a.go"}]}}`,
		`{"type":"assistant","uuid":"a5","timestamp":"2026-01-01T00:00:06Z","message":{"id":"msg_3","content":[{"type":"text","text":"All passing."}]}}`,
	}
	path := filepath.Join(t.TempDir(), "sess.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	full, err := (&Provider{}).GetThreadDetails(context.Background(), domain.Thread{SourceFilePath: path})
	if err != nil {
		t.Fatal(err)
	}

	if len(full.Messages) != 4 {
		for _, m := range full.Messages {
			t.Logf("%s %q %+v", m.Role, m.Content, m.Tools)
		}
		t.Fatalf("got %d messages, want 4 (tool results are not messages of their own)", len(full.Messages))
	}
	first := full.Messages[1]
	if first.Content != "Running them." {
		t.Errorf("merged text = %q", first.Content)
	}
	want := []domain.ToolCall{
		{Name: "Bash", Input: "go test ./...", Output: "ok  example.com/p  0.3s"},
		{Name: "Read", Input: `{"file_path":"/w/missing.go"}`, Output: "File does not exist.", Error: true},
	}
	if len(first.Tools) != 2 || first.Tools[0] != want[0] || first.Tools[1] != want[1] {
		t.Errorf("tools = %+v\nwant   %+v", first.Tools, want)
	}

	toolOnly := full.Messages[2]
	if toolOnly.Role != domain.RoleAssistant || toolOnly.Content != "" || len(toolOnly.Tools) != 1 || toolOnly.Tools[0].Output != "a.go" {
		t.Errorf("tool-only turn = %+v", toolOnly)
	}
	if full.Messages[3].Content != "All passing." {
		t.Errorf("last message = %+v", full.Messages[3])
	}
}

func TestToolOutputIsShortened(t *testing.T) {
	big := strings.Repeat("y", domain.MaxToolOutput+500)
	lines := []string{
		`{"type":"assistant","uuid":"a1","timestamp":"2026-01-01T00:00:01Z","message":{"id":"m1","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"cat big"}}]}}`,
		`{"type":"user","uuid":"r1","timestamp":"2026-01-01T00:00:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"` + big + `"}]}}`,
	}
	path := filepath.Join(t.TempDir(), "sess.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	full, err := (&Provider{}).GetThreadDetails(context.Background(), domain.Thread{SourceFilePath: path})
	if err != nil {
		t.Fatal(err)
	}
	out := full.Messages[0].Tools[0].Output
	if !strings.HasSuffix(out, "… (500 more characters)") {
		t.Errorf("output not shortened: %d characters", len([]rune(out)))
	}
}
