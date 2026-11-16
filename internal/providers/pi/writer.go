package pi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
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

// Handed-off assistant turns weren't produced by a pi model, so they carry a
// placeholder identity with zeroed usage instead of borrowing a real model's.
const (
	syntheticAPI      = "openai-completions"
	syntheticProvider = "threadman"
	syntheticModel    = "handoff"
)

type Writer struct {
	baseDir string
	// flat is set when the sessions directory is overridden: pi then stores
	// sessions directly in it instead of in one folder per workspace.
	flat bool
}

func NewWriter() *Writer {
	return &Writer{baseDir: sessionsDir(), flat: os.Getenv("PI_CODING_AGENT_SESSION_DIR") != ""}
}

func (w *Writer) Name() string { return providerName }

// encodeWorkspaceDir mirrors pi's session folder naming: drop the leading path
// separator, turn "/", "\" and ":" into "-", and wrap the result in "--".
// `pi --session` looks sessions up by working directory, so a session written
// to any other folder won't resume in place.
func encodeWorkspaceDir(workspacePath string) string {
	p := strings.TrimLeft(workspacePath, `/\`)
	p = strings.NewReplacer("/", "-", `\`, "-", ":", "-").Replace(p)
	return "--" + p + "--"
}

func (w *Writer) WriteThread(ctx context.Context, thread domain.Thread) (string, error) {
	sessionID := uuid.New().String()
	now := time.Now().UTC()

	dir := w.baseDir
	if !w.flat {
		dir = filepath.Join(dir, encodeWorkspaceDir(thread.WorkspacePath))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create session dir: %w", err)
	}

	stamp := strings.NewReplacer(":", "-", ".", "-").Replace(now.Format("2006-01-02T15:04:05.000Z"))
	path := filepath.Join(dir, stamp+"_"+sessionID+".jsonl")

	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("create session file: %w", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)

	if err := enc.Encode(map[string]any{
		"type":      "session",
		"version":   3,
		"id":        sessionID,
		"timestamp": isoTime(now),
		"cwd":       thread.WorkspacePath,
	}); err != nil {
		return "", err
	}

	if err := writeMessages(enc, thread.Messages, nil, map[string]bool{}, now); err != nil {
		return "", err
	}
	return sessionID, nil
}

// writeMessages encodes messages as entries chained from parent. pi rebuilds
// the conversation by walking parentId links from the last entry, so each
// entry has to point at the one before it. used holds the entry IDs already
// in the session.
func writeMessages(enc *json.Encoder, messages []domain.Message, parent any, used map[string]bool, now time.Time) error {
	parentID := parent
	for _, msg := range messages {
		if msg.Role != domain.RoleUser && msg.Role != domain.RoleAssistant {
			continue
		}
		ts := msg.Timestamp
		if ts.IsZero() {
			ts = now
		}
		ts = ts.UTC()

		id, err := entryID(used)
		if err != nil {
			return err
		}

		m := map[string]any{"role": string(msg.Role), "timestamp": ts.UnixMilli()}
		if msg.Role == domain.RoleUser {
			m["content"] = msg.Content
		} else {
			m["content"] = []map[string]any{{"type": "text", "text": msg.Content}}
			m["api"] = syntheticAPI
			m["provider"] = syntheticProvider
			m["model"] = syntheticModel
			m["usage"] = map[string]any{
				"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0, "totalTokens": 0,
				"cost": map[string]any{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0, "total": 0},
			}
			m["stopReason"] = "stop"
		}

		if err := enc.Encode(map[string]any{
			"type":      "message",
			"id":        id,
			"parentId":  parentID,
			"timestamp": isoTime(ts),
			"message":   m,
		}); err != nil {
			return err
		}
		parentID = id
	}
	return nil
}

// AppendMessages adds messages to the end of an existing session, chained
// from its last entry, so `pi --session` continues the same session with
// them on its active branch.
func (w *Writer) AppendMessages(ctx context.Context, target domain.Thread, messages []domain.Message) error {
	path := target.SourceFilePath
	if path == "" {
		return fmt.Errorf("pi session %s has no session file", target.OriginalID)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read session file: %w", err)
	}

	// The first line is the session header; its ID names the session and
	// is not part of the entry tree.
	used := map[string]bool{}
	var leaf any
	for i, line := range bytes.Split(data, []byte("\n")) {
		var e struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		if json.Unmarshal(bytes.TrimSpace(line), &e) != nil || e.ID == "" {
			continue
		}
		if i == 0 && e.Type == "session" {
			continue
		}
		used[e.ID] = true
		leaf = e.ID
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return fmt.Errorf("open session file: %w", err)
	}
	defer f.Close()

	if len(data) > 0 && data[len(data)-1] != '\n' {
		if _, err := f.Write([]byte{'\n'}); err != nil {
			return err
		}
	}
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	if err := writeMessages(enc, messages, leaf, used, time.Now().UTC()); err != nil {
		return err
	}
	return f.Close()
}

func (w *Writer) OpenCommand(sessionID string, workspacePath string) *exec.Cmd {
	cmd := exec.Command("pi", "--session", sessionID)
	cmd.Dir = workspacePath
	return cmd
}

func (w *Writer) DeleteThread(ctx context.Context, thread domain.Thread) error {
	return os.Remove(thread.SourceFilePath)
}

func isoTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// entryID returns an unused 8-character hex ID, the form pi uses for entries.
func entryID(used map[string]bool) (string, error) {
	for range 16 {
		b := make([]byte, 4)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		id := hex.EncodeToString(b)
		if !used[id] {
			used[id] = true
			return id, nil
		}
	}
	return "", fmt.Errorf("could not generate a unique entry id")
}
