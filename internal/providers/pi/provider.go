// Package pi reads sessions written by the pi coding agent
// (@earendil-works/pi-coding-agent). Sessions are JSONL files whose entries
// form a tree via id/parentId; see pi's docs/session-format.md.
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

type Provider struct {
	baseDir string
}

func NewProvider() *Provider {
	return &Provider{baseDir: sessionsDir()}
}

// sessionsDir mirrors pi's own resolution: PI_CODING_AGENT_SESSION_DIR, then
// <agent dir>/sessions where the agent dir is PI_CODING_AGENT_DIR or ~/.pi/agent.
func sessionsDir() string {
	if d := os.Getenv("PI_CODING_AGENT_SESSION_DIR"); d != "" {
		return d
	}
	agentDir := os.Getenv("PI_CODING_AGENT_DIR")
	if agentDir == "" {
		home, _ := os.UserHomeDir()
		agentDir = filepath.Join(home, ".pi", "agent")
	}
	return filepath.Join(agentDir, "sessions")
}

func (p *Provider) Name() string { return providerName }

type entry struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	ParentID  *string         `json:"parentId"`
	Timestamp string          `json:"timestamp"`
	CWD       string          `json:"cwd"`
	Name      string          `json:"name"`
	Message   json.RawMessage `json:"message"`
}

type message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
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
		dir := filepath.Join(p.baseDir, d.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || filepath.Ext(f.Name()) != ".jsonl" {
				continue
			}
			t, err := p.parse(filepath.Join(dir, f.Name()), false)
			if err != nil {
				continue
			}
			threads = append(threads, t)
		}
	}
	return threads, nil
}

func (p *Provider) GetThreadDetails(ctx context.Context, t domain.Thread) (domain.Thread, error) {
	full, err := p.parse(t.SourceFilePath, true)
	if err != nil {
		return t, err
	}
	t.Messages = full.Messages
	return t, nil
}

// parse reads one session file. The visible conversation is the active branch:
// the chain of entries from the last entry in the file back to the root.
// Abandoned branches stay in the file but are not shown.
func (p *Provider) parse(path string, withMessages bool) (domain.Thread, error) {
	f, err := os.Open(path)
	if err != nil {
		return domain.Thread{}, err
	}
	defer f.Close()

	var header entry
	byID := map[string]entry{}
	var leaf string
	var sessionName string

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 4*1024*1024), 64*1024*1024)
	first := true
	for sc.Scan() {
		var e entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			continue
		}
		if first {
			first = false
			if e.Type != "session" || e.ID == "" {
				return domain.Thread{}, fmt.Errorf("not a pi session: %s", path)
			}
			header = e
			continue
		}
		if e.ID == "" {
			continue
		}
		byID[e.ID] = e
		leaf = e.ID
		if e.Type == "session_info" && e.Name != "" {
			sessionName = e.Name
		}
	}
	if err := sc.Err(); err != nil {
		return domain.Thread{}, err
	}
	if header.ID == "" {
		return domain.Thread{}, fmt.Errorf("empty pi session: %s", path)
	}

	var chain []entry
	seen := map[string]bool{}
	for id := leaf; id != "" && !seen[id]; {
		seen[id] = true
		e, ok := byID[id]
		if !ok {
			break
		}
		chain = append(chain, e)
		if e.ParentID == nil {
			break
		}
		id = *e.ParentID
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}

	createdAt := parseTime(header.Timestamp)
	lastAt := createdAt
	var messages []domain.Message
	firstUser := ""
	for _, e := range chain {
		if ts := parseTime(e.Timestamp); ts.After(lastAt) {
			lastAt = ts
		}
		if e.Type != "message" {
			continue
		}
		var m message
		if err := json.Unmarshal(e.Message, &m); err != nil {
			continue
		}
		role, text := domain.Role(""), ""
		switch m.Role {
		case "user":
			role, text = domain.RoleUser, extractText(m.Content)
		case "assistant":
			role, text = domain.RoleAssistant, extractText(m.Content)
		default:
			continue
		}
		if text == "" {
			continue
		}
		if role == domain.RoleUser && firstUser == "" {
			firstUser = text
		}
		if withMessages {
			messages = append(messages, domain.Message{
				ID: e.ID, Role: role, Content: text, Timestamp: parseTime(e.Timestamp),
			})
		}
	}

	title := sessionName
	if title == "" {
		title = truncate(firstUser, 50)
	}

	return domain.Thread{
		ID:             providerName + "-" + header.ID,
		Provider:       providerName,
		OriginalID:     header.ID,
		Title:          title,
		WorkspacePath:  header.CWD,
		SourceFilePath: path,
		Messages:       messages,
		CreatedAt:      createdAt,
		LastSyncedAt:   lastAt,
	}, nil
}

// extractText flattens message content, which is either a plain string or a
// list of blocks. Only text blocks are kept; thinking and tool calls are not.
func extractText(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var sb strings.Builder
	for _, b := range blocks {
		if b.Type == "text" {
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-3]) + "..."
}
