package sync

import (
	"context"
	"fmt"
	"time"

	"github.com/zuhailkhan/threadman/internal/domain"
	"github.com/zuhailkhan/threadman/internal/ports"
)

type SyncResult struct {
	Provider     string
	ThreadsFound int
	ThreadsSaved int
	Skipped      int
	Errors       []string
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
				result.Errors = append(result.Errors, fmt.Sprintf("read %s: %v", t.OriginalID, err))
				continue
			}
			if !hasUserMessage(full.Messages) {
				result.Skipped++
				continue
			}
			full.LastSyncedAt = time.Now()
			if err := s.repo.UpsertThread(ctx, full); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("save %s: %v", t.OriginalID, err))
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

func hasUserMessage(messages []domain.Message) bool {
	for _, m := range messages {
		if m.Role == domain.RoleUser {
			return true
		}
	}
	return false
}

func (s *Service) ListThreads(ctx context.Context, provider string) ([]domain.Thread, error) {
	return s.repo.ListThreads(ctx, provider)
}

func (s *Service) GetThread(ctx context.Context, id string) (domain.Thread, error) {
	threads, err := s.repo.ListThreads(ctx, "")
	if err != nil {
		return domain.Thread{}, err
	}
	for _, t := range threads {
		if t.ID == id {
			for _, p := range s.providers {
				if p.Name() == t.Provider {
					return p.GetThreadDetails(ctx, t)
				}
			}
			return t, nil
		}
	}
	return domain.Thread{}, fmt.Errorf("thread not found: %s", id)
}

func (s *Service) SearchMessages(ctx context.Context, query string) ([]domain.Thread, error) {
	return s.repo.SearchMessages(ctx, query)
}

func (s *Service) ProviderNames() []string {
	names := make([]string, len(s.providers))
	for i, p := range s.providers {
		names[i] = p.Name()
	}
	return names
}

func (s *Service) CountThreads(ctx context.Context) (int, error) {
	return s.repo.CountThreads(ctx)
}
