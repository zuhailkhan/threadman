// Package pi reads and writes sessions of the pi coding agent
// (https://github.com/badlogic/pi-mono). Sessions are JSONL files stored at
// ~/.pi/agent/sessions/--<encoded-cwd>--/<timestamp>_<id>.jsonl.
package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zuhailkhan/threadman/internal/domain"
	"github.com/zuhailkhan/threadman/internal/ports"
)

const providerName = "pi"

// sessionsDir returns pi's session root, honouring PI_CODING_AGENT_DIR.
func sessionsDir() string {
	if dir := os.Getenv("PI_CODING_AGENT_DIR"); dir != "" {
		return filepath.Join(dir, "sessions")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".pi", "agent", "sessions")
}

type Provider struct {
	baseDir string
}

func NewProvider() *Provider {
	return &Provider{baseDir: sessionsDir()}
}

func (p *Provider) Name() string { return providerName }

type entry struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Timestamp string          `json:"timestamp"`
	CWD       string          `json:"cwd"`
	Name      string          `json:"name"`
	Message   json.RawMessage `json:"message"`
}

type message struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

func (p *Provider) DiscoverThreads(ctx context.Context) ([]domain.Thread, error) {
	dirs, err := os.ReadDir(p.baseDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ports.ErrSourceUnavailable, p.baseDir)
		}
		return nil, err
	}

	threads := []domain.Thread{}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		dirPath := filepath.Join(p.baseDir, d.Name())
		files, err := os.ReadDir(dirPath)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || filepath.Ext(f.Name()) != ".jsonl" {
				continue
			}
			t, err := parseMetadata(filepath.Join(dirPath, f.Name()))
			if err != nil {
				continue
			}
			threads = append(threads, t)
		}
	}
	return threads, nil
}

func parseMetadata(path string) (domain.Thread, error) {
	f, err := os.Open(path)
	if err != nil {
		return domain.Thread{}, err
	}
	defer f.Close()

	var (
		sessionID, cwd, name, firstMsg string
		createdAt, lastAt              time.Time
		sawHeader                      bool
	)

	sc := newScanner(f)
	for sc.Scan() {
		var e entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			continue
		}
		ts, _ := time.Parse(time.RFC3339, e.Timestamp)

		switch e.Type {
		case "session":
			sawHeader = true
			sessionID = e.ID
			cwd = e.CWD
			createdAt = ts
		case "session_info":
			if e.Name != "" {
				name = e.Name
			}
		case "message":
			var m message
			if err := json.Unmarshal(e.Message, &m); err == nil && m.Role == "user" && firstMsg == "" {
				firstMsg = extractText(m.Content)
			}
		}
		if ts.After(lastAt) {
			lastAt = ts
		}
	}
	if !sawHeader || sessionID == "" {
		return domain.Thread{}, fmt.Errorf("not a pi session: %s", path)
	}
	if createdAt.IsZero() {
		createdAt = lastAt
	}

	title := name
	if title == "" {
		title = truncate(firstMsg, 50)
	}

	return domain.Thread{
		ID:             providerName + "-" + sessionID,
		Provider:       providerName,
		OriginalID:     sessionID,
		Title:          title,
		WorkspacePath:  cwd,
		SourceFilePath: path,
		CreatedAt:      createdAt,
		LastSyncedAt:   lastAt,
	}, nil
}

func (p *Provider) GetThreadDetails(ctx context.Context, t domain.Thread) (domain.Thread, error) {
	f, err := os.Open(t.SourceFilePath)
	if err != nil {
		return t, err
	}
	defer f.Close()

	messages := []domain.Message{}
	sc := newScanner(f)
	for sc.Scan() {
		var e entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil || e.Type != "message" {
			continue
		}
		var m message
		if err := json.Unmarshal(e.Message, &m); err != nil {
			continue
		}
		var role domain.Role
		switch m.Role {
		case "user":
			role = domain.RoleUser
		case "assistant":
			role = domain.RoleAssistant
		default: // toolResult, bashExecution, etc.
			continue
		}
		content := extractText(m.Content)
		if content == "" {
			continue
		}
		ts, _ := time.Parse(time.RFC3339, e.Timestamp)
		messages = append(messages, domain.Message{ID: e.ID, Role: role, Content: content, Timestamp: ts})
	}
	t.Messages = messages
	return t, nil
}

// IngestFromHook lets `threadman ingest --provider pi` import one session.
func (p *Provider) IngestFromHook(ctx context.Context, payload ports.HookPayload) (domain.Thread, error) {
	path := payload.TranscriptPath
	if path == "" {
		if payload.SessionID == "" {
			return domain.Thread{}, fmt.Errorf("hook payload missing transcript_path and session_id")
		}
		var err error
		if path, err = p.findSessionFile(payload.SessionID); err != nil {
			return domain.Thread{}, err
		}
	}
	t, err := parseMetadata(path)
	if err != nil {
		return domain.Thread{}, err
	}
	return p.GetThreadDetails(ctx, t)
}

func (p *Provider) findSessionFile(sessionID string) (string, error) {
	matches, _ := filepath.Glob(filepath.Join(p.baseDir, "*", "*_"+sessionID+".jsonl"))
	if len(matches) == 0 {
		return "", fmt.Errorf("session file not found for session_id: %s", sessionID)
	}
	return matches[0], nil
}

// extractText returns the text of a string or block-array message body,
// ignoring thinking, tool calls and images.
func extractText(raw any) string {
	switch v := raw.(type) {
	case string:
		return v
	case []any:
		var sb strings.Builder
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok || m["type"] != "text" {
				continue
			}
			if text, ok := m["text"].(string); ok {
				sb.WriteString(text)
			}
		}
		return sb.String()
	}
	return ""
}

func newScanner(f *os.File) *bufio.Scanner {
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 2*1024*1024), 16*1024*1024)
	return s
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-3]) + "..."
}
