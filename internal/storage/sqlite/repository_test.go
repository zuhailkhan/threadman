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

	threads, err := repo.ListThreads(ctx, "claude", false)
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

	threads, _ := repo.ListThreads(ctx, "", false)
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

	threads, _ := repo.ListThreads(ctx, "", false)
	byProvider := map[string]domain.Thread{}
	for _, th := range threads {
		byProvider[th.Provider] = th
	}
	geminiStub := byProvider["gemini"]

	if err := repo.RecordHandoff(ctx, geminiStub, "opencode", "ses_abc123"); err != nil {
		t.Fatalf("second handoff: %v", err)
	}

	threads, _ = repo.ListThreads(ctx, "", false)
	byProvider = map[string]domain.Thread{}
	for _, th := range threads {
		byProvider[th.Provider] = th
	}

	if byProvider["opencode"].CanonicalID != "claude-abc" {
		t.Errorf("opencode canonical_id: got %q, want %q", byProvider["opencode"].CanonicalID, "claude-abc")
	}
}

func TestMarkThreadDeleted_SkipsReimportOnSameHash(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()

	th := baseThread("claude-abc", "claude", "abc")
	th.Messages = []domain.Message{
		{ID: "m1", Role: domain.RoleUser, Content: "hello", Timestamp: time.Unix(1000, 0)},
	}
	if err := repo.UpsertThread(ctx, th); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	hash := computeDeleteHash(th)
	if err := repo.MarkThreadDeleted(ctx, th.ID, hash); err != nil {
		t.Fatalf("MarkThreadDeleted: %v", err)
	}

	if err := repo.UpsertThread(ctx, th); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}

	threads, err := repo.ListThreads(ctx, "", false)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(threads) != 0 {
		t.Errorf("expected 0 threads (deleted skipped), got %d", len(threads))
	}
}

func TestMarkThreadDeleted_ReimportsOnChangedHash(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()

	th := baseThread("claude-abc", "claude", "abc")
	th.Messages = []domain.Message{
		{ID: "m1", Role: domain.RoleUser, Content: "hello", Timestamp: time.Unix(1000, 0)},
	}
	if err := repo.UpsertThread(ctx, th); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	hash := computeDeleteHash(th)
	if err := repo.MarkThreadDeleted(ctx, th.ID, hash); err != nil {
		t.Fatalf("MarkThreadDeleted: %v", err)
	}

	th.Messages = append(th.Messages, domain.Message{
		ID: "m2", Role: domain.RoleAssistant, Content: "reply", Timestamp: time.Unix(2000, 0),
	})
	if err := repo.UpsertThread(ctx, th); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}

	threads, err := repo.ListThreads(ctx, "", false)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(threads) != 1 {
		t.Fatalf("expected 1 thread after re-import, got %d", len(threads))
	}
}

func TestArchiveThreadGroup_HidesFromDefaultList(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()

	th := baseThread("claude-abc", "claude", "abc")
	if err := repo.UpsertThread(ctx, th); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := repo.ArchiveThreadGroup(ctx, th.ID); err != nil {
		t.Fatalf("ArchiveThreadGroup: %v", err)
	}

	threads, err := repo.ListThreads(ctx, "", false)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(threads) != 0 {
		t.Errorf("expected 0 threads with includeArchived=false, got %d", len(threads))
	}

	threads, err = repo.ListThreads(ctx, "", true)
	if err != nil {
		t.Fatalf("ListThreads (include): %v", err)
	}
	if len(threads) != 1 {
		t.Errorf("expected 1 thread with includeArchived=true, got %d", len(threads))
	}
	if !threads[0].Archived {
		t.Error("expected thread.Archived = true")
	}
}

func TestUnarchiveThreadGroup(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()

	th := baseThread("claude-abc", "claude", "abc")
	if err := repo.UpsertThread(ctx, th); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := repo.ArchiveThreadGroup(ctx, th.ID); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if err := repo.UnarchiveThreadGroup(ctx, th.ID); err != nil {
		t.Fatalf("unarchive: %v", err)
	}

	threads, err := repo.ListThreads(ctx, "", false)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(threads) != 1 {
		t.Errorf("expected 1 thread after unarchive, got %d", len(threads))
	}
}

