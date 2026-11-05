package opencode

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/zuhailkhan/threadman/internal/ports"
)

func TestDiscoverThreadsMissingDBIsUnavailable(t *testing.T) {
	p := &Provider{dbPath: filepath.Join(t.TempDir(), "opencode.db")}

	_, err := p.DiscoverThreads(context.Background())

	if !errors.Is(err, ports.ErrSourceUnavailable) {
		t.Errorf("got %v, want ErrSourceUnavailable", err)
	}
}
