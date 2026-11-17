package claude

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/zuhailkhan/threadman/internal/domain"
	"github.com/zuhailkhan/threadman/internal/ports"
)

func TestDiscoverThreadsMissingDirIsUnavailable(t *testing.T) {
	p := &Provider{baseDir: filepath.Join(t.TempDir(), "missing")}

	_, err := p.DiscoverThreads(context.Background())

	if !errors.Is(err, ports.ErrSourceUnavailable) {
		t.Errorf("got %v, want ErrSourceUnavailable", err)
	}
}

// A line longer than the old 2 MB limit, such as a pasted image, used to end
// the read early and silently drop every message after it.
func TestGetThreadDetailsReadsLongLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sess.jsonl")
	big := strings.Repeat("x", 3*1024*1024)
	lines := []string{
		`{"type":"user","uuid":"u1","timestamp":"2026-01-01T00:00:01Z","message":{"role":"user","content":"` + big + `"}}`,
		`{"type":"user","uuid":"u2","timestamp":"2026-01-01T00:00:02Z","message":{"role":"user","content":"after the long line"}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	p := &Provider{baseDir: dir}
	full, err := p.GetThreadDetails(context.Background(), domain.Thread{SourceFilePath: path})
	if err != nil {
		t.Fatalf("GetThreadDetails: %v", err)
	}
	if len(full.Messages) != 2 || full.Messages[1].Content != "after the long line" {
		t.Errorf("got %d messages; the read stopped at the long line", len(full.Messages))
	}
}

func TestTitleTruncatesByCharacter(t *testing.T) {
	got := truncate(strings.Repeat("修正", 40), 50)
	if !utf8.ValidString(got) || utf8.RuneCountInString(got) != 50 {
		t.Errorf("truncate = %q (%d characters)", got, utf8.RuneCountInString(got))
	}
}
