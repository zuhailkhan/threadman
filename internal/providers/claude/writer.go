package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/zuhailkhan/threadman/internal/domain"
)

// syntheticModel marks assistant turns that were not produced by a model in
// this session, so usage and pricing don't get attributed to a real one.
const syntheticModel = "<synthetic>"

type Writer struct {
	baseDir string
}

func NewWriter() *Writer {
	home, _ := os.UserHomeDir()
	return &Writer{
		baseDir: filepath.Join(home, ".claude", "projects"),
	}
}

func (w *Writer) Name() string {
	return "claude"
}

// encodeProjectDir mirrors how Claude Code names a workspace's folder under
// ~/.claude/projects: every character that isn't an ASCII letter or digit
// becomes a dash. `claude --resume` only looks in the current workspace's
// folder, so a session written anywhere else can't be found.
func encodeProjectDir(workspacePath string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		default:
			return '-'
		}
	}, workspacePath)
}

func (w *Writer) WriteThread(ctx context.Context, thread domain.Thread) (string, error) {
	sessionID := uuid.New().String()

	dir := filepath.Join(w.baseDir, encodeProjectDir(thread.WorkspacePath))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create project dir: %w", err)
	}

	path := filepath.Join(dir, sessionID+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return "", fmt.Errorf("create session file: %w", err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)

	header := map[string]any{
		"type":           "permission-mode",
		"permissionMode": "default",
		"sessionId":      sessionID,
	}
	if err := enc.Encode(header); err != nil {
		return "", err
	}

	// Claude Code rebuilds a conversation by walking parentUuid links, so each
	// entry has to point at the one before it.
	var parentUUID any
	for _, msg := range thread.Messages {
		ts := msg.Timestamp
		if ts.IsZero() {
			ts = time.Now()
		}
		id := uuid.New().String()

		entry := map[string]any{
			"uuid":        id,
			"parentUuid":  parentUUID,
			"isSidechain": false,
			"userType":    "external",
			"entrypoint":  "cli",
			"sessionId":   sessionID,
			"cwd":         thread.WorkspacePath,
			"timestamp":   ts.Format(time.RFC3339),
		}

		switch msg.Role {
		case domain.RoleUser, domain.RoleSystem:
			entry["type"] = "user"
			entry["isMeta"] = msg.Role == domain.RoleSystem
			entry["message"] = map[string]any{
				"role":    "user",
				"content": msg.Content,
			}
		case domain.RoleAssistant:
			entry["type"] = "assistant"
			entry["message"] = map[string]any{
				"id":            uuid.New().String(),
				"type":          "message",
				"role":          "assistant",
				"model":         syntheticModel,
				"stop_reason":   "end_turn",
				"stop_sequence": nil,
				"content": []map[string]any{
					{"type": "text", "text": msg.Content},
				},
				"usage": map[string]int{"input_tokens": 0, "output_tokens": 0},
			}
		default:
			continue
		}

		if err := enc.Encode(entry); err != nil {
			return "", err
		}
		parentUUID = id
	}

	return sessionID, nil
}

func (w *Writer) OpenCommand(sessionID string, workspacePath string) *exec.Cmd {
	cmd := exec.Command("claude", "--resume", sessionID)
	cmd.Dir = workspacePath
	return cmd
}

func (w *Writer) DeleteThread(ctx context.Context, thread domain.Thread) error {
	return os.Remove(thread.SourceFilePath)
}
