package sqlite

import (
	"context"
	"strings"
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

	if err := repo.RecordHandoff(ctx, source, testHandoff(source, "gemini", "xyz")); err != nil {
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
	if err := repo.RecordHandoff(ctx, source, testHandoff(source, "gemini", "xyz")); err != nil {
		t.Fatalf("first handoff: %v", err)
	}

	threads, _ := repo.ListThreads(ctx, "", false)
	byProvider := map[string]domain.Thread{}
	for _, th := range threads {
		byProvider[th.Provider] = th
	}
	geminiStub := byProvider["gemini"]

	if err := repo.RecordHandoff(ctx, geminiStub, testHandoff(geminiStub, "opencode", "ses_abc123")); err != nil {
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
	if err := repo.RecordHandoff(ctx, source, testHandoff(source, "gemini", "xyz")); err != nil {
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

func TestMarkMissingNotIn_LeavesTombstones(t *testing.T) {
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

	if err := repo.MarkMissingNotIn(ctx, "claude", []string{}); err != nil {
		t.Fatalf("MarkMissingNotIn: %v", err)
	}
	if e, err := repo.client.Thread.Get(ctx, th.ID); err != nil || e.SourceMissing || !e.Deleted {
		t.Fatalf("tombstone after marking: %+v, %v", e, err)
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

func threadsByID(t *testing.T, repo *Repository, provider string) map[string]domain.Thread {
	t.Helper()
	threads, err := repo.ListThreads(context.Background(), provider, true)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	out := map[string]domain.Thread{}
	for _, th := range threads {
		out[th.ID] = th
	}
	return out
}

// A session its tool removed stays in threadman, with everything the user
// set on it, flagged as missing.
func TestMarkMissingNotIn_KeepsThreadAndMessages(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()

	for _, id := range []string{"gone", "kept"} {
		if err := repo.UpsertThread(ctx, threadWithMessages("claude-"+id, "claude", id)); err != nil {
			t.Fatalf("upsert %s: %v", id, err)
		}
	}
	if err := repo.RenameThreadGroup(ctx, "claude-gone", "My title"); err != nil {
		t.Fatal(err)
	}
	if err := repo.ArchiveThreadGroup(ctx, "claude-gone"); err != nil {
		t.Fatal(err)
	}

	if err := repo.MarkMissingNotIn(ctx, "claude", []string{"claude-kept"}); err != nil {
		t.Fatalf("MarkMissingNotIn: %v", err)
	}

	got := threadsByID(t, repo, "claude")
	gone, kept := got["claude-gone"], got["claude-kept"]
	if !gone.SourceMissing || kept.SourceMissing {
		t.Errorf("SourceMissing: gone %v, kept %v", gone.SourceMissing, kept.SourceMissing)
	}
	if len(gone.Messages) != 2 || gone.CustomTitle != "My title" || !gone.Archived {
		t.Errorf("missing thread lost data: %+v", gone)
	}
	if n := countMessages(t, repo); n != 4 {
		t.Errorf("messages: got %d, want all 4", n)
	}
}

func TestMarkMissingNotIn_OnlyThatProvider(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()
	if err := repo.UpsertThread(ctx, threadWithMessages("pi-1", "pi", "1")); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkMissingNotIn(ctx, "claude", []string{"claude-x"}); err != nil {
		t.Fatal(err)
	}
	if th := threadsByID(t, repo, "pi")["pi-1"]; th.SourceMissing {
		t.Error("another provider's thread was flagged")
	}
}

// A session that reappears, for example restored from a backup, is live again.
func TestUpsertThread_ClearsMissingFlag(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()

	gone := threadWithMessages("claude-gone", "claude", "gone")
	if err := repo.UpsertThread(ctx, gone); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := repo.MarkMissingNotIn(ctx, "claude", []string{"claude-other"}); err != nil {
		t.Fatalf("MarkMissingNotIn: %v", err)
	}
	if err := repo.UpsertThread(ctx, gone); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	th := threadsByID(t, repo, "claude")["claude-gone"]
	if th.SourceMissing || len(th.Messages) != 2 {
		t.Errorf("after the session came back: missing %v, %d messages", th.SourceMissing, len(th.Messages))
	}
}

// Databases written by versions that pruned threads hold messages whose
// thread was deleted without them. Deleting the thread row directly reproduces that state.
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

func TestMarkMissingNotIn_SweepsOrphanedMessages(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()

	orphanMessages(t, repo, threadWithMessages("claude-old", "claude", "old"))
	if err := repo.UpsertThread(ctx, threadWithMessages("claude-kept", "claude", "kept")); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := repo.MarkMissingNotIn(ctx, "claude", []string{"claude-kept"}); err != nil {
		t.Fatalf("MarkMissingNotIn: %v", err)
	}

	if got := countMessages(t, repo); got != 2 {
		t.Errorf("messages left: got %d, want the 2 of the kept thread", got)
	}
}

func testHandoff(source domain.Thread, provider, originalID string) domain.Handoff {
	return domain.Handoff{
		ID:                 source.ID + "->" + provider + "-" + originalID,
		SourceThreadID:     source.ID,
		TargetProvider:     provider,
		TargetOriginalID:   originalID,
		Mode:               "full",
		SourceMessageCount: len(source.Messages),
		Content:            "[threadman handoff x]\nbody",
	}
}

func TestRecordHandoff_StoresRecord(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()

	source := threadWithMessages("claude-abc", "claude", "abc")
	if err := repo.UpsertThread(ctx, source); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	h := domain.Handoff{
		ID:                  "h-1",
		TargetProvider:      "pi",
		TargetOriginalID:    "sess-1",
		Mode:                "detailed",
		Summarizer:          "Ollama · llama3.2",
		SourceMessageCount:  2,
		SourceLastMessageID: "claude-abc:m2",
		Content:             "[threadman handoff h-1 · from Claude Code]\nbrief",
		CreatedAt:           time.Unix(5000, 0),
	}
	if err := repo.RecordHandoff(ctx, source, h); err != nil {
		t.Fatalf("RecordHandoff: %v", err)
	}

	got, err := repo.GetHandoff(ctx, "h-1")
	if err != nil {
		t.Fatalf("GetHandoff: %v", err)
	}
	want := h
	want.SourceThreadID = "claude-abc"
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("CreatedAt: got %v, want %v", got.CreatedAt, want.CreatedAt)
	}
	got.CreatedAt = want.CreatedAt
	if got != want {
		t.Errorf("record:\n got %+v\nwant %+v", got, want)
	}
	if got.TargetThreadID() != "pi-sess-1" {
		t.Errorf("TargetThreadID: got %q", got.TargetThreadID())
	}
}

func TestRecordHandoff_RejectsIncompleteRecord(t *testing.T) {
	repo := setupTestRepo(t)
	source := baseThread("claude-abc", "claude", "abc")
	if err := repo.RecordHandoff(context.Background(), source, domain.Handoff{ID: "h-1", TargetProvider: "pi"}); err == nil {
		t.Error("a record without a target session should be rejected")
	}
}

// A live hook can ingest the target session before the handoff is recorded.
func TestRecordHandoff_KeepsMessagesOfAlreadySyncedTarget(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()

	source := threadWithMessages("claude-abc", "claude", "abc")
	target := threadWithMessages("pi-sess-1", "pi", "sess-1")
	for _, th := range []domain.Thread{source, target} {
		if err := repo.UpsertThread(ctx, th); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}

	if err := repo.RecordHandoff(ctx, source, testHandoff(source, "pi", "sess-1")); err != nil {
		t.Fatalf("RecordHandoff: %v", err)
	}

	threads, err := repo.ListThreads(ctx, "pi", false)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(threads) != 1 || len(threads[0].Messages) != 2 {
		t.Fatalf("target lost its messages: %+v", threads)
	}
	if threads[0].CanonicalID != "claude-abc" {
		t.Errorf("target not linked: canonical_id %q", threads[0].CanonicalID)
	}
}

func TestUpsertThread_StoresMessageKind(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()

	th := baseThread("pi-sess-1", "pi", "sess-1")
	th.Messages = []domain.Message{
		{ID: "b", Role: domain.RoleUser, Content: "[threadman handoff h-1]\nblock", Kind: domain.KindHandoff, HandoffID: "h-1", Timestamp: time.Unix(1000, 0)},
		{ID: "u", Role: domain.RoleUser, Content: "next", Timestamp: time.Unix(1001, 0)},
	}
	if err := repo.UpsertThread(ctx, th); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	threads, err := repo.ListThreads(ctx, "pi", false)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	byContent := map[string]domain.Message{}
	for _, m := range threads[0].Messages {
		byContent[m.Content] = m
	}
	if m := byContent["[threadman handoff h-1]\nblock"]; m.Kind != domain.KindHandoff || m.HandoffID != "h-1" {
		t.Errorf("handoff block: kind %q, handoff_id %q", m.Kind, m.HandoffID)
	}
	if m := byContent["next"]; m.Kind != domain.KindMessage || m.HandoffID != "" {
		t.Errorf("ordinary message: kind %q, handoff_id %q", m.Kind, m.HandoffID)
	}
}

func TestSearchMessages_SkipsHandoffBlocks(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()

	source := baseThread("claude-abc", "claude", "abc")
	source.Messages = []domain.Message{{ID: "m1", Role: domain.RoleUser, Content: "fix the zebra parser", Timestamp: time.Unix(1000, 0)}}
	target := baseThread("pi-sess-1", "pi", "sess-1")
	target.Messages = []domain.Message{{ID: "b", Role: domain.RoleUser, Content: "[threadman handoff h-1]\nUser: fix the zebra parser", Kind: domain.KindHandoff, HandoffID: "h-1", Timestamp: time.Unix(1001, 0)}}
	for _, th := range []domain.Thread{source, target} {
		if err := repo.UpsertThread(ctx, th); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}

	threads, err := repo.SearchMessages(ctx, "zebra")
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(threads) != 1 || threads[0].ID != "claude-abc" {
		ids := []string{}
		for _, th := range threads {
			ids = append(ids, th.ID)
		}
		t.Errorf("got %v, want only the source thread", ids)
	}
}

func TestUpsertThread_EmptyTitleKeepsStoredTitle(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()

	th := baseThread("pi-sess-1", "pi", "sess-1")
	th.Title = "Fix the parser"
	if err := repo.UpsertThread(ctx, th); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	th.Title = ""
	if err := repo.UpsertThread(ctx, th); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}

	threads, err := repo.ListThreads(ctx, "pi", false)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if threads[0].Title != "Fix the parser" {
		t.Errorf("title: got %q, want the stored one", threads[0].Title)
	}
}

func TestListThreads_NewestActivityFirst(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()

	// Created first but talked in most recently.
	old := baseThread("claude-old", "claude", "old")
	old.CreatedAt = time.Unix(1000, 0)
	old.Messages = []domain.Message{{ID: "m", Role: domain.RoleUser, Content: "recent", Timestamp: time.Unix(9000, 0)}}
	quiet := baseThread("claude-quiet", "claude", "quiet")
	quiet.CreatedAt = time.Unix(5000, 0)
	quiet.Messages = []domain.Message{{ID: "m", Role: domain.RoleUser, Content: "older", Timestamp: time.Unix(5000, 0)}}
	empty := baseThread("claude-empty", "claude", "empty")
	empty.CreatedAt = time.Unix(7000, 0)
	for _, th := range []domain.Thread{quiet, old, empty} {
		if err := repo.UpsertThread(ctx, th); err != nil {
			t.Fatal(err)
		}
	}

	threads, err := repo.ListThreads(ctx, "", false)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, th := range threads {
		got = append(got, th.ID)
	}
	if want := []string{"claude-old", "claude-empty", "claude-quiet"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("order %v, want %v", got, want)
	}
}

func TestSearchMessages_SkipsDeletedThreads(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()

	th := baseThread("claude-abc", "claude", "abc")
	th.Messages = []domain.Message{{ID: "m1", Role: domain.RoleUser, Content: "fix the zebra parser", Timestamp: time.Unix(1000, 0)}}
	if err := repo.UpsertThread(ctx, th); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkThreadDeleted(ctx, th.ID, computeDeleteHash(th)); err != nil {
		t.Fatal(err)
	}

	threads, err := repo.SearchMessages(ctx, "zebra")
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 0 {
		t.Errorf("search found a deleted thread: %+v", threads)
	}
}
