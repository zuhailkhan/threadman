package sync

import (
	"context"
	"errors"
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
	Unavailable  bool // source not found; nothing was read or pruned
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
		results = append(results, s.syncProvider(ctx, p))
	}
	return results
}

// SyncProvider syncs one provider. ok is false when no provider has that name.
func (s *Service) SyncProvider(ctx context.Context, name string) (result SyncResult, ok bool) {
	for _, p := range s.providers {
		if p.Name() == name {
			return s.syncProvider(ctx, p), true
		}
	}
	return SyncResult{}, false
}

func (s *Service) syncProvider(ctx context.Context, p ports.ThreadProvider) SyncResult {
	result := SyncResult{Provider: p.Name()}

	threads, err := p.DiscoverThreads(ctx)
	if err != nil {
		if errors.Is(err, ports.ErrSourceUnavailable) {
			result.Unavailable = true
		} else {
			result.Err = err
		}
		return result
	}
	result.ThreadsFound = len(threads)

	discoveredIDs := make([]string, 0, len(threads))
	for _, t := range threads {
		discoveredIDs = append(discoveredIDs, t.ID)
		full, err := p.GetThreadDetails(ctx, t)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("read %s: %v", t.OriginalID, err))
			continue
		}
		markHandoffs(&full)
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

	// Finding nothing is not evidence that everything was deleted: session
	// files that can't be read or recognised are skipped without an error, so
	// pruning on an empty discovery could erase the provider's stored history.
	if len(discoveredIDs) > 0 {
		if err := s.repo.DeleteThreadsNotIn(ctx, p.Name(), discoveredIDs); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("prune stale threads: %v", err))
		}
	}
	return result
}

// markHandoffs finds the blocks threadman wrote into a session and files them
// as handoffs instead of messages the user typed. A provider titles a session
// after its first user message, which in a handed-off session is the block, so
// that title is dropped and the stored one is kept.
func markHandoffs(t *domain.Thread) {
	for i, m := range t.Messages {
		if m.Role != domain.RoleUser {
			continue
		}
		if id, _, ok := domain.ParseHandoffMarker(m.Content); ok {
			t.Messages[i].Kind = domain.KindHandoff
			t.Messages[i].HandoffID = id
		}
	}
	if domain.IsHandoffMarker(t.Title) {
		t.Title = ""
	}
}

func hasUserMessage(messages []domain.Message) bool {
	for _, m := range messages {
		if m.Role == domain.RoleUser {
			return true
		}
	}
	return false
}

func (s *Service) ListThreads(ctx context.Context, provider string, includeArchived bool) ([]domain.Thread, error) {
	return s.repo.ListThreads(ctx, provider, includeArchived)
}

func (s *Service) GetThread(ctx context.Context, id string) (domain.Thread, error) {
	threads, err := s.repo.ListThreads(ctx, "", true)
	if err != nil {
		return domain.Thread{}, err
	}
	for _, t := range threads {
		if t.ID == id {
			for _, p := range s.providers {
				if p.Name() == t.Provider {
					full, err := p.GetThreadDetails(ctx, t)
					if err != nil {
						return full, err
					}
					markHandoffs(&full)
					if full.Title == "" {
						full.Title = t.Title
					}
					return full, nil
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

func (s *Service) RecordHandoff(ctx context.Context, source domain.Thread, h domain.Handoff) error {
	return s.repo.RecordHandoff(ctx, source, h)
}

func (s *Service) GetHandoff(ctx context.Context, id string) (domain.Handoff, error) {
	return s.repo.GetHandoff(ctx, id)
}

func (s *Service) CountThreads(ctx context.Context) (int, error) {
	return s.repo.CountThreads(ctx)
}

func (s *Service) IngestFromHook(ctx context.Context, providerName string, payload ports.HookPayload) error {
	for _, p := range s.providers {
		if p.Name() != providerName {
			continue
		}
		hi, ok := p.(ports.HookIngester)
		if !ok {
			return fmt.Errorf("provider %s does not support hook ingestion", providerName)
		}
		thread, err := hi.IngestFromHook(ctx, payload)
		if err != nil {
			return err
		}
		markHandoffs(&thread)
		if !hasUserMessage(thread.Messages) {
			return nil
		}
		thread.LastSyncedAt = time.Now()
		return s.repo.UpsertThread(ctx, thread)
	}
	return fmt.Errorf("provider not found: %s", providerName)
}

func (s *Service) ArchiveThread(ctx context.Context, t domain.Thread) error {
	key := t.CanonicalID
	if key == "" {
		key = t.ID
	}
	return s.repo.ArchiveThreadGroup(ctx, key)
}

func (s *Service) UnarchiveThread(ctx context.Context, t domain.Thread) error {
	key := t.CanonicalID
	if key == "" {
		key = t.ID
	}
	return s.repo.UnarchiveThreadGroup(ctx, key)
}

func (s *Service) RenameThread(ctx context.Context, t domain.Thread, customTitle string) error {
	key := t.CanonicalID
	if key == "" {
		key = t.ID
	}
	return s.repo.RenameThreadGroup(ctx, key, customTitle)
}

func (s *Service) DeleteThreadCopies(ctx context.Context, selected []domain.Thread, writers []ports.ThreadWriter) error {
	writerByName := make(map[string]ports.ThreadWriter, len(writers))
	for _, w := range writers {
		writerByName[w.Name()] = w
	}

	for _, t := range selected {
		w, ok := writerByName[t.Provider]
		if !ok {
			return fmt.Errorf("no writer for provider: %s", t.Provider)
		}
		if err := w.DeleteThread(ctx, t); err != nil {
			return fmt.Errorf("delete source for %s/%s: %w", t.Provider, t.OriginalID, err)
		}

		hash := computeDeleteHash(t)
		if err := s.repo.MarkThreadDeleted(ctx, t.ID, hash); err != nil {
			return fmt.Errorf("mark deleted %s: %w", t.ID, err)
		}
	}
	return nil
}

func computeDeleteHash(t domain.Thread) string {
	if len(t.Messages) == 0 {
		return "0:0"
	}
	last := t.Messages[len(t.Messages)-1]
	return fmt.Sprintf("%d:%d", len(t.Messages), last.Timestamp.Unix())
}
