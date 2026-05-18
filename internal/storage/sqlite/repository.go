package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/zuhailkhan/threadman/ent"
	"github.com/zuhailkhan/threadman/ent/message"
	"github.com/zuhailkhan/threadman/ent/thread"
	"github.com/zuhailkhan/threadman/internal/domain"

	_ "github.com/mattn/go-sqlite3"
)

type Repository struct {
	client *ent.Client
}

func NewRepository(dbPath string) (*Repository, error) {
	client, err := ent.Open("sqlite3", dbPath)
	if err != nil {
		return nil, err
	}

	if err := client.Schema.Create(context.Background()); err != nil {
		client.Close()
		return nil, err
	}

	return &Repository{client: client}, nil
}

func (r *Repository) UpsertThread(ctx context.Context, t domain.Thread) error {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return err
	}

	err = tx.Thread.Create().
		SetID(t.ID).
		SetProvider(t.Provider).
		SetOriginalID(t.OriginalID).
		SetTitle(t.Title).
		SetWorkspacePath(t.WorkspacePath).
		SetSourceFilePath(t.SourceFilePath).
		SetCanonicalID(t.CanonicalID).
		SetCreatedAt(t.CreatedAt).
		SetLastSyncedAt(t.LastSyncedAt).
		OnConflict().
		Update(func(u *ent.ThreadUpsert) {
			u.SetProvider(t.Provider)
			u.SetOriginalID(t.OriginalID)
			u.SetTitle(t.Title)
			u.SetWorkspacePath(t.WorkspacePath)
			u.SetSourceFilePath(t.SourceFilePath)
			u.SetCreatedAt(t.CreatedAt)
			u.SetLastSyncedAt(t.LastSyncedAt)
			if t.CanonicalID != "" {
				u.SetCanonicalID(t.CanonicalID)
			}
		}).
		Exec(ctx)
	if err != nil {
		tx.Rollback()
		return err
	}

	_, err = tx.Message.Delete().
		Where(message.HasThreadWith(thread.ID(t.ID))).
		Exec(ctx)
	if err != nil {
		tx.Rollback()
		return err
	}

	builders := make([]*ent.MessageCreate, len(t.Messages))
	for i, m := range t.Messages {
		builders[i] = tx.Message.Create().
			SetID(t.ID+":"+m.ID).
			SetRole(string(m.Role)).
			SetContent(m.Content).
			SetTimestamp(m.Timestamp).
			SetThreadID(t.ID)
	}

	if len(builders) > 0 {
		if err := tx.Message.CreateBulk(builders...).Exec(ctx); err != nil {
			tx.Rollback()
			return err
		}
	}

	return tx.Commit()
}

func (r *Repository) ListThreads(ctx context.Context, provider string) ([]domain.Thread, error) {
	query := r.client.Thread.Query().WithMessages()
	if provider != "" {
		query = query.Where(thread.Provider(provider))
	}

	entities, err := query.All(ctx)
	if err != nil {
		return nil, err
	}

	threads := make([]domain.Thread, len(entities))
	for i, e := range entities {
		threads[i] = mapEntThreadToDomain(e)
	}

	return threads, nil
}

func (r *Repository) CountThreads(ctx context.Context) (int, error) {
	return r.client.Thread.Query().Count(ctx)
}

func (r *Repository) DeleteThreadsNotIn(ctx context.Context, provider string, ids []string) error {
	_, err := r.client.Thread.Delete().
		Where(thread.Provider(provider), thread.IDNotIn(ids...)).
		Exec(ctx)
	return err
}

func (r *Repository) SearchMessages(ctx context.Context, query string) ([]domain.Thread, error) {
	entities, err := r.client.Thread.Query().
		Where(thread.HasMessagesWith(message.ContentContains(query))).
		WithMessages().
		All(ctx)
	if err != nil {
		return nil, err
	}

	threads := make([]domain.Thread, len(entities))
	for i, e := range entities {
		threads[i] = mapEntThreadToDomain(e)
	}

	return threads, nil
}

func (r *Repository) RecordHandoff(ctx context.Context, source domain.Thread, targetProvider, targetOriginalID string) error {
	rootID := source.CanonicalID
	if rootID == "" {
		rootID = source.ID
	}

	if err := r.client.Thread.UpdateOneID(source.ID).
		SetCanonicalID(rootID).
		Exec(ctx); err != nil {
		return fmt.Errorf("set source canonical_id: %w", err)
	}

	stub := domain.Thread{
		ID:            targetProvider + "-" + targetOriginalID,
		Provider:      targetProvider,
		OriginalID:    targetOriginalID,
		CanonicalID:   rootID,
		Title:         source.Title,
		WorkspacePath: source.WorkspacePath,
		CreatedAt:     time.Now(),
		LastSyncedAt:  time.Now(),
	}
	return r.UpsertThread(ctx, stub)
}

func mapEntThreadToDomain(e *ent.Thread) domain.Thread {
	messages := make([]domain.Message, len(e.Edges.Messages))
	for j, m := range e.Edges.Messages {
		messages[j] = domain.Message{
			ID:        m.ID,
			Role:      domain.Role(m.Role),
			Content:   m.Content,
			Timestamp: m.Timestamp,
		}
	}

	return domain.Thread{
		ID:             e.ID,
		Provider:       e.Provider,
		OriginalID:     e.OriginalID,
		Title:          e.Title,
		WorkspacePath:  e.WorkspacePath,
		SourceFilePath: e.SourceFilePath,
		CanonicalID:    e.CanonicalID,
		Messages:       messages,
		CreatedAt:      e.CreatedAt,
		LastSyncedAt:   e.LastSyncedAt,
	}
}
