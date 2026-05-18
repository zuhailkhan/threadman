package opencode

import (
	"context"
	"database/sql"
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

type Writer struct {
	dbPath string
}

func NewWriter() *Writer {
	dbPath := os.Getenv("OPENCODE_DB_PATH")
	if dbPath == "" {
		home, _ := os.UserHomeDir()
		dbPath = filepath.Join(home, ".local", "share", "opencode", "opencode.db")
	}
	return &Writer{
		dbPath: dbPath,
	}
}

func (w *Writer) Name() string {
	return "opencode"
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

	for i, msg := range thread.Messages {
		msgID := uuid.New().String()
		ts := msg.Timestamp
		if ts.IsZero() {
			ts = time.Now()
		}
		// Increment timestamp by message index to ensure stable ordering in the
		// database, as OpenCode relies on time_created for message sequence.
		msgCreated := ts.UnixMilli() + int64(i)

		var msgData []byte
		if msg.Role == domain.RoleAssistant {
			msgData, _ = json.Marshal(map[string]interface{}{
				"role":       "assistant",
				"mode":       "build",
				"agent":      "build",
				"modelID":    "",
				"providerID": "",
				"path":       map[string]string{"cwd": thread.WorkspacePath, "root": thread.WorkspacePath},
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
				"role":  "user",
				"agent": "build",
				"model": map[string]string{"providerID": "", "modelID": ""},
				"tools": map[string]bool{"todowrite": false, "task": false},
				"summary": map[string]interface{}{"diffs": []interface{}{}},
				"time":  map[string]int64{"created": msgCreated},
			})
		}

		_, err = tx.ExecContext(ctx,
			`INSERT INTO message (id, session_id, data, time_created, time_updated) VALUES (?, ?, ?, ?, ?)`,
			msgID, sessionID, string(msgData), msgCreated, msgCreated,
		)
		if err != nil {
			return "", fmt.Errorf("insert message: %w", err)
		}

		partData, _ := json.Marshal(map[string]string{"type": "text", "text": msg.Content})
		_, err = tx.ExecContext(ctx,
			`INSERT INTO part (id, message_id, session_id, data, time_created, time_updated) VALUES (?, ?, ?, ?, ?, ?)`,
			uuid.New().String(), msgID, sessionID, string(partData), msgCreated, msgCreated,
		)
		if err != nil {
			return "", fmt.Errorf("insert part: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}

	return sessionID, nil
}

func (w *Writer) OpenCommand(sessionID string, workspacePath string) *exec.Cmd {
	cmd := exec.Command("opencode", "-s", sessionID)
	cmd.Dir = workspacePath
	return cmd
}
