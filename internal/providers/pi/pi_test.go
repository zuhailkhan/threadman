package pi

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/zuhailkhan/threadman/internal/domain"
	"github.com/zuhailkhan/threadman/internal/ports"
)

func TestDiscoverThreadsMissingDirIsUnavailable(t *testing.T) {
	p := &Provider{baseDir: filepath.Join(t.TempDir(), "missing")}
	if _, err := p.DiscoverThreads(context.Background()); !errors.Is(err, ports.ErrSourceUnavailable) {
		t.Errorf("got %v, want ErrSourceUnavailable", err)
	}
}

func TestWriteThenReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	w := &Writer{baseDir: dir}
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	src := domain.Thread{
		WorkspacePath: "/home/me/proj",
		Messages: []domain.Message{
			{Role: domain.RoleUser, Content: "hello there", Timestamp: ts},
			{Role: domain.RoleAssistant, Content: "hi!", Timestamp: ts.Add(time.Second)},
		},
	}
	id, err := w.WriteThread(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}

	p := &Provider{baseDir: dir}
	threads, err := p.DiscoverThreads(context.Background())
	if err != nil || len(threads) != 1 {
		t.Fatalf("discover: %v, %d threads", err, len(threads))
	}
	got := threads[0]
	if got.OriginalID != id || got.Provider != "pi" || got.WorkspacePath != "/home/me/proj" || got.Title != "hello there" {
		t.Errorf("unexpected metadata: %+v", got)
	}
	got, err = p.GetThreadDetails(context.Background(), got)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 2 || got.Messages[0].Role != domain.RoleUser || got.Messages[1].Content != "hi!" {
		t.Errorf("unexpected messages: %+v", got.Messages)
	}

	found, err := p.findSessionFile(id)
	if err != nil || found != got.SourceFilePath {
		t.Errorf("findSessionFile = %q, %v", found, err)
	}
}

func TestEncodeProjectDir(t *testing.T) {
	if got := encodeProjectDir("/home/me/proj"); got != "--home-me-proj--" {
		t.Errorf("got %q", got)
	}
}
