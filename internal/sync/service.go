package sync

import (
	"context"
	"time"

	"github.com/zuhailkhan/threadman/internal/domain"
	"github.com/zuhailkhan/threadman/internal/ports"
)

type SyncResult struct {
	Provider     string
	ThreadsFound int
	ThreadsSaved int
	Err          error
}

type Service struct {
	providers []ports.ThreadProvider
	repo      ports.ThreadRepository
}

func New(providers []ports.ThreadProvider, repo ports.ThreadRepository) *Service {
	return &Service{providers: providers, repo: repo}
}

func (s *Service) SyncAll(ctx context.Context) []SyncResult {
	results := make([]SyncResult, 0, len(s.providers))

	for _, p := range s.providers {
		result := SyncResult{Provider: p.Name()}

		threads, err := p.DiscoverThreads(ctx)
		if err != nil {
			result.Err = err
			results = append(results, result)
			continue
		}
		result.ThreadsFound = len(threads)

		for _, t := range threads {
			full, err := p.GetThreadDetails(ctx, t)
			if err != nil {
				continue
			}
			full.LastSyncedAt = time.Now()
			if err := s.repo.UpsertThread(ctx, full); err != nil {
				continue
			}
			result.ThreadsSaved++
		}

		results = append(results, result)
	}

	return results
}

func (s *Service) SyncProvider(ctx context.Context, name string) (SyncResult, bool) {
	for _, p := range s.providers {
		if p.Name() == name {
			results := s.SyncAll(ctx)
			for _, r := range results {
				if r.Provider == name {
					return r, true
				}
			}
		}
	}
	return SyncResult{}, false
}

func (s *Service) ListThreads(ctx context.Context, provider string) ([]domain.Thread, error) {
	return s.repo.ListThreads(ctx, provider)
}

func (s *Service) SearchMessages(ctx context.Context, query string) ([]domain.Thread, error) {
	return s.repo.SearchMessages(ctx, query)
}
