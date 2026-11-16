package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
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
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
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
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ports.ErrSourceUnavailable, p.baseDir)
		}
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

	if err := scanner.Err(); err != nil {
		return domain.Thread{}, fmt.Errorf("read %s: %w", path, err)
	}

	title := customTitle
	if title == "" {
		title = aiTitle
	}
	if title == "" {
		title = truncate(firstMsg, 50)
	}

	if workspacePath == "" {
		projectDir := filepath.Base(filepath.Dir(path))
		if decoded, err := url.PathUnescape(projectDir); err == nil {
			workspacePath = decoded
		} else {
			workspacePath = projectDir
		}
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

	var messages []domain.Message
	// Claude Code writes one assistant message as several entries sharing
	// its message ID, one per content block, so entries are merged by it.
	byID := map[string]int{}
	// Entries already read, in case one is written twice.
	read := map[string]bool{}
	// Where each tool call is, so its result can be attached when it comes
	// back in a later user entry.
	type toolRef struct{ msg, tool int }
	tools := map[string]toolRef{}

	scanner := newScanner(file)
	for scanner.Scan() {
		line := scanner.Bytes()

		var base baseEntry
		if err := json.Unmarshal(line, &base); err != nil {
			continue
		}

		switch base.Type {
		case "user":
			var e userEntry
			if err := json.Unmarshal(line, &e); err != nil {
				continue
			}
			if e.UUID != "" {
				if read[e.UUID] {
					continue
				}
				read[e.UUID] = true
			}
			for _, r := range toolResults(e.Message.Content) {
				if ref, ok := tools[r.id]; ok {
					call := &messages[ref.msg].Tools[ref.tool]
					call.Output = domain.Shorten(r.text, domain.MaxToolOutput)
					call.Error = r.isError
				}
			}

			content := extractUserContent(e.Message.Content)
			if content == "" {
				continue
			}
			role := domain.RoleUser
			if e.IsMeta {
				role = domain.RoleSystem
			} else if strings.HasPrefix(content, "<") {
				continue
			}
			ts, _ := time.Parse(time.RFC3339, e.Timestamp)
			byID[e.UUID] = len(messages)
			messages = append(messages, domain.Message{ID: e.UUID, Role: role, Content: content, Timestamp: ts})

		case "assistant":
			var e assistantEntry
			if err := json.Unmarshal(line, &e); err != nil {
				continue
			}
			if e.UUID != "" {
				if read[e.UUID] {
					continue
				}
				read[e.UUID] = true
			}
			content := extractAssistantContent(e.Message.Content)
			var calls []domain.ToolCall
			var callIDs []string
			for _, b := range e.Message.Content {
				if b.Type == "tool_use" {
					calls = append(calls, domain.ToolCall{Name: b.Name, Input: domain.ToolInput(b.Input)})
					callIDs = append(callIDs, b.ID)
				}
			}
			if content == "" && len(calls) == 0 {
				continue
			}

			pos, ok := byID[e.Message.ID]
			if !ok {
				ts, _ := time.Parse(time.RFC3339, e.Timestamp)
				pos = len(messages)
				byID[e.Message.ID] = pos
				messages = append(messages, domain.Message{ID: e.Message.ID, Role: domain.RoleAssistant, Timestamp: ts})
			}
			m := &messages[pos]
			if content != "" {
				if m.Content != "" {
					m.Content += "\n\n"
				}
				m.Content += content
			}
			for i, call := range calls {
				tools[callIDs[i]] = toolRef{msg: pos, tool: len(m.Tools)}
				m.Tools = append(m.Tools, call)
			}
		}
	}

	// A line that can't be read ends the scan early; storing what was read
	// so far would silently cut the conversation short.
	if err := scanner.Err(); err != nil {
		return t, fmt.Errorf("read %s: %w", t.SourceFilePath, err)
	}

	t.Messages = messages
	return t, nil
}

type toolResult struct {
	id      string
	text    string
	isError bool
}

// toolResults finds the tool_result blocks in a user entry. Their content is
// either a string or a list of blocks, of which the text ones are kept.
func toolResults(raw any) []toolResult {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []toolResult
	for _, item := range items {
		b, ok := item.(map[string]any)
		if !ok || b["type"] != "tool_result" {
			continue
		}
		id, _ := b["tool_use_id"].(string)
		isError, _ := b["is_error"].(bool)
		out = append(out, toolResult{id: id, text: resultText(b["content"]), isError: isError})
	}
	return out
}

func resultText(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, item := range c {
			if b, ok := item.(map[string]any); ok {
				if text, ok := b["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
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

// maxLine is the longest session line read. A line holding a pasted image
// or a large tool result can run to many megabytes.
const maxLine = 64 * 1024 * 1024

func newScanner(f *os.File) *bufio.Scanner {
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 64*1024), maxLine)
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
