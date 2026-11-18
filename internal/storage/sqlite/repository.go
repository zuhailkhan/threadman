package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/zuhailkhan/threadman/ent"
	"github.com/zuhailkhan/threadman/ent/message"
	"github.com/zuhailkhan/threadman/ent/thread"
	"github.com/zuhailkhan/threadman/internal/domain"

	// A SQLite written in pure Go, so threadman builds without a C compiler.
	_ "modernc.org/sqlite"
)

type Repository struct {
	client *ent.Client
}

// NewRepository opens the database at dsn: a file name or "file:" URI,
// optionally followed by connection parameters such as _fk=1.
func NewRepository(dsn string) (*Repository, error) {
	db, err := sql.Open("sqlite", withTimeFormat(dsn))
	if err != nil {
		return nil, err
	}
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.SQLite, db)))

	if err := client.Schema.Create(context.Background()); err != nil {
		client.Close()
		return nil, err
	}

	return &Repository{client: client}, nil
}

// withTimeFormat makes the driver write times as SQLite's own text format,
// "2006-01-02 15:04:05.000-07:00", the layout earlier versions of threadman
// wrote and the one the driver reads first. Left to its default it would
// write Go's time.String().
func withTimeFormat(dsn string) string {
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "_time_format=sqlite"
}

func computeDeleteHash(t domain.Thread) string {
	if len(t.Messages) == 0 {
		return "0:0"
	}
	last := t.Messages[len(t.Messages)-1]
	return fmt.Sprintf("%d:%d", len(t.Messages), last.Timestamp.Unix())
}

