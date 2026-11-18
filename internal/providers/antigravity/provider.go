// Package antigravity reads conversations written by the Antigravity CLI
// (agy). Each conversation is its own SQLite database of "steps" whose payloads
// are protobuf messages. Google publishes no schema for them, so the field
// numbers used here were worked out from real conversation files and may need
// revisiting when the CLI changes its format.
package antigravity

import (
	"context"
	"database/sql"
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
	_ "modernc.org/sqlite"
)

const providerName = "antigravity"

// Step types, from the steps.step_type column.
const (
	stepUserInput       = 14
	stepPlannerResponse = 15
)

// Protobuf field numbers inside steps.step_payload.
const (
	fieldStepMetadata    = 5  // step: metadata message
	fieldMetaCreatedAt   = 1  // metadata: timestamp {1: seconds, 2: nanos}
	fieldUserInput       = 19 // step: user input message
	fieldUserText        = 2  // user input: the text as typed
	fieldPlannerResponse = 20 // step: model response message
	fieldResponseText    = 1  // response: the reply shown to the user
)

type Provider struct {
	baseDir string
}

func NewProvider() *Provider {
	return &Provider{baseDir: dataDir()}
}

func dataDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gemini", "antigravity-cli")
}

func (p *Provider) Name() string { return providerName }

func (p *Provider) conversationsDir() string {
	return filepath.Join(p.baseDir, "conversations")
}

func (p *Provider) conversationPath(id string) string {
	return filepath.Join(p.conversationsDir(), id+".db")
}

func openReadOnly(path string) (*sql.DB, error) {
	return sql.Open("sqlite", "file:"+path+"?mode=ro")
}

// summary is what the CLI's own conversation index knows about a conversation.
type summary struct {
	title     string
	workspace string
	modified  time.Time
	child     bool // spawned by another conversation, e.g. a subagent
}

// loadSummaries reads conversation_summaries.db. It is only an index: when it
// is missing or unreadable, conversations are still imported, without a title
// or workspace.
func (p *Provider) loadSummaries(ctx context.Context) map[string]summary {
	out := map[string]summary{}
	path := filepath.Join(p.baseDir, "conversation_summaries.db")
	if _, err := os.Stat(path); err != nil {
		return out
	}
	db, err := openReadOnly(path)
	if err != nil {
		return out
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, `
		SELECT conversation_id, title, workspace_uris, parent_conversation_id, last_modified_time
		FROM conversation_summaries
	`)
	if err != nil {
		return out
	}
	defer rows.Close()

	for rows.Next() {
		var (
			id, title, uris, parent string
			modified                any
		)
		if err := rows.Scan(&id, &title, &uris, &parent, &modified); err != nil {
			continue
		}
		s := summary{title: title, workspace: firstWorkspace(uris), child: parent != ""}
		switch v := modified.(type) {
		case time.Time:
			s.modified = v
		case string:
			s.modified, _ = time.Parse("2006-01-02 15:04:05.999999999-07:00", v)
		}
		out[id] = s
	}
	return out
}

// firstWorkspace turns the first entry of a JSON list of file:// URIs into a
// local path.
func firstWorkspace(urisJSON string) string {
	var uris []string
	if err := json.Unmarshal([]byte(urisJSON), &uris); err != nil || len(uris) == 0 {
		return ""
	}
	u, err := url.Parse(uris[0])
	if err != nil || u.Scheme != "file" {
		return ""
	}
	path := u.Path
	// file:///C:/Users/me parses to "/C:/Users/me".
	if len(path) >= 3 && path[0] == '/' && path[2] == ':' {
		path = path[1:]
	}
	return filepath.FromSlash(path)
}

func (p *Provider) DiscoverThreads(ctx context.Context) ([]domain.Thread, error) {
	files, err := os.ReadDir(p.conversationsDir())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ports.ErrSourceUnavailable, p.conversationsDir())
		}
		return nil, err
	}

	summaries := p.loadSummaries(ctx)
	threads := []domain.Thread{}
	for _, f := range files {
		if f.IsDir() || filepath.Ext(f.Name()) != ".db" {
			continue
		}
		id := strings.TrimSuffix(f.Name(), ".db")
		s := summaries[id]
		if s.child {
			continue
		}
		t, err := p.read(ctx, id, s, false)
		if err != nil {
			continue
		}
		threads = append(threads, t)
	}
	return threads, nil
}

func (p *Provider) GetThreadDetails(ctx context.Context, t domain.Thread) (domain.Thread, error) {
	full, err := p.read(ctx, t.OriginalID, summary{}, true)
	if err != nil {
		return t, err
	}
	t.Messages = full.Messages
	return t, nil
}

func (p *Provider) read(ctx context.Context, id string, s summary, withMessages bool) (domain.Thread, error) {
	path := p.conversationPath(id)
	if _, err := os.Stat(path); err != nil {
		return domain.Thread{}, err
	}
	db, err := openReadOnly(path)
	if err != nil {
		return domain.Thread{}, err
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, `SELECT idx, step_type, step_payload FROM steps ORDER BY idx`)
	if err != nil {
		return domain.Thread{}, err
	}
	defer rows.Close()

	var (
		messages  []domain.Message
		firstUser string
		createdAt time.Time
		lastAt    time.Time
	)
	for rows.Next() {
		var (
			idx, stepType int
			payload       []byte
		)
		if err := rows.Scan(&idx, &stepType, &payload); err != nil {
			continue
		}
		ts := stepTime(payload)
		if !ts.IsZero() {
			if createdAt.IsZero() {
				createdAt = ts
			}
			if ts.After(lastAt) {
				lastAt = ts
			}
		}

		var role domain.Role
		var text string
		switch stepType {
		case stepUserInput:
			role, text = domain.RoleUser, pbString(payload, fieldUserInput, fieldUserText)
		case stepPlannerResponse:
			// A response that only calls tools has no reply text and is skipped.
			role, text = domain.RoleAssistant, pbString(payload, fieldPlannerResponse, fieldResponseText)
		default:
			continue
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		if role == domain.RoleUser && firstUser == "" {
			firstUser = text
		}
		if withMessages {
			messages = append(messages, domain.Message{
				ID: fmt.Sprintf("step-%d", idx), Role: role, Content: text, Timestamp: ts,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return domain.Thread{}, err
	}

	title := s.title
	if title == "" {
		title = truncate(firstUser, 50)
	}
	if s.modified.After(lastAt) {
		lastAt = s.modified
	}

	return domain.Thread{
		ID:             providerName + "-" + id,
		Provider:       providerName,
		OriginalID:     id,
		Title:          title,
		WorkspacePath:  s.workspace,
		SourceFilePath: path,
		Messages:       messages,
		CreatedAt:      createdAt,
		LastSyncedAt:   lastAt,
	}, nil
}

func stepTime(payload []byte) time.Time {
	ts, ok := pbBytes(payload, fieldStepMetadata, fieldMetaCreatedAt)
	if !ok {
		return time.Time{}
	}
	sec, _ := pbVarint(ts, 1)
	nanos, _ := pbVarint(ts, 2)
	if sec == 0 {
		return time.Time{}
	}
	return time.Unix(int64(sec), int64(nanos)).UTC()
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-3]) + "..."
}
