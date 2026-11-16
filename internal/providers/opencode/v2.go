package opencode

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/zuhailkhan/threadman/internal/domain"
)

// OpenCode 2.x moved sessions from the session, message and part tables to
// session_v2 and session_message. Upgraded databases keep the old tables with
// stale data and new databases don't have them at all, so session_v2 decides
// which schema is live.
const (
	sessionTableV1 = "session"
	sessionTableV2 = "session_v2"
)

func sessionTable(ctx context.Context, db *sql.DB) (string, error) {
	var name string
	err := db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`,
		sessionTableV2,
	).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return sessionTableV1, nil
	}
	if err != nil {
		return "", fmt.Errorf("detect opencode schema: %w", err)
	}
	return sessionTableV2, nil
}

type userMessageV2 struct {
	Text string `json:"text"`
}

type assistantMessageV2 struct {
	Content []contentBlockV2 `json:"content"`
}

type contentBlockV2 struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (p *Provider) threadDetailsV2(ctx context.Context, db *sql.DB, t domain.Thread) (domain.Thread, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, type, time_created, data
		FROM session_message
		WHERE session_id = ? AND type IN ('user', 'assistant')
		ORDER BY seq
	`, t.OriginalID)
	if err != nil {
		return t, err
	}
	defer rows.Close()

	var messages []domain.Message
	for rows.Next() {
		var (
			id            string
			msgType       string
			timeCreatedMs int64
			data          string
		)
		if err := rows.Scan(&id, &msgType, &timeCreatedMs, &data); err != nil {
			continue
		}

		role, content := parseMessageV2(msgType, data)
		if content == "" {
			continue
		}
		messages = append(messages, domain.Message{
			ID:        id,
			Role:      role,
			Content:   content,
			Timestamp: time.UnixMilli(timeCreatedMs),
		})
	}
	if err := rows.Err(); err != nil {
		return t, err
	}

	t.Messages = messages
	// session_v2.title is nullable, unlike the v1 column.
	if t.Title == "" && len(messages) > 0 {
		t.Title = titleFromMessage(messages[0].Content)
	}
	return t, nil
}

func parseMessageV2(msgType, data string) (domain.Role, string) {
	if msgType == "user" {
		var m userMessageV2
		if err := json.Unmarshal([]byte(data), &m); err != nil {
			return domain.RoleUser, ""
		}
		return domain.RoleUser, m.Text
	}

	var m assistantMessageV2
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		return domain.RoleAssistant, ""
	}
	var sb strings.Builder
	for _, block := range m.Content {
		if block.Type == "text" {
			sb.WriteString(block.Text)
		}
	}
	return domain.RoleAssistant, sb.String()
}

func titleFromMessage(content string) string {
	title := strings.ReplaceAll(content, "\n", " ")
	if runes := []rune(title); len(runes) > 50 {
		return string(runes[:47]) + "..."
	}
	return title
}

// importDocument is the file format of `opencode session import`. OpenCode
// rejects documents without projectID, cost, tokens and an assistant model, so
// those are written as placeholders. The real project comes from --directory.
//
// Model and Agent decide what the session runs with once it is resumed. Without
// them OpenCode falls back to its default model, not the one the user works with.
type importDocument struct {
	Info     importInfo      `json:"info"`
	Messages []importMessage `json:"messages"`
}

type importInfo struct {
	ID        string          `json:"id"`
	ProjectID string          `json:"projectID"`
	Title     string          `json:"title"`
	Location  importLocation  `json:"location"`
	Time      importTime      `json:"time"`
	Cost      float64         `json:"cost"`
	Tokens    importTokens    `json:"tokens"`
	Model     json.RawMessage `json:"model,omitempty"`
	Agent     string          `json:"agent,omitempty"`
}

type importLocation struct {
	Directory string `json:"directory"`
}

type importTime struct {
	Created int64 `json:"created"`
	Updated int64 `json:"updated"`
}

type importTokens struct {
	Input     int         `json:"input"`
	Output    int         `json:"output"`
	Reasoning int         `json:"reasoning"`
	Cache     importCache `json:"cache"`
}

type importCache struct {
	Read  int `json:"read"`
	Write int `json:"write"`
}

type importModel struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerID"`
	Variant    string `json:"variant"`
}

