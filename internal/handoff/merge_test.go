package handoff

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zuhailkhan/threadman/internal/domain"
)

// briefLLM answers each brief request with "brief of <title>", read from the
// transcript's title line, and records what it was asked. It is safe for the
// concurrent calls a merge makes.
type briefLLM struct {
	mu      sync.Mutex
	systems map[string]string // title -> system prompt
	users   map[string]string // title -> transcript
	fail    string            // title whose brief fails
}

func (f *briefLLM) Complete(ctx context.Context, system, user string) (string, error) {
	line, _, _ := strings.Cut(user, "\n")
	title := strings.TrimPrefix(line, "Conversation title: ")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.systems == nil {
		f.systems, f.users = map[string]string{}, map[string]string{}
	}
	f.systems[title], f.users[title] = system, user
	if title == f.fail {
		return "", errors.New("model overloaded")
	}
	return "brief of " + title, nil
}

func (f *briefLLM) Ping(ctx context.Context) error { return nil }

// mergeThread builds a thread of n alternating messages starting at start.
func mergeThread(id, provider, title, workspace string, n int, start time.Time) domain.Thread {
	t := sampleThread(n)
	t.ID, t.Provider, t.Title, t.WorkspacePath = id, provider, title, workspace
	for i := range t.Messages {
		t.Messages[i].ID = id + ":" + string(rune('a'+i))
		t.Messages[i].Timestamp = start.Add(time.Duration(i) * time.Minute)
	}
	return t
}

func mergeSources() []domain.Thread {
	day := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	// Given newest first, to check the merge orders them itself.
	return []domain.Thread{
		mergeThread("opencode-c", "opencode", "Third", "/proj/c", 6, day.Add(48*time.Hour)),
		mergeThread("claude-a", "claude", "First", "/proj/a", 4, day),
		mergeThread("pi-b", "pi", "Second", "/proj/b", 4, day.Add(24*time.Hour)),
	}
}

func TestBuildMerge(t *testing.T) {
	c := &briefLLM{}
	src := mergeSources()
	out, h, err := BuildMerge(context.Background(), c, "Ollama · llama3.2", src, testNow)
	if err != nil {
		t.Fatal(err)
	}

	// Oldest first; the newest thread is the source and gives the workspace.
	if want := []string{"claude-a", "pi-b", "opencode-c"}; strings.Join(h.MergedFrom, ",") != strings.Join(want, ",") {
		t.Errorf("MergedFrom = %v, want %v", h.MergedFrom, want)
	}
	if h.SourceThreadID != "opencode-c" || h.Mode != "merge" || h.SourceMessageCount != 14 || h.SourceLastMessageID != "opencode-c:f" {
		t.Errorf("record = %+v", h)
	}
	if out.WorkspacePath != "/proj/c" || out.Title != "Third (merged from 3)" {
		t.Errorf("session: workspace %q, title %q", out.WorkspacePath, out.Title)
	}
	if len(out.Messages) != 1 || out.Messages[0].Content != h.Content || out.Messages[0].Kind != domain.KindHandoff {
		t.Fatalf("want one handoff block, got %+v", out.Messages)
	}
	block := h.Content
	if id, detail, ok := domain.ParseHandoffMarker(block); !ok || id != h.ID ||
		detail != "merged from 3 threads · Claude Code, pi and OpenCode · 14 messages" {
		t.Errorf("marker: id %q detail %q", id, detail)
	}

	// One section per thread, in order, each with its context and brief.
	sections := []string{
		"## Conversation 1 of 3 · Claude Code · First\n\nProject: /proj/a\nWhen: 2026-11-01 09:00 to 2026-11-01 09:03 · 4 messages\nFull conversation: threadman show claude-a\n\n### Brief\n\nbrief of First",
		"## Conversation 2 of 3 · pi · Second\n\nProject: /proj/b\n",
		"brief of Second",
		"## Conversation 3 of 3 · OpenCode · Third · newest\n\nProject: /proj/c\n",
		"brief of Third\n\n### Last 2 messages\n\n[User · 2026-11-03 09:04]\nuser 5\n\n[Assistant · OpenCode · 2026-11-03 09:05]\nassistant 6\n",
	}
	at := 0
	for _, s := range sections {
		i := strings.Index(block[at:], s)
		if i < 0 {
			t.Fatalf("block missing, or out of order:\n%s\n--- block ---\n%s", s, block)
		}
		at += i + len(s)
	}

	// Older threads are briefed whole; the newest without its last 2 messages.
	if !strings.Contains(c.systems["First"], wholeRule) || strings.Contains(c.systems["First"], tailRule) {
		t.Error("an older thread's brief should cover the whole conversation")
	}
	if !strings.Contains(c.systems["Third"], tailRule) {
		t.Error("the newest thread's brief should stop short of its last messages")
	}
	if !strings.Contains(c.users["First"], "user 3\n") || !strings.Contains(c.users["Third"], "user 3\n") || strings.Contains(c.users["Third"], "user 5") {
		t.Errorf("transcripts:\nFirst: %s\nThird: %s", c.users["First"], c.users["Third"])
	}
}

// A newest thread with nothing before its last messages is carried whole.
func TestBuildMergeShortNewestThread(t *testing.T) {
	c := &briefLLM{}
	day := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	src := []domain.Thread{
		mergeThread("claude-a", "claude", "First", "/a", 4, day),
		mergeThread("pi-b", "pi", "Second", "/b", 2, day.Add(time.Hour)),
	}
	_, h, err := BuildMerge(context.Background(), c, "", src, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if _, asked := c.systems["Second"]; asked {
		t.Error("a 2-message newest thread should not be summarized")
	}
	if !strings.Contains(h.Content, "· newest\n\nProject: /b\nWhen: 2026-11-01 10:00 to 2026-11-01 10:01 · 2 messages\nFull conversation: threadman show pi-b\n\n### Conversation\n\n[User") {
		t.Errorf("newest thread not carried whole:\n%s", h.Content)
	}
}

func TestBuildMergeRejectsTooFewOrTooMany(t *testing.T) {
	c := &briefLLM{}
	if _, _, err := BuildMerge(context.Background(), c, "", mergeSources()[:1], testNow); err == nil {
		t.Error("one thread is not a merge")
	}
	many := make([]domain.Thread, MaxMergeThreads+1)
	for i := range many {
		many[i] = mergeThread("t", "claude", "T", "/", 2, testNow)
	}
	if _, _, err := BuildMerge(context.Background(), c, "", many, testNow); err == nil {
		t.Errorf("more than %d threads should be refused", MaxMergeThreads)
	}
}

// One failed brief fails the merge, naming the thread.
func TestBuildMergeReportsFailedBrief(t *testing.T) {
	c := &briefLLM{fail: "Second"}
	_, _, err := BuildMerge(context.Background(), c, "", mergeSources(), testNow)
	if err == nil || !strings.Contains(err.Error(), `"Second"`) || !strings.Contains(err.Error(), "model overloaded") {
		t.Errorf("err = %v", err)
	}
}

func TestNestHeadings(t *testing.T) {
	in := "## Goal\nShip it.\n# Top\n```\n## not a heading\n```\n#### Deep\n#hashtag"
	want := "#### Goal\nShip it.\n#### Top\n```\n## not a heading\n```\n#### Deep\n#hashtag"
	if got := nestHeadings(in); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}
