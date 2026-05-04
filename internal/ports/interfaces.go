package ports

import (
	"context"
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
}