type importMessage struct {
	ID      string           `json:"id"`
	Type    string           `json:"type"`
	Time    importMsgTime    `json:"time"`
	Text    string           `json:"text,omitempty"`
	Files   []any            `json:"files,omitempty"`
	Agents  []any            `json:"agents,omitempty"`
	Agent   string           `json:"agent,omitempty"`
	Content []contentBlockV2 `json:"content,omitempty"`
	Finish  string           `json:"finish,omitempty"`
	Model   *importModel     `json:"model,omitempty"`
	Cost    *float64         `json:"cost,omitempty"`
	Tokens  *importTokens    `json:"tokens,omitempty"`
}

type importMsgTime struct {
	Created   int64 `json:"created"`
	Completed int64 `json:"completed,omitempty"`
}

func newImportDocument(thread domain.Thread, sessionID string, now time.Time) importDocument {
	title := thread.Title
	if title == "" {
		title = "(imported)"
	}

	doc := importDocument{
		Info: importInfo{
			ID:        sessionID,
			ProjectID: "global",
			Title:     title,
			Location:  importLocation{Directory: thread.WorkspacePath},
			Time:      importTime{Created: now.UnixMilli(), Updated: now.UnixMilli()},
		},
		Messages: make([]importMessage, 0, len(thread.Messages)),
	}

	doc.Messages = toImportMessages(thread.Messages, now, 0)
	return doc
}

// toImportMessages converts messages to the import format, each dated after
// the one before it and after `after`, the newest message already in the
// session.
func toImportMessages(messages []domain.Message, now time.Time, after int64) []importMessage {
	out := make([]importMessage, 0, len(messages))
	for i, msg := range messages {
		ts := msg.Timestamp
		if ts.IsZero() {
			ts = now
		}
		// Offset by index so messages sharing a timestamp stay in order.
		created := max(ts.UnixMilli(), after+1) + int64(i)
		id := "msg_" + strings.ReplaceAll(uuid.New().String(), "-", "")

		if msg.Role == domain.RoleAssistant {
			out = append(out, importMessage{
				ID:      id,
				Type:    "assistant",
				Time:    importMsgTime{Created: created, Completed: created},
				Agent:   "build",
				Content: []contentBlockV2{{Type: "text", Text: msg.Content}},
				Finish:  "stop",
				Model:   &importModel{Variant: "default"},
				Cost:    new(float64),
				Tokens:  &importTokens{},
			})
			continue
		}
		out = append(out, importMessage{
			ID:     id,
			Type:   "user",
			Time:   importMsgTime{Created: created},
			Text:   msg.Content,
			Files:  []any{},
			Agents: []any{},
		})
	}
	return out
}

// latestModel returns the model and agent of the most recently used session.
func latestModel(ctx context.Context, db *sql.DB) (json.RawMessage, string, error) {
	var model, agent sql.NullString
	err := db.QueryRowContext(ctx, `
		SELECT model, agent
		FROM session_v2
		WHERE model IS NOT NULL
		ORDER BY time_updated DESC
		LIMIT 1
	`).Scan(&model, &agent)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("read latest opencode model: %w", err)
	}
	return json.RawMessage(model.String), agent.String, nil
}

func (w *Writer) importThread(ctx context.Context, db *sql.DB, thread domain.Thread) (string, error) {
	sessionID := "ses_" + strings.ReplaceAll(uuid.New().String(), "-", "")

	doc := newImportDocument(thread, sessionID, time.Now())
	model, agent, err := latestModel(ctx, db)
	if err != nil {
		return "", err
	}
	doc.Info.Model, doc.Info.Agent = model, agent

	data, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}

	if err := w.importFile(ctx, thread.WorkspacePath, data); err != nil {
		return "", err
	}
	return sessionID, nil
}

