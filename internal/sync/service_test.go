package sync

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/zuhailkhan/threadman/internal/domain"
	"github.com/zuhailkhan/threadman/internal/ports"
)

type stubProvider struct {
	name    string
	err     error
	threads []domain.Thread
}

func (p stubProvider) Name() string { return p.name }

func (p stubProvider) DiscoverThreads(context.Context) ([]domain.Thread, error) {
	return p.threads, p.err
}

func (p stubProvider) GetThreadDetails(_ context.Context, t domain.Thread) (domain.Thread, error) {
	return t, nil
}

type pruneRecorder struct {
	ports.ThreadRepository
	pruned []string
}

func (r *pruneRecorder) DeleteThreadsNotIn(_ context.Context, provider string, _ []string) error {
	r.pruned = append(r.pruned, provider)
	return nil
}

func TestSyncAllSkipsUnavailableSourceWithoutPruning(t *testing.T) {
	repo := &pruneRecorder{}
	svc := New([]ports.ThreadProvider{
		stubProvider{name: "missing", err: fmt.Errorf("%w: /nowhere", ports.ErrSourceUnavailable)},
		// No user message, so the thread is discovered but never upserted.
		stubProvider{name: "present", threads: []domain.Thread{{ID: "present-1"}}},
	}, repo)

	results := svc.SyncAll(context.Background())

	if !results[0].Unavailable || results[0].Err != nil {
		t.Errorf("missing source: got %+v, want Unavailable with no Err", results[0])
	}
	if results[1].Unavailable || results[1].Err != nil {
		t.Errorf("present source: got %+v", results[1])
	}
	if len(repo.pruned) != 1 || repo.pruned[0] != "present" {
		t.Errorf("pruned %v, want only [present]", repo.pruned)
	}
}

func TestSyncAllDoesNotPruneOnEmptyDiscovery(t *testing.T) {
	repo := &pruneRecorder{}
	svc := New([]ports.ThreadProvider{stubProvider{name: "empty"}}, repo)

	r := svc.SyncAll(context.Background())[0]

	if r.Err != nil || r.Unavailable || len(r.Errors) != 0 {
		t.Errorf("got %+v, want a clean result", r)
	}
	if len(repo.pruned) != 0 {
		t.Errorf("pruned %v after discovering no threads", repo.pruned)
	}
}

func TestSyncAllReportsRealFailures(t *testing.T) {
	repo := &pruneRecorder{}
	boom := errors.New("corrupt database")
	svc := New([]ports.ThreadProvider{stubProvider{name: "broken", err: boom}}, repo)

	r := svc.SyncAll(context.Background())[0]

	if !errors.Is(r.Err, boom) || r.Unavailable {
		t.Errorf("got %+v, want Err set and Unavailable false", r)
	}
	if len(repo.pruned) != 0 {
		t.Errorf("pruned %v after a failed read", repo.pruned)
	}
}

// detailProvider discovers one thread and returns it with fixed details.
type detailProvider struct {
	thread domain.Thread
}

func (p detailProvider) Name() string { return p.thread.Provider }

func (p detailProvider) DiscoverThreads(context.Context) ([]domain.Thread, error) {
	return []domain.Thread{p.thread}, nil
}

func (p detailProvider) GetThreadDetails(_ context.Context, t domain.Thread) (domain.Thread, error) {
	t.Messages = append([]domain.Message(nil), p.thread.Messages...)
	t.Title = p.thread.Title
	return t, nil
}

func (p detailProvider) IngestFromHook(ctx context.Context, _ ports.HookPayload) (domain.Thread, error) {
	return p.GetThreadDetails(ctx, p.thread)
}

type upsertRecorder struct {
	ports.ThreadRepository
	saved []domain.Thread
}

func (r *upsertRecorder) UpsertThread(_ context.Context, t domain.Thread) error {
	r.saved = append(r.saved, t)
	return nil
}

func (r *upsertRecorder) DeleteThreadsNotIn(context.Context, string, []string) error { return nil }

// handedOffThread is a pi session that threadman created by handing a
// conversation off, and that the user then continued.
func handedOffThread() domain.Thread {
	block := domain.HandoffMarker("h-1", "from Claude Code · full transcript · 4 messages") + "\n\nbody"
	return domain.Thread{
		ID:       "pi-sess-1",
		Provider: "pi",
		// pi titles a session after its first user message: the block.
		Title: "[threadman handoff h-1 · from Claude Code · f...",
		Messages: []domain.Message{
			{ID: "b", Role: domain.RoleUser, Content: block},
			{ID: "u", Role: domain.RoleUser, Content: "now add tests"},
			{ID: "a", Role: domain.RoleAssistant, Content: "[threadman handoff h-2] is not a marker in an assistant turn"},
		},
	}
}

func assertHandoffsMarked(t *testing.T, th domain.Thread) {
	t.Helper()
	if th.Title != "" {
		t.Errorf("title %q should be dropped so the stored one is kept", th.Title)
	}
	want := []struct {
		kind domain.MessageKind
		id   string
	}{{domain.KindHandoff, "h-1"}, {domain.KindMessage, ""}, {domain.KindMessage, ""}}
	for i, w := range want {
		if m := th.Messages[i]; m.Kind != w.kind || m.HandoffID != w.id {
			t.Errorf("message %d: kind %q, handoff_id %q; want %q, %q", i, m.Kind, m.HandoffID, w.kind, w.id)
		}
	}
}

func TestSyncAllMarksHandoffBlocks(t *testing.T) {
	repo := &upsertRecorder{}
	svc := New([]ports.ThreadProvider{detailProvider{thread: handedOffThread()}}, repo)

	svc.SyncAll(context.Background())

	if len(repo.saved) != 1 {
		t.Fatalf("saved %d threads, want 1", len(repo.saved))
	}
	assertHandoffsMarked(t, repo.saved[0])
}

func TestIngestFromHookMarksHandoffBlocks(t *testing.T) {
	repo := &upsertRecorder{}
	svc := New([]ports.ThreadProvider{detailProvider{thread: handedOffThread()}}, repo)

	if err := svc.IngestFromHook(context.Background(), "pi", ports.HookPayload{}); err != nil {
		t.Fatal(err)
	}
	if len(repo.saved) != 1 {
		t.Fatalf("saved %d threads, want 1", len(repo.saved))
	}
	assertHandoffsMarked(t, repo.saved[0])
}

type countingProvider struct {
	stubProvider
	discovered *int
}

func (p countingProvider) DiscoverThreads(ctx context.Context) ([]domain.Thread, error) {
	*p.discovered++
	return p.stubProvider.DiscoverThreads(ctx)
}

func TestSyncProviderSyncsOnlyThatProvider(t *testing.T) {
	var a, b int
	svc := New([]ports.ThreadProvider{
		countingProvider{stubProvider{name: "a"}, &a},
		countingProvider{stubProvider{name: "b"}, &b},
	}, &pruneRecorder{})

	r, ok := svc.SyncProvider(context.Background(), "b")
	if !ok || r.Provider != "b" {
		t.Fatalf("SyncProvider = %+v, %v", r, ok)
	}
	if a != 0 || b != 1 {
		t.Errorf("discovered a %d times and b %d times, want 0 and 1", a, b)
	}
	if _, ok := svc.SyncProvider(context.Background(), "missing"); ok {
		t.Error("an unknown provider was synced")
	}
}