func (r *Repository) UpsertThread(ctx context.Context, t domain.Thread) error {
	existing, lookupErr := r.client.Thread.Get(ctx, t.ID)
	if lookupErr == nil && existing.Deleted {
		incoming := computeDeleteHash(t)
		if incoming == existing.DeletedHash {
			return nil
		}
		if err := r.client.Thread.UpdateOneID(t.ID).
			SetDeleted(false).
			SetDeletedHash("").
			Exec(ctx); err != nil {
			return err
		}
	}

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
		SetCustomTitle("").
		SetArchived(false).
		SetDeleted(false).
		SetDeletedHash("").
		SetSourceMissing(false).
		OnConflict().
		Update(func(u *ent.ThreadUpsert) {
			// The provider produced the session, so it exists again.
			u.SetSourceMissing(false)
			u.SetProvider(t.Provider)
			u.SetOriginalID(t.OriginalID)
			// An empty title never replaces a known one. Sync blanks the
			// title of a handed-off session, whose provider would otherwise
			// title it after the handoff block.
			if t.Title != "" {
				u.SetTitle(t.Title)
			}
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

	messageIDs := make([]string, len(t.Messages))
	for i, m := range t.Messages {
		messageIDs[i] = t.ID + ":" + m.ID
	}

	// Also clear rows by ID: messages orphaned by an earlier prune are no longer
	// linked to the thread, and would otherwise collide with the insert below.
	_, err = tx.Message.Delete().
		Where(message.Or(
			message.HasThreadWith(thread.ID(t.ID)),
			message.IDIn(messageIDs...),
		)).
		Exec(ctx)
	if err != nil {
		tx.Rollback()
		return err
	}

	builders := make([]*ent.MessageCreate, len(t.Messages))
	for i, m := range t.Messages {
		builders[i] = tx.Message.Create().
			SetID(messageIDs[i]).
			SetRole(string(m.Role)).
			SetContent(m.Content).
			SetTimestamp(m.Timestamp).
			SetKind(string(m.Kind)).
			SetHandoffID(m.HandoffID).
			SetTools(domain.EncodeTools(m.Tools)).
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

func (r *Repository) ListThreads(ctx context.Context, provider string, includeArchived bool) ([]domain.Thread, error) {
	query := r.client.Thread.Query().
		Where(thread.Deleted(false)).
		WithMessages()
	if provider != "" {
		query = query.Where(thread.Provider(provider))
	}
	if !includeArchived {
		query = query.Where(thread.Archived(false))
	}

	entities, err := query.All(ctx)
	if err != nil {
		return nil, err
	}

	threads := make([]domain.Thread, len(entities))
	for i, e := range entities {
		threads[i] = mapEntThreadToDomain(e)
	}
	sortByActivity(threads)

	return threads, nil
}

// sortByActivity orders threads by their newest message, most recent first.
func sortByActivity(threads []domain.Thread) {
	slices.SortStableFunc(threads, func(a, b domain.Thread) int {
		return lastActivity(b).Compare(lastActivity(a))
	})
}

// lastActivity is the time of a thread's newest message, or when it was
// created if it has none.
func lastActivity(t domain.Thread) time.Time {
	at := t.CreatedAt
	for _, m := range t.Messages {
		if m.Timestamp.After(at) {
			at = m.Timestamp
		}
	}
	return at
}

func (r *Repository) CountThreads(ctx context.Context) (int, error) {
	return r.client.Thread.Query().Count(ctx)
}

// MarkMissingNotIn flags a provider's threads that are not in ids as missing
// from their tool. Nothing is deleted: threadman keeps its copy of a session
// the tool has removed, and the flag clears if the session is synced again.
// Threads the user deleted keep their tombstone instead.
func (r *Repository) MarkMissingNotIn(ctx context.Context, provider string, ids []string) error {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return err
	}
	if err := tx.Thread.Update().
		Where(
			thread.Provider(provider),
			thread.IDNotIn(ids...),
			thread.Deleted(false),
			thread.SourceMissing(false),
		).
		SetSourceMissing(true).
		Exec(ctx); err != nil {
		tx.Rollback()
		return err
	}
	// Earlier versions pruned threads by deleting them, which left their
	// messages behind unlinked. Nothing can show those any more.
	if _, err := tx.Message.Delete().Where(message.Not(message.HasThread())).Exec(ctx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (r *Repository) SearchMessages(ctx context.Context, query string) ([]domain.Thread, error) {
	entities, err := r.client.Thread.Query().
		// Handoff blocks repeat messages stored on their source thread, so
		// matching them would report the same text twice.
		Where(
			thread.Deleted(false),
			thread.HasMessagesWith(
				message.ContentContains(query),
				message.KindNEQ(string(domain.KindHandoff)),
			),
		).
		WithMessages().
		All(ctx)
	if err != nil {
		return nil, err
	}

	threads := make([]domain.Thread, len(entities))
	for i, e := range entities {
		threads[i] = mapEntThreadToDomain(e)
	}
	sortByActivity(threads)

	return threads, nil
}

// RecordHandoff stores h and links its target session into the source's
// group. The target thread is created as an empty stub until its session is
// synced; a target that is already stored keeps its messages. A merge's
// target is not linked to any group.
func (r *Repository) RecordHandoff(ctx context.Context, source domain.Thread, h domain.Handoff) error {
	if h.ID == "" || h.TargetProvider == "" || h.TargetOriginalID == "" {
		return fmt.Errorf("handoff record is missing its ID or target")
	}
	createdAt := h.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	if err := r.client.Handoff.Create().
		SetID(h.ID).
		SetSourceThreadID(source.ID).
		SetMergedFrom(h.MergedFrom).
		SetTargetThreadID(h.TargetThreadID()).
		SetTargetProvider(h.TargetProvider).
		SetMode(h.Mode).
		SetSummarizer(h.Summarizer).
		SetSourceMessageCount(h.SourceMessageCount).
		SetSourceLastMessageID(h.SourceLastMessageID).
		SetContent(h.Content).
		SetCreatedAt(createdAt).
		Exec(ctx); err != nil {
		return fmt.Errorf("save handoff: %w", err)
	}

	if len(h.MergedFrom) > 0 {
		return r.recordMergeTarget(ctx, source, h)
	}

	rootID := source.CanonicalID
	if rootID == "" {
		rootID = source.ID
	}

	if err := r.client.Thread.UpdateOneID(source.ID).
		SetCanonicalID(rootID).
		Exec(ctx); err != nil {
		return fmt.Errorf("set source canonical_id: %w", err)
	}

	// A live hook can ingest the target session before this runs. Upserting
	// a stub over it would delete the messages it already has.
	targetID := h.TargetThreadID()
	exists, err := r.client.Thread.Query().Where(thread.ID(targetID)).Exist(ctx)
	if err != nil {
		return err
	}
	if exists {
		return r.client.Thread.UpdateOneID(targetID).SetCanonicalID(rootID).Exec(ctx)
	}

	stub := domain.Thread{
		ID:            targetID,
		Provider:      h.TargetProvider,
		OriginalID:    h.TargetOriginalID,
		CanonicalID:   rootID,
		Title:         source.Title,
		WorkspacePath: source.WorkspacePath,
		CreatedAt:     time.Now(),
		LastSyncedAt:  time.Now(),
	}
	return r.UpsertThread(ctx, stub)
}

// recordMergeTarget stores the session a merge wrote as a conversation of its
// own. It is not linked into any source's group: the sources ran separately
// and never saw each other, so they don't form one timeline. source is the
// newest of them; the stub takes its workspace.
func (r *Repository) recordMergeTarget(ctx context.Context, source domain.Thread, h domain.Handoff) error {
	targetID := h.TargetThreadID()
	exists, err := r.client.Thread.Query().Where(thread.ID(targetID)).Exist(ctx)
	if err != nil || exists {
		return err
	}
	title := source.CustomTitle
	if title == "" {
		title = source.Title
	}
	stub := domain.Thread{
		ID:            targetID,
		Provider:      h.TargetProvider,
		OriginalID:    h.TargetOriginalID,
		Title:         domain.MergedTitle(title, len(h.MergedFrom)),
		WorkspacePath: source.WorkspacePath,
		CreatedAt:     time.Now(),
		LastSyncedAt:  time.Now(),
	}
	return r.UpsertThread(ctx, stub)
}

// GetHandoff returns the stored record of one handoff.
func (r *Repository) GetHandoff(ctx context.Context, id string) (domain.Handoff, error) {
	e, err := r.client.Handoff.Get(ctx, id)
	if err != nil {
		return domain.Handoff{}, err
	}
	provider := e.TargetProvider
	return domain.Handoff{
		ID:                  e.ID,
		SourceThreadID:      e.SourceThreadID,
		MergedFrom:          e.MergedFrom,
		TargetProvider:      provider,
		TargetOriginalID:    strings.TrimPrefix(e.TargetThreadID, provider+"-"),
		Mode:                e.Mode,
		Summarizer:          e.Summarizer,
		SourceMessageCount:  e.SourceMessageCount,
		SourceLastMessageID: e.SourceLastMessageID,
		Content:             e.Content,
		CreatedAt:           e.CreatedAt,
	}, nil
}

func (r *Repository) MarkThreadDeleted(ctx context.Context, id, hash string) error {
	return r.client.Thread.UpdateOneID(id).
		SetDeleted(true).
		SetDeletedHash(hash).
		Exec(ctx)
}

func (r *Repository) ArchiveThreadGroup(ctx context.Context, canonicalID string) error {
	return r.client.Thread.Update().
		Where(thread.Or(
			thread.CanonicalID(canonicalID),
			thread.And(thread.ID(canonicalID), thread.CanonicalID("")),
		)).
		SetArchived(true).
		Exec(ctx)
}

func (r *Repository) UnarchiveThreadGroup(ctx context.Context, canonicalID string) error {
	return r.client.Thread.Update().
		Where(thread.Or(
			thread.CanonicalID(canonicalID),
			thread.And(thread.ID(canonicalID), thread.CanonicalID("")),
		)).
		SetArchived(false).
		Exec(ctx)
}

func (r *Repository) RenameThreadGroup(ctx context.Context, canonicalID, customTitle string) error {
	return r.client.Thread.Update().
		Where(thread.Or(
			thread.CanonicalID(canonicalID),
			thread.And(thread.ID(canonicalID), thread.CanonicalID("")),
		)).
		SetCustomTitle(customTitle).
		Exec(ctx)
}

func mapEntThreadToDomain(e *ent.Thread) domain.Thread {
	messages := make([]domain.Message, len(e.Edges.Messages))
	for j, m := range e.Edges.Messages {
		messages[j] = domain.Message{
			ID:        m.ID,
			Role:      domain.Role(m.Role),
			Content:   m.Content,
			Timestamp: m.Timestamp,
			Kind:      domain.MessageKind(m.Kind),
			HandoffID: m.HandoffID,
			Tools:     domain.DecodeTools(m.Tools),
		}
	}

	title := e.Title
	if e.CustomTitle != "" {
		title = e.CustomTitle
	}

	return domain.Thread{
		ID:             e.ID,
		Provider:       e.Provider,
		OriginalID:     e.OriginalID,
		Title:          title,
		CustomTitle:    e.CustomTitle,
		WorkspacePath:  e.WorkspacePath,
		SourceFilePath: e.SourceFilePath,
		CanonicalID:    e.CanonicalID,
		Messages:       messages,
		CreatedAt:      e.CreatedAt,
		LastSyncedAt:   e.LastSyncedAt,
		Archived:       e.Archived,
		SourceMissing:  e.SourceMissing,
	}
}
