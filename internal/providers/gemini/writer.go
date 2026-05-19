package gemini

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

func projectHash(workspacePath string) string {
	h := sha256.Sum256([]byte(workspacePath))
	return hex.EncodeToString(h[:])
}

func (w *Writer) WriteThread(ctx context.Context, thread domain.Thread) (string, error) {
	sessionID := uuid.New().String()
	now := time.Now().UTC()

	dir := filepath.Join(w.baseDir, filepath.Base(thread.WorkspacePath), "chats")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create chats dir: %w", err)
	}

	filename := fmt.Sprintf("session-%s-%s.jsonl", now.Format("2006-01-02T15-04"), sessionID[:8])
	path := filepath.Join(dir, filename)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return "", fmt.Errorf("create session file: %w", err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)

	meta := sessionMetadata{
		SessionID:   sessionID,
		ProjectHash: projectHash(thread.WorkspacePath),
		StartTime:   now,
		LastUpdated: now,
		Kind:        "main",
		Title:       thread.Title,
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
	cmd := exec.Command("gemini", "--resume", sessionID)
	cmd.Dir = workspacePath
	return cmd
}

func (w *Writer) DeleteThread(ctx context.Context, thread domain.Thread) error {
	return os.Remove(thread.SourceFilePath)
}
