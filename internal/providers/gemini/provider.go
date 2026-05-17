package gemini

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
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

type sessionMetadata struct {
	SessionID   string    `json:"sessionId"`
	StartTime   time.Time `json:"startTime"`
	LastUpdated time.Time `json:"lastUpdated"`
}

type logEntry struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Content   any    `json:"content"`
}

func (p *Provider) Name() string {
	return "gemini"
}

func NewProvider() *Provider {
	home, _ := os.UserHomeDir()
	return &Provider{
		baseDir: filepath.Join(home, ".gemini", "tmp"),
	}
}

func (p *Provider) DiscoverThreads(ctx context.Context) ([]domain.Thread, error) {
	threads := []domain.Thread{}

	project, projectDirectoryReadError := os.ReadDir(p.baseDir)
	if projectDirectoryReadError != nil {
		return nil, projectDirectoryReadError
	}

	for _, entry := range project {
		if !entry.IsDir() {
			continue
		}
		chatsPath := filepath.Join(p.baseDir, entry.Name(), "chats")
		chats, chatsDirectoryReadError := os.ReadDir(chatsPath)
		if chatsDirectoryReadError != nil {
			continue
		}

		for _, chatFile := range chats {
			if chatFile.IsDir() || filepath.Ext(chatFile.Name()) != ".jsonl" {
				continue
			}
			fullPath := filepath.Join(chatsPath, chatFile.Name())
			thread, threadParseError := p.parseMetadata(fullPath, entry.Name())
			if threadParseError != nil {
				continue
			}

			threads = append(threads, thread)
		}
	}

	return threads, nil
}

func (p *Provider) parseMetadata(path string, projectName string) (domain.Thread, error) {
	file, fileOpenError := os.Open(path)
	if fileOpenError != nil {
		return domain.Thread{}, fileOpenError
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		return domain.Thread{}, scanner.Err()
	}

	var meta sessionMetadata

	if err := json.Unmarshal(scanner.Bytes(), &meta); err != nil {
		return domain.Thread{}, err
	}

	workspacePath, err := url.PathUnescape(projectName)
	if err != nil {
		workspacePath = projectName
	}

	return domain.Thread{
		ID:             fmt.Sprintf("gemini-%s", meta.SessionID),
		Provider:       "gemini",
		OriginalID:     meta.SessionID,
		WorkspacePath:  workspacePath,
		SourceFilePath: path,
		CreatedAt:      meta.StartTime,
		LastSyncedAt:   meta.LastUpdated,
	}, nil
}

func (p *Provider) IngestFromHook(ctx context.Context, payload ports.HookPayload) (domain.Thread, error) {
	if payload.TranscriptPath == "" {
		return domain.Thread{}, fmt.Errorf("gemini hook payload missing transcript_path")
	}
	projectName := filepath.Base(filepath.Dir(filepath.Dir(payload.TranscriptPath)))
	t, err := p.parseMetadata(payload.TranscriptPath, projectName)
	if err != nil {
		return domain.Thread{}, err
	}
	return p.GetThreadDetails(ctx, t)
}

func (p *Provider) GetThreadDetails(ctx context.Context, t domain.Thread) (domain.Thread, error) {
	file, err := os.Open(t.SourceFilePath)
	if err != nil {
		return t, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	if scanner.Scan() {
	}

	// Gemini streams partial updates: the same message ID can appear multiple
	// times with growing content. We keep the last occurrence per ID.
	type msgEntry struct {
		msg   domain.Message
		order int
	}
	seen := make(map[string]msgEntry)
	order := 0

	for scanner.Scan() {
		var entry logEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}

		if entry.Type != "user" && entry.Type != "gemini" {
			continue
		}

		role := domain.RoleUser
		if entry.Type == "gemini" {
			role = domain.RoleAssistant
		}

		content := ""
		switch c := entry.Content.(type) {
		case string:
			content = c
		case []any:
			for _, item := range c {
				if m, ok := item.(map[string]any); ok {
					if text, ok := m["text"].(string); ok {
						content += text
					}
				}
			}
		}

		if content == "" {
			continue
		}

		ts, _ := time.Parse(time.RFC3339, entry.Timestamp)

		e, exists := seen[entry.ID]
		if !exists {
			e.order = order
			order++
		}
		e.msg = domain.Message{
			ID:        entry.ID,
			Role:      role,
			Content:   content,
			Timestamp: ts,
		}
		seen[entry.ID] = e
	}

	messages := make([]domain.Message, len(seen))
	for _, e := range seen {
		messages[e.order] = e.msg
	}

	t.Messages = messages
	if len(messages) > 0 {
		t.Title = messages[0].Content
		if len(t.Title) > 50 {
			t.Title = strings.ReplaceAll(t.Title, "\n", " ")
			if len(t.Title) > 50 {
				t.Title = t.Title[:47] + "..."
			}
		}
	}

	return t, nil
}
