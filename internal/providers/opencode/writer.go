package opencode

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/zuhailkhan/threadman/internal/domain"
)

type Writer struct {
	dbPath string
	// backupDir keeps a copy of each OpenCode 2.x session before it is
	// re-imported to append to it.
	backupDir string
	// run overrides how the opencode CLI is invoked. Nil runs the binary on PATH.
	run func(ctx context.Context, args ...string) ([]byte, error)
}

func NewWriter() *Writer {
	home, _ := os.UserHomeDir()
	dbPath := os.Getenv("OPENCODE_DB_PATH")
	if dbPath == "" {
		dbPath = filepath.Join(home, ".local", "share", "opencode", "opencode.db")
	}
	return &Writer{
		dbPath:    dbPath,
		backupDir: filepath.Join(home, ".local", "share", "threadman", "backups", "opencode"),
	}
}

func (w *Writer) Name() string {
	return "opencode"
}

func (w *Writer) runOpencode(ctx context.Context, args ...string) ([]byte, error) {
	if w.run != nil {
		return w.run(ctx, args...)
	}
	return exec.CommandContext(ctx, "opencode", args...).CombinedOutput()
}

func (w *Writer) resolveProjectID(db *sql.DB, workspacePath string) string {
	var projectID string
	err := db.QueryRow(
		`SELECT id FROM project WHERE worktree = ? LIMIT 1`,
		workspacePath,
	).Scan(&projectID)
	if err == nil {
		return projectID
	}
	return "global"
}

func (w *Writer) resolveVersion(db *sql.DB) string {
	var version string
	err := db.QueryRow(
		`SELECT version FROM session WHERE version != '' ORDER BY time_created DESC LIMIT 1`,
	).Scan(&version)
	if err == nil && version != "" {
		return version
	}
	return ""
}

func (w *Writer) WriteThread(ctx context.Context, thread domain.Thread) (string, error) {
	db, err := sql.Open("sqlite3", w.dbPath)
	if err != nil {
		return "", fmt.Errorf("open db: %w", err)
	}
	defer db.Close()

	table, err := sessionTable(ctx, db)
	if err != nil {
		return "", err
	}
	if table == sessionTableV2 {
		return w.importThread(ctx, db, thread)
	}

	projectID := w.resolveProjectID(db, thread.WorkspacePath)
	version := w.resolveVersion(db)
	slug := fmt.Sprintf("imported-%s", time.Now().Format("20060102-150405"))

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	sessionID := "ses_" + strings.ReplaceAll(uuid.New().String(), "-", "")
	now := time.Now().UnixMilli()

	title := thread.Title
	if title == "" {
		title = "(imported)"
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO session (id, project_id, slug, version, title, directory, time_created, time_updated) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		sessionID, projectID, slug, version, title, thread.WorkspacePath, now, now,
	)
	if err != nil {
		return "", fmt.Errorf("insert session: %w", err)
	}

	if err := insertMessagesV1(ctx, tx, sessionID, thread.WorkspacePath, thread.Messages, 0); err != nil {
		return "", err
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}

	return sessionID, nil
}

// insertMessagesV1 adds messages to a 1.x session, one message row and one
// text part each. OpenCode orders messages by time_created, so each is dated
// after the one before it and after `after`, the session's newest message.
func insertMessagesV1(ctx context.Context, tx *sql.Tx, sessionID, workspace string, messages []domain.Message, after int64) error {
	for i, msg := range messages {
		msgID := uuid.New().String()
		ts := msg.Timestamp
		if ts.IsZero() {
			ts = time.Now()
		}
		// Increment timestamp by message index to ensure stable ordering in the
		// database, as OpenCode relies on time_created for message sequence.
		msgCreated := max(ts.UnixMilli(), after+1) + int64(i)

		var msgData []byte
		if msg.Role == domain.RoleAssistant {
			msgData, _ = json.Marshal(map[string]interface{}{
				"role":       "assistant",
				"mode":       "build",
				"agent":      "build",
				"modelID":    "",
				"providerID": "",
				"path":       map[string]string{"cwd": workspace, "root": workspace},
				"tokens": map[string]interface{}{
					"total":     0,
					"input":     0,
					"output":    0,
					"reasoning": 0,
					"cache":     map[string]int{"write": 0, "read": 0},
				},
				"time":   map[string]int64{"created": msgCreated, "completed": msgCreated},
				"cost":   0,
				"finish": "end-turn",
			})
		} else {
			msgData, _ = json.Marshal(map[string]interface{}{
				"role":    "user",
				"agent":   "build",
				"model":   map[string]string{"providerID": "", "modelID": ""},
				"tools":   map[string]bool{"todowrite": false, "task": false},
				"summary": map[string]interface{}{"diffs": []interface{}{}},
				"time":    map[string]int64{"created": msgCreated},
			})
		}

		_, err := tx.ExecContext(ctx,
			`INSERT INTO message (id, session_id, data, time_created, time_updated) VALUES (?, ?, ?, ?, ?)`,
			msgID, sessionID, string(msgData), msgCreated, msgCreated,
		)
		if err != nil {
			return fmt.Errorf("insert message: %w", err)
		}

		partData, _ := json.Marshal(map[string]string{"type": "text", "text": msg.Content})
		_, err = tx.ExecContext(ctx,
			`INSERT INTO part (id, message_id, session_id, data, time_created, time_updated) VALUES (?, ?, ?, ?, ?, ?)`,
			uuid.New().String(), msgID, sessionID, string(partData), msgCreated, msgCreated,
		)
		if err != nil {
			return fmt.Errorf("insert part: %w", err)
		}
	}
	return nil
}

// AppendMessages adds messages to the end of an existing session, so
// `opencode -s` continues the same session with them in its history.
func (w *Writer) AppendMessages(ctx context.Context, target domain.Thread, messages []domain.Message) error {
	dbPath := target.SourceFilePath
	if dbPath == "" {
		dbPath = w.dbPath
	}
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()

	table, err := sessionTable(ctx, db)
	if err != nil {
		return err
	}
	if table == sessionTableV2 {
		return w.appendV2(ctx, db, target, messages)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	var newest sql.NullInt64
	err = tx.QueryRowContext(ctx,
		`SELECT (SELECT MAX(time_created) FROM message WHERE session_id = s.id) FROM session s WHERE s.id = ?`,
		target.OriginalID,
	).Scan(&newest)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("opencode session %s not found", target.OriginalID)
	}
	if err != nil {
		return fmt.Errorf("read session: %w", err)
	}

	if err := insertMessagesV1(ctx, tx, target.OriginalID, target.WorkspacePath, messages, newest.Int64); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE session SET time_updated = ? WHERE id = ?`, time.Now().UnixMilli(), target.OriginalID); err != nil {
		return fmt.Errorf("update session: %w", err)
	}
	return tx.Commit()
}

func (w *Writer) DeleteThread(ctx context.Context, thread domain.Thread) error {
	db, err := sql.Open("sqlite3", thread.SourceFilePath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()

	table, err := sessionTable(ctx, db)
	if err != nil {
		return err
	}
	if table == sessionTableV2 {
		return w.deleteSessionV2(ctx, thread.OriginalID)
	}

	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return fmt.Errorf("enable foreign keys: %w", err)
	}

	_, err = db.ExecContext(ctx, `DELETE FROM session WHERE id = ?`, thread.OriginalID)
	return err
}

func (w *Writer) OpenCommand(sessionID string, workspacePath string) *exec.Cmd {
	cmd := exec.Command("opencode", "-s", sessionID)
	cmd.Dir = workspacePath
	return cmd
}
