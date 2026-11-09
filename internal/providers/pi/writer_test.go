package pi

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zuhailkhan/threadman/internal/domain"
)

func TestEncodeWorkspaceDir(t *testing.T) {
	cases := map[string]string{
		`C:\Users\zuhai\workspace\threadman`: "--C--Users-zuhai-workspace-threadman--",
		"/home/user/myproject":               "--home-user-myproject--",
	}
	for in, want := range cases {
		if got := encodeWorkspaceDir(in); got != want {
			t.Errorf("encodeWorkspaceDir(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWriterRoundTrip(t *testing.T) {
	dir := t.TempDir()
	w := &Writer{baseDir: dir}
	p := &Provider{baseDir: dir}

	now := time.Now()
	thread := domain.Thread{
		Provider:      "claude",
		OriginalID:    "orig",
		WorkspacePath: "/home/user/myproject",
		Messages: []domain.Message{
			{ID: "1", Role: domain.RoleUser, Content: "hello there", Timestamp: now},
			{ID: "2", Role: domain.RoleSystem, Content: "ignored", Timestamp: now},
			{ID: "3", Role: domain.RoleAssistant, Content: "hi back", Timestamp: now.Add(time.Second)},
			{ID: "4", Role: domain.RoleUser, Content: "and again", Timestamp: now.Add(2 * time.Second)},
		},
	}

	id, err := w.WriteThread(context.Background(), thread)
	if err != nil || id == "" {
		t.Fatalf("WriteThread: %q, %v", id, err)
	}

	files, _ := filepath.Glob(filepath.Join(dir, "--home-user-myproject--", "*_"+id+".jsonl"))
	if len(files) != 1 {
		t.Fatalf("want one session file named for id %s, got %v", id, files)
	}

	threads, err := p.DiscoverThreads(context.Background())
	if err != nil || len(threads) != 1 {
		t.Fatalf("discover: %v, %d", err, len(threads))
	}
	got := threads[0]
	if got.OriginalID != id || got.WorkspacePath != thread.WorkspacePath || got.Title != "hello there" {
		t.Errorf("thread: %+v", got)
	}
	full, err := p.GetThreadDetails(context.Background(), got)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"hello there", "hi back", "and again"}
	if len(full.Messages) != len(want) {
		t.Fatalf("messages: %+v", full.Messages)
	}
	for i, m := range full.Messages {
		if m.Content != want[i] {
			t.Errorf("message %d = %q, want %q", i, m.Content, want[i])
		}
	}
}

func TestDeleteThread(t *testing.T) {
	dir := t.TempDir()
	w := &Writer{baseDir: dir}
	id, err := w.WriteThread(context.Background(), domain.Thread{WorkspacePath: "/p", Messages: []domain.Message{{Role: domain.RoleUser, Content: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*", "*_"+id+".jsonl"))
	if err := w.DeleteThread(context.Background(), domain.Thread{SourceFilePath: files[0]}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(files[0]); !os.IsNotExist(err) {
		t.Errorf("file still exists: %v", err)
	}
}

func TestFlatLayoutRoundTrip(t *testing.T) {
	dir := t.TempDir()
	w := &Writer{baseDir: dir, flat: true}
	id, err := w.WriteThread(context.Background(), domain.Thread{
		WorkspacePath: "/p",
		Messages:      []domain.Message{{Role: domain.RoleUser, Content: "flat"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "*_"+id+".jsonl")); len(files) != 1 {
		t.Fatalf("want session file directly in %s", dir)
	}
	threads, err := (&Provider{baseDir: dir}).DiscoverThreads(context.Background())
	if err != nil || len(threads) != 1 || threads[0].Title != "flat" {
		t.Errorf("discover flat: %v %+v", err, threads)
	}
}
