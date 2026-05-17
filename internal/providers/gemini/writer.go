package gemini

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
		baseDir: filepath.Join(home, ".gemini", "tmp"),
	}
}

func (w *Writer) Name() string {
	return "gemini"
}

func (w *Writer) WriteThread(ctx context.Context, thread domain.Thread) (string, error) {
	sessionID := uuid.New().String()
	now := time.Now()

	encodedWS := url.PathEscape(thread.WorkspacePath)
	dir := filepath.Join(w.baseDir, encodedWS, "chats")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create chats dir: %w", err)
	}

	path := filepath.Join(dir, sessionID+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return "", fmt.Errorf("create session file: %w", err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)

	meta := sessionMetadata{
		SessionID:   sessionID,
		StartTime:   now,
		LastUpdated: now,
	}
	if err := enc.Encode(meta); err != nil {
		return "", fmt.Errorf("encode metadata: %w", err)
	}

	for _, msg := range thread.Messages {
		ts := msg.Timestamp
		if ts.IsZero() {
			ts = now
		}

		entryType := "user"
		if msg.Role == domain.RoleAssistant {
			entryType = "gemini"
		}

		entry := logEntry{
			ID:        uuid.New().String(),
			Type:      entryType,
			Timestamp: ts.Format(time.RFC3339),
			Content:   msg.Content,
		}
		if err := enc.Encode(entry); err != nil {
			return "", fmt.Errorf("encode message: %w", err)
		}
	}

	return sessionID, nil
}

func (w *Writer) OpenCommand(sessionID string, workspacePath string) *exec.Cmd {
	cmd := exec.Command("gemini")
	cmd.Dir = workspacePath
	return cmd
}
