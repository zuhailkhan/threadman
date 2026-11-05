package ports

import (
	"context"
	"errors"
	"os/exec"

	"github.com/zuhailkhan/threadman/internal/domain"
)

// ErrSourceUnavailable means a provider has no local source to read, usually
// because its tool is not installed. Callers must not treat this as "zero
// threads": pruning on it would erase the provider's stored history.
var ErrSourceUnavailable = errors.New("provider source not found")

type ThreadProvider interface {
	Name() string

	DiscoverThreads(ctx context.Context) ([]domain.Thread, error)

	GetThreadDetails(ctx context.Context, thread domain.Thread) (domain.Thread, error)
}

type ThreadRepository interface {
	UpsertThread(ctx context.Context, thread domain.Thread) error

	ListThreads(ctx context.Context, provider string, includeArchived bool) ([]domain.Thread, error)

	SearchMessages(ctx context.Context, query string) ([]domain.Thread, error)

	CountThreads(ctx context.Context) (int, error)

	DeleteThreadsNotIn(ctx context.Context, provider string, ids []string) error

	RecordHandoff(ctx context.Context, source domain.Thread, targetProvider, targetOriginalID string) error

	MarkThreadDeleted(ctx context.Context, id, hash string) error

	ArchiveThreadGroup(ctx context.Context, canonicalID string) error

	UnarchiveThreadGroup(ctx context.Context, canonicalID string) error

	RenameThreadGroup(ctx context.Context, canonicalID, customTitle string) error
}

type ThreadInjector interface {
	InjectThread(ctx context.Context, thread domain.Thread) (domain.Thread, error)
}

type HookPayload struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
}

type HookIngester interface {
	IngestFromHook(ctx context.Context, payload HookPayload) (domain.Thread, error)
}

type ThreadWriter interface {
	Name() string
	WriteThread(ctx context.Context, thread domain.Thread) (string, error)
	OpenCommand(sessionID string, workspacePath string) *exec.Cmd
	DeleteThread(ctx context.Context, thread domain.Thread) error
}
