package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zuhailkhan/threadman/internal/domain"
	"github.com/zuhailkhan/threadman/internal/ports"
)

type Provider struct {
	baseDir string
}

func NewProvider() *Provider {
	home, _ := os.UserHomeDir()
	return &Provider{
		baseDir: filepath.Join(home, ".claude", "projects"),
	}
}

func (p *Provider) Name() string {
	return "claude"
}

type baseEntry struct {
	Type string `json:"type"`
}

type permissionModeEntry struct {
	SessionID string `json:"sessionId"`
}

type userEntry struct {
	UUID      string      `json:"uuid"`
	Timestamp string      `json:"timestamp"`
	IsMeta    bool        `json:"isMeta"`
	CWD       string      `json:"cwd"`
	Message   userMessage `json:"message"`
}

type userMessage struct {
	Content any `json:"content"`
}

type assistantEntry struct {
	UUID      string           `json:"uuid"`
	Timestamp string           `json:"timestamp"`
	Message   assistantMessage `json:"message"`
}

type assistantMessage struct {
	ID      string         `json:"id"`
	Content []contentBlock `json:"content"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type aiTitleEntry struct {
	AITitle string `json:"aiTitle"`
}

type customTitleEntry struct {
	CustomTitle string `json:"customTitle"`
}

type timestampEntry struct {
	Timestamp string `json:"timestamp"`
}

func (p *Provider) DiscoverThreads(ctx context.Context) ([]domain.Thread, error) {
	threads := []domain.Thread{}

	projectDirs, err := os.ReadDir(p.baseDir)
	if err != nil {
		return nil, err
	}

	for _, projectDir := range projectDirs {
		if !projectDir.IsDir() {
			continue
		}
		dirPath := filepath.Join(p.baseDir, projectDir.Name())
		files, err := os.ReadDir(dirPath)
		if err != nil {
			continue
		}

		for _, f := range files {
			if f.IsDir() || filepath.Ext(f.Name()) != ".jsonl" {
				continue
			}
			fullPath := filepath.Join(dirPath, f.Name())
			thread, err := p.parseMetadata(fullPath)
			if err != nil {
				continue
			}
			threads = append(threads, thread)
		}
	}

	return threads, nil
}

func (p *Provider) parseMetadata(path string) (domain.Thread, error) {
	file, err := os.Open(path)
	if err != nil {
		return domain.Thread{}, err
	}
	defer file.Close()

	sessionID := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	var (
		customTitle   string
		aiTitle       string
		firstMsg      string
		workspacePath string
		createdAt     time.Time
		lastSyncedAt  time.Time
	)

	scanner := newScanner(file)
	for scanner.Scan() {
		line := scanner.Bytes()

		var base baseEntry
		if err := json.Unmarshal(line, &base); err != nil {
			continue
		}

		switch base.Type {
		case "permission-mode":
			var e permissionModeEntry
			if err := json.Unmarshal(line, &e); err == nil {
				sessionID = e.SessionID
			}

		case "custom-title":
			var e customTitleEntry
			if err := json.Unmarshal(line, &e); err == nil && e.CustomTitle != "" {
				customTitle = e.CustomTitle
			}

		case "ai-title":
			var e aiTitleEntry
			if err := json.Unmarshal(line, &e); err == nil {
				aiTitle = e.AITitle
			}

		case "user":
			var e userEntry
			if err := json.Unmarshal(line, &e); err == nil {
				if !e.IsMeta {
					if workspacePath == "" && e.CWD != "" {
						workspacePath = e.CWD
					}
					if firstMsg == "" {
						if c := extractUserContent(e.Message.Content); c != "" && !strings.HasPrefix(c, "<") {
							firstMsg = c
						}
					}
				}
				trackTimestamp(e.Timestamp, &createdAt, &lastSyncedAt)
			}

		default:
			var e timestampEntry
			if err := json.Unmarshal(line, &e); err == nil && e.Timestamp != "" {
				if ts, err := time.Parse(time.RFC3339, e.Timestamp); err == nil {
					if ts.After(lastSyncedAt) {
						lastSyncedAt = ts
					}
				}
			}
		}
	}

	title := customTitle
	if title == "" {
		title = aiTitle
	}
	if title == "" {
		title = truncate(firstMsg, 50)
	}

	return domain.Thread{
		ID:             fmt.Sprintf("claude-%s", sessionID),
		Provider:       "claude",
		OriginalID:     sessionID,
		Title:          title,
		WorkspacePath:  workspacePath,
		SourceFilePath: path,
		CreatedAt:      createdAt,
		LastSyncedAt:   lastSyncedAt,
	}, nil
}

func (p *Provider) GetThreadDetails(ctx context.Context, t domain.Thread) (domain.Thread, error) {
	file, err := os.Open(t.SourceFilePath)
	if err != nil {
		return t, err
	}
	defer file.Close()

	type msgEntry struct {
		msg   domain.Message
		order int
	}
	seen := make(map[string]msgEntry)
	order := 0

	scanner := newScanner(file)
	for scanner.Scan() {
		line := scanner.Bytes()

		var base baseEntry
		if err := json.Unmarshal(line, &base); err != nil {
			continue
		}

		var msg domain.Message
		switch base.Type {
		case "user":
			var e userEntry
			if err := json.Unmarshal(line, &e); err != nil || e.IsMeta {
				continue
			}
			content := extractUserContent(e.Message.Content)
			if content == "" || strings.HasPrefix(content, "<") {
				continue
			}
			ts, _ := time.Parse(time.RFC3339, e.Timestamp)
			msg = domain.Message{ID: e.UUID, Role: domain.RoleUser, Content: content, Timestamp: ts}

		case "assistant":
			var e assistantEntry
			if err := json.Unmarshal(line, &e); err != nil {
				continue
			}
			content := extractAssistantContent(e.Message.Content)
			if content == "" {
				continue
			}
			ts, _ := time.Parse(time.RFC3339, e.Timestamp)
			msg = domain.Message{ID: e.Message.ID, Role: domain.RoleAssistant, Content: content, Timestamp: ts}

		default:
			continue
		}

		e, exists := seen[msg.ID]
		if !exists {
			e.order = order
			order++
		}
		e.msg = msg
		seen[msg.ID] = e
	}

	messages := make([]domain.Message, len(seen))
	for _, e := range seen {
		messages[e.order] = e.msg
	}

	t.Messages = messages
	return t, nil
}

func extractUserContent(raw any) string {
	switch v := raw.(type) {
	case string:
		return v
	case []any:
		var sb strings.Builder
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				if text, ok := m["text"].(string); ok {
					sb.WriteString(text)
				}
			}
		}
		return sb.String()
	}
	return ""
}

func extractAssistantContent(blocks []contentBlock) string {
	var sb strings.Builder
	for _, block := range blocks {
		if block.Type == "text" {
			sb.WriteString(block.Text)
		}
	}
	return sb.String()
}

func trackTimestamp(raw string, createdAt, lastSyncedAt *time.Time) {
	ts, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return
	}
	if createdAt.IsZero() {
		*createdAt = ts
	}
	if ts.After(*lastSyncedAt) {
		*lastSyncedAt = ts
	}
}

func (p *Provider) IngestFromHook(ctx context.Context, payload ports.HookPayload) (domain.Thread, error) {
	path := payload.TranscriptPath
	if path == "" {
		if payload.SessionID == "" {
			return domain.Thread{}, fmt.Errorf("hook payload missing transcript_path and session_id")
		}
		var err error
		path, err = p.findSessionFile(payload.SessionID)
		if err != nil {
			return domain.Thread{}, err
		}
	}
	t, err := p.parseMetadata(path)
	if err != nil {
		return domain.Thread{}, err
	}
	return p.GetThreadDetails(ctx, t)
}

func (p *Provider) findSessionFile(sessionID string) (string, error) {
	dirs, err := os.ReadDir(p.baseDir)
	if err != nil {
		return "", fmt.Errorf("read claude projects dir: %w", err)
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		candidate := filepath.Join(p.baseDir, d.Name(), sessionID+".jsonl")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("session file not found for session_id: %s", sessionID)
}

func newScanner(f *os.File) *bufio.Scanner {
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 2*1024*1024), 2*1024*1024)
	return s
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
