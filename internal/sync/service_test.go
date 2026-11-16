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
