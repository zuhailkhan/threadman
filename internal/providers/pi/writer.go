package pi

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

const (
	sessionVersion = 3
	syntheticModel = "threadman-handoff"
)

type Writer struct {
	baseDir string
}

func NewWriter() *Writer {
	return &Writer{baseDir: sessionsDir()}
}

func (w *Writer) Name() string { return providerName }

// encodeProjectDir mirrors pi's session folder naming: the workspace path
// with its leading slash dropped and path separators/colons turned into
// dashes, wrapped in "--".
func encodeProjectDir(workspacePath string) string {
	s := strings.TrimLeft(workspacePath, `/\`)
	s = strings.NewReplacer("/", "-", `\`, "-", ":", "-").Replace(s)
	return "--" + s + "--"
}

func (w *Writer) WriteThread(ctx context.Context, thread domain.Thread) (string, error) {
	sessionID := uuid.New().String()
	now := time.Now().UTC()

	dir := filepath.Join(w.baseDir, encodeProjectDir(thread.WorkspacePath))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create session dir: %w", err)
	}

	stamp := strings.NewReplacer(":", "-", ".", "-").Replace(now.Format("2006-01-02T15:04:05.000Z"))
	path := filepath.Join(dir, stamp+"_"+sessionID+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return "", fmt.Errorf("create session file: %w", err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)

	if err := enc.Encode(map[string]any{
		"type":      "session",
		"version":   sessionVersion,
		"id":        sessionID,
		"timestamp": now.Format(time.RFC3339Nano),
		"cwd":       thread.WorkspacePath,
	}); err != nil {
		return "", err
	}

	var parentID any // null for the first entry
	for _, msg := range thread.Messages {
		ts := msg.Timestamp
		if ts.IsZero() {
			ts = now
		}
		id := uuid.New().String()[:8]

		var body map[string]any
		switch msg.Role {
		case domain.RoleUser, domain.RoleSystem:
			body = map[string]any{
				"role":      "user",
				"content":   []map[string]any{{"type": "text", "text": msg.Content}},
				"timestamp": ts.UnixMilli(),
			}
		case domain.RoleAssistant:
			body = map[string]any{
				"role":       "assistant",
				"content":    []map[string]any{{"type": "text", "text": msg.Content}},
				"api":        "unknown",
				"provider":   "threadman",
				"model":      syntheticModel,
				"stopReason": "stop",
				"usage": map[string]any{
					"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0, "totalTokens": 0,
					"cost": map[string]any{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0, "total": 0},
				},
				"timestamp": ts.UnixMilli(),
			}
		default:
			continue
		}

		if err := enc.Encode(map[string]any{
			"type":      "message",
			"id":        id,
			"parentId":  parentID,
			"timestamp": ts.UTC().Format(time.RFC3339Nano),
			"message":   body,
		}); err != nil {
			return "", err
		}
		parentID = id
	}

	return sessionID, nil
}

func (w *Writer) OpenCommand(sessionID string, workspacePath string) *exec.Cmd {
	cmd := exec.Command("pi", "--session", sessionID)
	cmd.Dir = workspacePath
	return cmd
}

func (w *Writer) DeleteThread(ctx context.Context, thread domain.Thread) error {
	return os.Remove(thread.SourceFilePath)
}
