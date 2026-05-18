package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/zuhailkhan/threadman/internal/domain"
)

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

func (w *Writer) WriteThread(ctx context.Context, thread domain.Thread) (string, error) {
	sessionID := uuid.New().String()

	encodedWS := url.PathEscape(thread.WorkspacePath)
	dir := filepath.Join(w.baseDir, encodedWS)
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
		"type":      "permission-mode",
		"sessionId": sessionID,
	}
	if err := enc.Encode(header); err != nil {
		return "", err
	}

	for _, msg := range thread.Messages {
		ts := msg.Timestamp
		if ts.IsZero() {
			ts = time.Now()
		}
		id := uuid.New().String()

		var entry map[string]any
		switch msg.Role {
		case domain.RoleUser, domain.RoleSystem:
			isMeta := msg.Role == domain.RoleSystem
			entry = map[string]any{
				"type":      "user",
				"uuid":      id,
				"timestamp": ts.Format(time.RFC3339),
				"isMeta":    isMeta,
				"cwd":       thread.WorkspacePath,
				"message": map[string]any{
					"content": msg.Content,
				},
			}
		case domain.RoleAssistant:
			msgID := uuid.New().String()
			entry = map[string]any{
				"type":      "assistant",
				"uuid":      id,
				"timestamp": ts.Format(time.RFC3339),
				"message": map[string]any{
					"id": msgID,
					"content": []map[string]any{
						{"type": "text", "text": msg.Content},
					},
				},
			}
		default:
			continue
		}

		if err := enc.Encode(entry); err != nil {
			return "", err
		}
	}

	return sessionID, nil
}

func (w *Writer) OpenCommand(sessionID string, workspacePath string) *exec.Cmd {
	cmd := exec.Command("claude", "--resume", sessionID)
	cmd.Dir = workspacePath
	return cmd
}
