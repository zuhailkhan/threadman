package ports

import (
	"context"
	"os/exec"

	"github.com/zuhailkhan/threadman/internal/domain"
)

type ThreadProvider interface {
	Name() string

	DiscoverThreads(ctx context.Context) ([]domain.Thread, error)

	GetThreadDetails(ctx context.Context, thread domain.Thread) (domain.Thread, error)
}

type ThreadRepository interface {
	UpsertThread(ctx context.Context, thread domain.Thread) error

	ListThreads(ctx context.Context, provider string) ([]domain.Thread, error)

	SearchMessages(ctx context.Context, query string) ([]domain.Thread, error)

	CountThreads(ctx context.Context) (int, error)
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
}
