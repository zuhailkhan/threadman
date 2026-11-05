package claude

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/zuhailkhan/threadman/internal/ports"
)

func TestDiscoverThreadsMissingDirIsUnavailable(t *testing.T) {
	p := &Provider{baseDir: filepath.Join(t.TempDir(), "missing")}

	_, err := p.DiscoverThreads(context.Background())

	if !errors.Is(err, ports.ErrSourceUnavailable) {
		t.Errorf("got %v, want ErrSourceUnavailable", err)
	}
}
