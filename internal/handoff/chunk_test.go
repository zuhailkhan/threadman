package handoff

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zuhailkhan/threadman/internal/domain"
	"github.com/zuhailkhan/threadman/internal/llm"
)

// windowClient is a model with a context window. It rejects a request whose
// estimated size is over reject (when set), answers notes and condense
// requests with short notes, and records every request.
type windowClient struct {
	limits llm.Limits
	// reject is the input size, in estimated tokens, above which a request
	// fails as too long; 0 accepts everything.
	reject int
	// rejectLimit is the context size the rejection reports; 0 reports none.
	rejectLimit int

	mu       sync.Mutex
	systems  []string
	inputs   []string
	inFlight atomic.Int32
	maxSeen  atomic.Int32
}

func (c *windowClient) Limits(context.Context) llm.Limits { return c.limits }
func (c *windowClient) Ping(context.Context) error        { return nil }

func (c *windowClient) Complete(ctx context.Context, system, user string) (string, error) {
	n := c.inFlight.Add(1)
	defer c.inFlight.Add(-1)
	for {
		seen := c.maxSeen.Load()
		if n <= seen || c.maxSeen.CompareAndSwap(seen, n) {
			break
		}
	}
	time.Sleep(time.Millisecond)

	c.mu.Lock()
	c.systems = append(c.systems, system)
	c.inputs = append(c.inputs, user)
	c.mu.Unlock()

	if c.reject > 0 && estimateTokens(system)+estimateTokens(user) > c.reject {
		return "", &llm.ContextError{Limit: c.rejectLimit, Err: errors.New("400 Bad Request: request exceeds the available context size")}
	}
	switch {
	case strings.HasPrefix(system, notesHead):
		return "- notes on " + firstLineOf(user), nil
	case strings.HasPrefix(system, condenseHead):
		return "- condensed notes", nil
	}
	return "## Goal\nthe brief", nil
}

func firstLineOf(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, "This is part") {
			return l
		}
	}
	return "notes"
}

// longThread is a conversation of n turns of about size bytes each.
func longThread(n, size int) domain.Thread {
	t := domain.Thread{ID: "claude-long", Provider: "claude", Title: "long work"}
	base := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	for i := range n {
		role := domain.RoleUser
		if i%2 == 1 {
			role = domain.RoleAssistant
		}
		t.Messages = append(t.Messages, domain.Message{
			ID: fmt.Sprint(i), Role: role, Timestamp: base.Add(time.Duration(i) * time.Minute),
			Content: fmt.Sprintf("turn %d ", i) + strings.Repeat("word ", size/5),
		})
	}
	return t
}

