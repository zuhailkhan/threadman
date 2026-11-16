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

func threadWithMessages(id, provider, originalID string) domain.Thread {
	th := baseThread(id, provider, originalID)
	th.Messages = []domain.Message{
		{ID: "m1", Role: domain.RoleUser, Content: "hello", Timestamp: time.Unix(1000, 0)},
		{ID: "m2", Role: domain.RoleAssistant, Content: "hi", Timestamp: time.Unix(1001, 0)},
	}
	return th
}

func countMessages(t *testing.T, repo *Repository) int {
	t.Helper()
	n, err := repo.client.Message.Query().Count(context.Background())
	if err != nil {
		t.Fatalf("count messages: %v", err)
	}
	return n
}

func TestDeleteThreadsNotIn_RemovesMessagesOfPrunedThreads(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()

	for _, id := range []string{"gone", "kept"} {
		if err := repo.UpsertThread(ctx, threadWithMessages("claude-"+id, "claude", id)); err != nil {
			t.Fatalf("upsert %s: %v", id, err)
		}
	}

	if err := repo.DeleteThreadsNotIn(ctx, "claude", []string{"claude-kept"}); err != nil {
		t.Fatalf("DeleteThreadsNotIn: %v", err)
	}

	if got := countMessages(t, repo); got != 2 {
		t.Errorf("messages left: got %d, want the 2 of the kept thread", got)
	}
}

func TestUpsertThread_ReimportsPrunedThread(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()

	gone := threadWithMessages("claude-gone", "claude", "gone")
	if err := repo.UpsertThread(ctx, gone); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := repo.UpsertThread(ctx, threadWithMessages("claude-kept", "claude", "kept")); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := repo.DeleteThreadsNotIn(ctx, "claude", []string{"claude-kept"}); err != nil {
		t.Fatalf("DeleteThreadsNotIn: %v", err)
	}

	if err := repo.UpsertThread(ctx, gone); err != nil {
		t.Fatalf("re-upsert of pruned thread: %v", err)
	}
	threads, err := repo.ListThreads(ctx, "claude", false)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(threads) != 2 {
		t.Fatalf("expected 2 threads, got %d", len(threads))
	}
	for _, th := range threads {
		if len(th.Messages) != 2 {
			t.Errorf("%s: got %d messages, want 2", th.ID, len(th.Messages))
		}
	}
}

// Databases written by earlier versions hold messages whose thread was pruned
// without them. Deleting the thread row directly reproduces that state.
func orphanMessages(t *testing.T, repo *Repository, th domain.Thread) {
	t.Helper()
	ctx := context.Background()
	if err := repo.UpsertThread(ctx, th); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := repo.client.Thread.DeleteOneID(th.ID).Exec(ctx); err != nil {
		t.Fatalf("delete thread row: %v", err)
	}
	if got := countMessages(t, repo); got != len(th.Messages) {
		t.Fatalf("setup: got %d orphaned messages, want %d", got, len(th.Messages))
	}
}

func TestUpsertThread_ReplacesOrphanedMessages(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()

	th := threadWithMessages("claude-abc", "claude", "abc")
	orphanMessages(t, repo, th)

	if err := repo.UpsertThread(ctx, th); err != nil {
		t.Fatalf("upsert over orphaned messages: %v", err)
	}
	threads, err := repo.ListThreads(ctx, "claude", false)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(threads) != 1 || len(threads[0].Messages) != 2 {
		t.Fatalf("got %d threads, want 1 with 2 messages", len(threads))
	}
	if got := countMessages(t, repo); got != 2 {
		t.Errorf("messages: got %d, want 2", got)
	}
}

func TestDeleteThreadsNotIn_SweepsOrphanedMessages(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()

	orphanMessages(t, repo, threadWithMessages("claude-old", "claude", "old"))
	if err := repo.UpsertThread(ctx, threadWithMessages("claude-kept", "claude", "kept")); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := repo.DeleteThreadsNotIn(ctx, "claude", []string{"claude-kept"}); err != nil {
		t.Fatalf("DeleteThreadsNotIn: %v", err)
	}

	if got := countMessages(t, repo); got != 2 {
		t.Errorf("messages left: got %d, want the 2 of the kept thread", got)
	}
}