// appendV2 adds messages to an existing 2.x session. OpenCode has no command
// for that, and import refuses a session ID that already exists, so the
// session is exported, deleted and imported again under the same ID with the
// messages added. The export is kept as a backup first; if the import fails or
// loses messages, the backup is imported back.
func (w *Writer) appendV2(ctx context.Context, db *sql.DB, target domain.Thread, messages []domain.Message) error {
	id := target.OriginalID

	// Deleting a session also deletes its subagent sessions, which the
	// export doesn't contain.
	var children int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM session_v2 WHERE parent_id = ?`, id).Scan(&children); err != nil {
		return fmt.Errorf("check subagent sessions: %w", err)
	}
	if children > 0 {
		return fmt.Errorf("opencode session %s has %d subagent session(s); appending to it means re-importing it, which would delete them", id, children)
	}

	original, err := w.exportSession(ctx, id)
	if err != nil {
		return err
	}
	backup, err := w.saveBackup(id, original)
	if err != nil {
		return err
	}

	var doc struct {
		Info     map[string]json.RawMessage `json:"info"`
		Messages []json.RawMessage          `json:"messages"`
	}
	if err := json.Unmarshal(original, &doc); err != nil {
		return fmt.Errorf("read exported session: %w", err)
	}
	before := len(doc.Messages)

	var newest int64
	for _, raw := range doc.Messages {
		var m struct {
			Time importMsgTime `json:"time"`
		}
		if json.Unmarshal(raw, &m) == nil && m.Time.Created > newest {
			newest = m.Time.Created
		}
	}
	for _, m := range toImportMessages(messages, time.Now(), newest) {
		raw, err := json.Marshal(m)
		if err != nil {
			return err
		}
		doc.Messages = append(doc.Messages, raw)
	}
	updated, err := json.Marshal(doc)
	if err != nil {
		return err
	}

	directory := target.WorkspacePath
	var loc importLocation
	if json.Unmarshal(doc.Info["location"], &loc) == nil && loc.Directory != "" {
		directory = loc.Directory
	}

	if err := w.deleteSessionV2(ctx, id); err != nil {
		return err
	}
	if err := w.importFile(ctx, directory, updated); err != nil {
		return w.restore(ctx, id, directory, original, backup, err)
	}

	got, err := w.exportSession(ctx, id)
	if err == nil {
		var check struct {
			Messages []json.RawMessage `json:"messages"`
		}
		if err = json.Unmarshal(got, &check); err == nil && len(check.Messages) != before+len(messages) {
			err = fmt.Errorf("re-imported session has %d messages, want %d", len(check.Messages), before+len(messages))
		}
	}
	if err != nil {
		return w.restore(ctx, id, directory, original, backup, err)
	}
	return nil
}

// restore puts the original session back after a failed append.
func (w *Writer) restore(ctx context.Context, id, directory string, original []byte, backup string, cause error) error {
	_ = w.deleteSessionV2(ctx, id)
	if err := w.importFile(ctx, directory, original); err != nil {
		return fmt.Errorf("append to opencode session %s failed (%v) and restoring it failed too (%v); the original is saved at %s", id, cause, err, backup)
	}
	return fmt.Errorf("append to opencode session %s failed, so it was restored unchanged: %w", id, cause)
}

// exportSession returns `opencode session export` output: the session as an
// import document.
func (w *Writer) exportSession(ctx context.Context, id string) ([]byte, error) {
	out, err := w.runOpencode(ctx, "session", "export", id)
	if err != nil {
		return nil, fmt.Errorf("opencode session export: %w: %s", err, firstLine(out))
	}
	// The CLI can print log lines around the JSON; keep the document only.
	start := bytes.IndexByte(out, '{')
	if start < 0 {
		return nil, fmt.Errorf("opencode session export: no session in output: %s", firstLine(out))
	}
	var doc json.RawMessage
	if err := json.NewDecoder(bytes.NewReader(out[start:])).Decode(&doc); err != nil {
		return nil, fmt.Errorf("opencode session export: %w", err)
	}
	return doc, nil
}

func (w *Writer) saveBackup(id string, data []byte) (string, error) {
	dir := w.backupDir
	if dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-%d.json", id, time.Now().UnixMilli()))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("back up session: %w", err)
	}
	return path, nil
}

// importFile runs `opencode session import` on a document. Import exits 0
// when it refuses a session that already exists, so that is checked too.
func (w *Writer) importFile(ctx context.Context, directory string, data []byte) error {
	f, err := os.CreateTemp("", "threadman-opencode-*.json")
	if err != nil {
		return fmt.Errorf("create import file: %w", err)
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("write import file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write import file: %w", err)
	}

	out, err := w.runOpencode(ctx, "session", "import", "--directory", directory, f.Name())
	if err != nil {
		return fmt.Errorf("opencode session import: %w: %s", err, firstLine(out))
	}
	if strings.Contains(string(out), "already exists") {
		return fmt.Errorf("opencode session import: %s", firstLine(out))
	}
	return nil
}

func (w *Writer) deleteSessionV2(ctx context.Context, sessionID string) error {
	out, err := w.runOpencode(ctx, "session", "delete", sessionID)
	if err == nil {
		return nil
	}
	// Deleting is idempotent, matching the v1 path.
	if strings.Contains(string(out), "Session not found") {
		return nil
	}
	return fmt.Errorf("opencode session delete: %w: %s", err, firstLine(out))
}

func firstLine(out []byte) string {
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(line)
}