func TestArchiveThreadGroup_AppliesToAllGroupMembers(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()

	source := baseThread("claude-abc", "claude", "abc")
	source.CanonicalID = "claude-abc"
	if err := repo.UpsertThread(ctx, source); err != nil {
		t.Fatalf("upsert source: %v", err)
	}
	if err := repo.RecordHandoff(ctx, source, "gemini", "xyz"); err != nil {
		t.Fatalf("handoff: %v", err)
	}

	if err := repo.ArchiveThreadGroup(ctx, "claude-abc"); err != nil {
		t.Fatalf("ArchiveThreadGroup: %v", err)
	}

	threads, err := repo.ListThreads(ctx, "", false)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(threads) != 0 {
		t.Errorf("expected 0 threads (all archived), got %d", len(threads))
	}
}

func TestRenameThreadGroup_SetsCustomTitle(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()

	th := baseThread("claude-abc", "claude", "abc")
	if err := repo.UpsertThread(ctx, th); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := repo.RenameThreadGroup(ctx, th.ID, "My Custom Title"); err != nil {
		t.Fatalf("RenameThreadGroup: %v", err)
	}

	threads, err := repo.ListThreads(ctx, "", false)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(threads) != 1 {
		t.Fatalf("expected 1 thread, got %d", len(threads))
	}
	if threads[0].Title != "My Custom Title" {
		t.Errorf("Title: got %q, want %q", threads[0].Title, "My Custom Title")
	}
	if threads[0].CustomTitle != "My Custom Title" {
		t.Errorf("CustomTitle: got %q, want %q", threads[0].CustomTitle, "My Custom Title")
	}
}

func TestRenameThreadGroup_ClearTitle(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()

	th := baseThread("claude-abc", "claude", "abc")
	if err := repo.UpsertThread(ctx, th); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := repo.RenameThreadGroup(ctx, th.ID, "My Custom Title"); err != nil {
		t.Fatalf("rename: %v", err)
	}

	if err := repo.RenameThreadGroup(ctx, th.ID, ""); err != nil {
		t.Fatalf("clear: %v", err)
	}

	threads, err := repo.ListThreads(ctx, "", false)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if threads[0].Title != "test thread" {
		t.Errorf("Title: got %q, want original %q", threads[0].Title, "test thread")
	}
	if threads[0].CustomTitle != "" {
		t.Errorf("CustomTitle: got %q, want empty", threads[0].CustomTitle)
	}
}

func TestUpsertThread_PreservesArchivedAndCustomTitle(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()

	th := baseThread("claude-abc", "claude", "abc")
	if err := repo.UpsertThread(ctx, th); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := repo.ArchiveThreadGroup(ctx, th.ID); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if err := repo.RenameThreadGroup(ctx, th.ID, "My Title"); err != nil {
		t.Fatalf("rename: %v", err)
	}

	th.Title = "updated by sync"
	if err := repo.UpsertThread(ctx, th); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}

	threads, err := repo.ListThreads(ctx, "", true)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(threads) != 1 {
		t.Fatalf("expected 1 thread, got %d", len(threads))
	}
	if !threads[0].Archived {
		t.Error("expected Archived to be preserved after re-sync")
	}
	if threads[0].CustomTitle != "My Title" {
		t.Errorf("CustomTitle: got %q, want %q", threads[0].CustomTitle, "My Title")
	}
}

func TestDeleteThreadsNotIn_PreservesTombstones(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()

	th := baseThread("claude-abc", "claude", "abc")
	th.Messages = []domain.Message{
		{ID: "m1", Role: domain.RoleUser, Content: "hello", Timestamp: time.Unix(1000, 0)},
	}
	if err := repo.UpsertThread(ctx, th); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	hash := computeDeleteHash(th)
	if err := repo.MarkThreadDeleted(ctx, th.ID, hash); err != nil {
		t.Fatalf("MarkThreadDeleted: %v", err)
	}

	if err := repo.DeleteThreadsNotIn(ctx, "claude", []string{}); err != nil {
		t.Fatalf("DeleteThreadsNotIn: %v", err)
	}

	if err := repo.UpsertThread(ctx, th); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	threads, err := repo.ListThreads(ctx, "", false)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(threads) != 0 {
		t.Errorf("expected tombstone to prevent re-import, got %d threads", len(threads))
	}
}
