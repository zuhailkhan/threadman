package opencode

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/zuhailkhan/threadman/internal/domain"
	"github.com/zuhailkhan/threadman/internal/ports"
)

type Provider struct {
	dbPath string
}

func NewProvider() *Provider {
	home, _ := os.UserHomeDir()
	return &Provider{
		dbPath: filepath.Join(home, ".local", "share", "opencode", "opencode.db"),
	}
}

func (p *Provider) Name() string {
	return "opencode"
}

func (p *Provider) openDB() (*sql.DB, error) {
	return sql.Open("sqlite3", "file:"+p.dbPath+"?mode=ro")
}

func (p *Provider) DiscoverThreads(ctx context.Context) ([]domain.Thread, error) {
	db, err := p.openDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, `
		SELECT id, title, directory, time_created, time_updated
		FROM session
		WHERE parent_id IS NULL
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var threads []domain.Thread
	for rows.Next() {
		var (
			id            string
			title         string
			directory     string
			timeCreatedMs int64
			timeUpdatedMs int64
		)
		if err := rows.Scan(&id, &title, &directory, &timeCreatedMs, &timeUpdatedMs); err != nil {
			continue
		}
		threads = append(threads, domain.Thread{
			ID:             fmt.Sprintf("opencode-%s", id),
			Provider:       "opencode",
			OriginalID:     id,
			Title:          title,
			WorkspacePath:  directory,
			SourceFilePath: p.dbPath,
			CreatedAt:      time.UnixMilli(timeCreatedMs),
			LastSyncedAt:   time.UnixMilli(timeUpdatedMs),
		})
	}

	return threads, rows.Err()
}

func (p *Provider) IngestFromHook(ctx context.Context, payload ports.HookPayload) (domain.Thread, error) {
	if payload.SessionID == "" {
		return domain.Thread{}, fmt.Errorf("opencode hook payload missing session_id")
	}
	db, err := p.openDB()
	if err != nil {
		return domain.Thread{}, err
	}
	defer db.Close()

	var (
		title         string
		directory     string
		timeCreatedMs int64
		timeUpdatedMs int64
	)
	err = db.QueryRowContext(ctx,
		`SELECT title, directory, time_created, time_updated FROM session WHERE id = ?`,
		payload.SessionID,
	).Scan(&title, &directory, &timeCreatedMs, &timeUpdatedMs)
	if err != nil {
		return domain.Thread{}, fmt.Errorf("session not found: %w", err)
	}

	t := domain.Thread{
		ID:             fmt.Sprintf("opencode-%s", payload.SessionID),
		Provider:       "opencode",
		OriginalID:     payload.SessionID,
		Title:          title,
		WorkspacePath:  directory,
		SourceFilePath: p.dbPath,
		CreatedAt:      time.UnixMilli(timeCreatedMs),
		LastSyncedAt:   time.UnixMilli(timeUpdatedMs),
	}
	return p.GetThreadDetails(ctx, t)
}

func (p *Provider) GetThreadDetails(ctx context.Context, t domain.Thread) (domain.Thread, error) {
	db, err := p.openDB()
	if err != nil {
		return t, err
	}
	defer db.Close()

	msgRows, err := db.QueryContext(ctx, `
		SELECT id, json_extract(data, '$.role'), time_created
		FROM message
		WHERE session_id = ?
		ORDER BY time_created
	`, t.OriginalID)
	if err != nil {
		return t, err
	}
	defer msgRows.Close()

	type msgMeta struct {
		id            string
		role          string
		timeCreatedMs int64
	}
	var msgs []msgMeta
	for msgRows.Next() {
		var m msgMeta
		if err := msgRows.Scan(&m.id, &m.role, &m.timeCreatedMs); err != nil {
			continue
		}
		msgs = append(msgs, m)
	}
	if err := msgRows.Err(); err != nil {
		return t, err
	}

	partRows, err := db.QueryContext(ctx, `
		SELECT message_id, data
		FROM part
		WHERE session_id = ?
		  AND json_extract(data, '$.type') = 'text'
		ORDER BY time_created
	`, t.OriginalID)
	if err != nil {
		return t, err
	}
	defer partRows.Close()

	textsByMsg := make(map[string][]string)
	for partRows.Next() {
		var (
			msgID   string
			rawData string
		)
		if err := partRows.Scan(&msgID, &rawData); err != nil {
			continue
		}
		var part struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(rawData), &part); err != nil || part.Text == "" {
			continue
		}
		textsByMsg[msgID] = append(textsByMsg[msgID], part.Text)
	}
	if err := partRows.Err(); err != nil {
		return t, err
	}

	var messages []domain.Message
	for _, m := range msgs {
		content := strings.Join(textsByMsg[m.id], "")
		if content == "" {
			continue
		}
		role := domain.RoleUser
		if m.role == "assistant" {
			role = domain.RoleAssistant
		}
		messages = append(messages, domain.Message{
			ID:        m.id,
			Role:      role,
			Content:   content,
			Timestamp: time.UnixMilli(m.timeCreatedMs),
		})
	}

	t.Messages = messages
	return t, nil
}
