package opencode

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

func (w *Writer) WriteThread(ctx context.Context, thread domain.Thread) (string, error) {
	db, err := sql.Open("sqlite3", w.dbPath)
	if err != nil {
		return "", fmt.Errorf("open db: %w", err)
	}
	defer db.Close()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	sessionID := uuid.New().String()
	now := time.Now().UnixMilli()

	title := thread.Title
	if title == "" {
		title = "(imported)"
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO session (id, title, directory, time_created, time_updated) VALUES (?, ?, ?, ?, ?)`,
		sessionID, title, thread.WorkspacePath, now, now,
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

		role := "user"
		if msg.Role == domain.RoleAssistant {
			role = "assistant"
		}
		msgData, _ := json.Marshal(map[string]string{"role": role})

		_, err = tx.ExecContext(ctx,
			`INSERT INTO message (id, session_id, data, time_created) VALUES (?, ?, ?, ?)`,
			msgID, sessionID, string(msgData), msgCreated,
		)
		if err != nil {
			return "", fmt.Errorf("insert message: %w", err)
		}

		partData, _ := json.Marshal(map[string]string{"type": "text", "text": msg.Content})
		_, err = tx.ExecContext(ctx,
			`INSERT INTO part (id, message_id, session_id, data, time_created) VALUES (?, ?, ?, ?, ?)`,
			uuid.New().String(), msgID, sessionID, string(partData), msgCreated,
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
	cmd := exec.Command("opencode")
	cmd.Dir = workspacePath
	return cmd
}
