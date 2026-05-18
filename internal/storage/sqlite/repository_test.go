package sqlite

import (
	"context"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/zuhailkhan/threadman/internal/domain"
)

func setupTestRepo(t *testing.T) *Repository {
	t.Helper()
	repo, err := NewRepository("file::memory:?cache=shared&mode=memory&_fk=1")
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	t.Cleanup(func() { repo.client.Close() })
	return repo
}

func baseThread(id, provider, originalID string) domain.Thread {
	return domain.Thread{
		ID:             id,
		Provider:       provider,
		OriginalID:     originalID,
		Title:          "test thread",
		WorkspacePath:  "/workspace/proj",
		SourceFilePath: "/path/to/file",
		CreatedAt:      time.Now(),
		LastSyncedAt:   time.Now(),
	}
}

func TestUpsertThreadPreservesCanonicalID(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()

	original := baseThread("claude-abc", "claude", "abc")
	original.CanonicalID = "claude-abc"
	if err := repo.UpsertThread(ctx, original); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	// Re-upsert with empty canonical_id (simulates a normal sync)
	synced := baseThread("claude-abc", "claude", "abc")
	synced.CanonicalID = ""
	synced.Title = "updated title"
	if err := repo.UpsertThread(ctx, synced); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	threads, err := repo.ListThreads(ctx, "claude")
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(threads) != 1 {
		t.Fatalf("expected 1 thread, got %d", len(threads))
	}
	if threads[0].CanonicalID != "claude-abc" {
		t.Errorf("canonical_id: got %q, want %q", threads[0].CanonicalID, "claude-abc")
	}
	if threads[0].Title != "updated title" {
		t.Errorf("title: got %q, want %q", threads[0].Title, "updated title")
	}
}

func TestRecordHandoff_SetsCanonicalOnSource(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()

	source := baseThread("claude-abc", "claude", "abc")
	if err := repo.UpsertThread(ctx, source); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := repo.RecordHandoff(ctx, source, "gemini", "xyz"); err != nil {
		t.Fatalf("RecordHandoff: %v", err)
	}

	threads, _ := repo.ListThreads(ctx, "")
	byProvider := map[string]domain.Thread{}
	for _, th := range threads {
		byProvider[th.Provider] = th
	}

	if len(byProvider) != 2 {
		t.Fatalf("expected 2 threads, got %d", len(byProvider))
	}
	if byProvider["claude"].CanonicalID != "claude-abc" {
		t.Errorf("source canonical_id: got %q, want %q", byProvider["claude"].CanonicalID, "claude-abc")
	}
	if byProvider["gemini"].ID != "gemini-xyz" {
		t.Errorf("stub ID: got %q, want %q", byProvider["gemini"].ID, "gemini-xyz")
	}
	if byProvider["gemini"].CanonicalID != "claude-abc" {
		t.Errorf("stub canonical_id: got %q, want %q", byProvider["gemini"].CanonicalID, "claude-abc")
	}
	if byProvider["gemini"].Title != "test thread" {
		t.Errorf("stub title: got %q, want %q", byProvider["gemini"].Title, "test thread")
	}
}

func TestRecordHandoff_ChainInheritsRootCanonicalID(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()

	source := baseThread("claude-abc", "claude", "abc")
	if err := repo.UpsertThread(ctx, source); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := repo.RecordHandoff(ctx, source, "gemini", "xyz"); err != nil {
		t.Fatalf("first handoff: %v", err)
	}

	threads, _ := repo.ListThreads(ctx, "")
	byProvider := map[string]domain.Thread{}
	for _, th := range threads {
		byProvider[th.Provider] = th
	}
	geminiStub := byProvider["gemini"]

	if err := repo.RecordHandoff(ctx, geminiStub, "opencode", "ses_abc123"); err != nil {
		t.Fatalf("second handoff: %v", err)
	}

	threads, _ = repo.ListThreads(ctx, "")
	byProvider = map[string]domain.Thread{}
	for _, th := range threads {
		byProvider[th.Provider] = th
	}

	if byProvider["opencode"].CanonicalID != "claude-abc" {
		t.Errorf("opencode canonical_id: got %q, want %q", byProvider["opencode"].CanonicalID, "claude-abc")
	}
}