// A transcript that fits is sent in one request, as before.
func TestShortTranscriptIsOneRequest(t *testing.T) {
	c := &windowClient{limits: llm.Limits{Context: 100_000}}
	if _, _, err := Build(context.Background(), c, "test", longThread(20, 200), ModeDetailed, time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(c.inputs) != 1 || c.systems[0] != detailedPrompt {
		t.Errorf("want one request with the detailed prompt, got %d", len(c.inputs))
	}
}

// A transcript longer than the context is read in parts that each fit, and
// the brief is written from the notes on them.
func TestLongTranscriptIsReadInParts(t *testing.T) {
	const window = 4000
	c := &windowClient{limits: llm.Limits{Context: window}}
	thread := longThread(120, 600) // about 24k tokens
	out, _, err := Build(context.Background(), c, "test", thread, ModeDetailed, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	notes := 0
	for i, sys := range c.systems {
		if total := estimateTokens(sys) + estimateTokens(c.inputs[i]); total > window {
			t.Errorf("request %d is about %d tokens, more than the %d-token window", i, total, window)
		}
		if strings.HasPrefix(sys, notesHead) {
			notes++
		}
	}
	if notes < 6 {
		t.Errorf("want the transcript read in several parts, got %d", notes)
	}
	if last := c.systems[len(c.systems)-1]; last != detailedPrompt {
		t.Error("the last request should write the brief with the detailed prompt")
	}
	// Parts are numbered and cover every turn before the split, in order.
	first := c.inputs[0]
	if !strings.Contains(first, "This is part 1 of") || !strings.Contains(first, "turns 1 to") {
		t.Errorf("first part isn't labelled:\n%.300s", first)
	}
	if !strings.Contains(out.Messages[0].Content, "## Goal\nthe brief") {
		t.Error("the block doesn't hold the brief")
	}
}

// With an unknown context, a request rejected as too long teaches the limit
// the server reported, and the brief is retried in parts.
func TestRejectedRequestIsRetriedInParts(t *testing.T) {
	c := &windowClient{reject: 3000, rejectLimit: 3000}
	if _, _, err := Build(context.Background(), c, "test", longThread(80, 600), ModeLight, time.Now()); err != nil {
		t.Fatalf("brief failed: %v", err)
	}
	if c.systems[0] != lightPrompt || !strings.Contains(strings.Join(c.systems, "|"), notesHead) {
		t.Error("want a whole-transcript attempt, then notes on parts")
	}
}

// When the server doesn't say its limit, what was sent is taken as too much.
func TestRejectionWithoutLimitStillRecovers(t *testing.T) {
	c := &windowClient{reject: 5000}
	if _, _, err := Build(context.Background(), c, "test", longThread(80, 600), ModeDetailed, time.Now()); err != nil {
		t.Fatalf("brief failed: %v", err)
	}
}

// A single turn longer than a part is cut down, keeping its start and end.
func TestOversizedTurnIsCut(t *testing.T) {
	c := &windowClient{limits: llm.Limits{Context: 3000}}
	thread := longThread(10, 200)
	thread.Messages[2].Content = "START " + strings.Repeat("x", 60_000) + " END"
	if _, _, err := Build(context.Background(), c, "test", thread, ModeDetailed, time.Now()); err != nil {
		t.Fatal(err)
	}
	found := false
	for i, in := range c.inputs {
		if estimateTokens(c.systems[i])+estimateTokens(in) > 3000 {
			t.Errorf("request %d doesn't fit", i)
		}
		if strings.Contains(in, "START") && strings.Contains(in, "END") && strings.Contains(in, "characters of this turn left out") {
			found = true
		}
	}
	if !found {
		t.Error("the long turn wasn't cut keeping its start and end")
	}
}

// Notes too long to send together are condensed in groups first.
func TestNotesAreCondensed(t *testing.T) {
	// The model rejects anything over its window, so a final request holding
	// every note would fail the test.
	c := &windowClient{limits: llm.Limits{Context: 1800}, reject: 1800, rejectLimit: 1800}
	// Make notes long: answer notes requests with a long reply.
	long := &longNotesClient{windowClient: c}
	if _, _, err := Build(context.Background(), long, "test", longThread(200, 600), ModeDetailed, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(c.systems, "|"), condenseHead) {
		t.Error("notes were never condensed")
	}
}

type longNotesClient struct{ *windowClient }

func (c *longNotesClient) Complete(ctx context.Context, system, user string) (string, error) {
	out, err := c.windowClient.Complete(ctx, system, user)
	if err == nil && strings.HasPrefix(system, notesHead) {
		out += strings.Repeat(" detail", 120)
	}
	return out, err
}

// A conversation that can't fit even in parts fails with advice.
func TestHopelessSummaryExplains(t *testing.T) {
	c := &windowClient{reject: 1} // rejects everything
	_, _, err := Build(context.Background(), c, "test", longThread(20, 200), ModeDetailed, time.Now())
	if err == nil || !strings.Contains(err.Error(), "--context") || !llm.IsContextError(err) {
		t.Fatalf("err = %v", err)
	}
}

// A local model writes a merge's briefs one at a time; a hosted one several.
func TestMergeConcurrencyFollowsLocality(t *testing.T) {
	threads := []domain.Thread{longThread(8, 100), longThread(8, 100), longThread(8, 100), longThread(8, 100)}
	for i := range threads {
		threads[i].ID = fmt.Sprint("t", i)
		for j := range threads[i].Messages {
			threads[i].Messages[j].Timestamp = threads[i].Messages[j].Timestamp.Add(time.Duration(i) * time.Hour)
		}
	}
	local := &windowClient{limits: llm.Limits{Context: 100_000, Local: true}}
	if _, _, err := BuildMerge(context.Background(), local, "test", threads, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := local.maxSeen.Load(); got != 1 {
		t.Errorf("local model got %d requests at once, want 1", got)
	}
	hosted := &windowClient{limits: llm.Limits{Context: 100_000}}
	if _, _, err := BuildMerge(context.Background(), hosted, "test", threads, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := hosted.maxSeen.Load(); got > mergeConcurrency {
		t.Errorf("hosted model got %d requests at once, more than %d", got, mergeConcurrency)
	}
}

// A summary in parts reports each part, then the brief; a merge adds which
// thread it is on.
func TestProgressIsReported(t *testing.T) {
	var mu sync.Mutex
	var seen []Progress
	ctx := WithProgress(context.Background(), func(p Progress) {
		mu.Lock()
		seen = append(seen, p)
		mu.Unlock()
	})
	c := &windowClient{limits: llm.Limits{Context: 4000}}
	if _, _, err := Build(ctx, c, "test", longThread(120, 600), ModeDetailed, time.Now()); err != nil {
		t.Fatal(err)
	}
	first, last := seen[0], seen[len(seen)-1]
	if first.Stage != StageReading || first.Part != 1 || first.Parts < 2 {
		t.Errorf("first report = %+v", first)
	}
	if last.Stage != StageWriting {
		t.Errorf("last report = %+v, want the brief being written", last)
	}
	for i := 1; i < len(seen); i++ {
		if seen[i].Stage == StageReading && seen[i].Part != seen[i-1].Part+1 {
			t.Fatalf("parts out of order: %+v then %+v", seen[i-1], seen[i])
		}
	}

	seen = nil
	threads := []domain.Thread{longThread(8, 100), longThread(8, 100)}
	threads[1].ID = "second"
	for j := range threads[1].Messages {
		threads[1].Messages[j].Timestamp = threads[1].Messages[j].Timestamp.Add(time.Hour)
	}
	local := &windowClient{limits: llm.Limits{Context: 100_000, Local: true}}
	if _, _, err := BuildMerge(ctx, local, "test", threads, time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0].Threads != 2 || seen[0].Thread != 1 || seen[1].Thread != 2 || seen[1].Done != 1 {
		t.Errorf("merge reports = %+v", seen)
	}
}
