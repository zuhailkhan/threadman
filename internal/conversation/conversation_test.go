package conversation

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/zuhailkhan/threadman/internal/domain"
)

var t0 = time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)

func at(minute int) time.Time { return t0.Add(time.Duration(minute) * time.Minute) }

func msg(minute int, content string) domain.Message {
	role := domain.RoleUser
	if minute%2 == 0 {
		role = domain.RoleAssistant
	}
	return domain.Message{ID: content, Role: role, Content: content, Timestamp: at(minute)}
}

func block(minute int, id string) domain.Message {
	return domain.Message{ID: id, Role: domain.RoleUser, Content: domain.HandoffMarker(id, "x"), Timestamp: at(minute), Kind: domain.KindHandoff, HandoffID: id}
}

func contents(entries []Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Message.Content
	}
	return out
}

func assertMissed(t *testing.T, threads []domain.Thread, provider string, want ...string) {
	t.Helper()
	got := contents(Missed(Timeline(threads), provider))
	if !slices.Equal(got, want) && !(len(got) == 0 && len(want) == 0) {
		t.Errorf("%s missed %v, want %v", provider, got, want)
	}
}

// Claude starts, hands off to OpenCode, OpenCode adds turns.
func handedOff() (claude, opencode domain.Thread) {
	claude = domain.Thread{ID: "claude-1", Provider: "claude", Messages: []domain.Message{msg(1, "c1"), msg(2, "c2")}}
	opencode = domain.Thread{ID: "opencode-1", Provider: "opencode", Messages: []domain.Message{block(3, "h1"), msg(5, "o5"), msg(6, "o6")}}
	return claude, opencode
}

func TestTimelineMergesInTimeOrder(t *testing.T) {
	claude, opencode := handedOff()
	claude.Messages = append(claude.Messages, msg(4, "c4"))

	got := Timeline([]domain.Thread{opencode, claude})

	want := []string{"c1", "c2", domain.HandoffMarker("h1", "x"), "c4", "o5", "o6"}
	if !slices.Equal(contents(got), want) {
		t.Fatalf("timeline %v, want %v", contents(got), want)
	}
	if got[0].Provider != "claude" || got[0].ThreadID != "claude-1" || got[2].Provider != "opencode" {
		t.Errorf("entries lost their session: %+v", got)
	}
}

func TestTimelineKeepsSessionOrderOnTies(t *testing.T) {
	a := domain.Thread{Provider: "claude", Messages: []domain.Message{{Content: "a1"}, {Content: "a2"}}}
	b := domain.Thread{Provider: "pi", Messages: []domain.Message{{Content: "b1"}}}
	if got := contents(Timeline([]domain.Thread{a, b})); !slices.Equal(got, []string{"a1", "a2", "b1"}) {
		t.Errorf("got %v", got)
	}
}

func TestMissedAfterHandoff(t *testing.T) {
	claude, opencode := handedOff()
	threads := []domain.Thread{claude, opencode}

	// The origin received nothing, so it missed everything OpenCode wrote.
	assertMissed(t, threads, "claude", "o5", "o6")
	// OpenCode's block carried Claude's turns.
	assertMissed(t, threads, "opencode")
	// A tool with no session gets every message, but not the block.
	assertMissed(t, threads, "pi", "c1", "c2", "o5", "o6")
}

// The user kept going in Claude after handing off. Claude still missed all of
// OpenCode's turns, including ones older than its own newest message.
func TestMissedWhenBothSidesMovedOn(t *testing.T) {
	claude, opencode := handedOff()
	claude.Messages = append(claude.Messages, msg(7, "c7"))
	opencode.Messages = append(opencode.Messages, msg(8, "o8"))
	threads := []domain.Thread{claude, opencode}

	assertMissed(t, threads, "claude", "o5", "o6", "o8")
	assertMissed(t, threads, "opencode", "c7")
}

// After the turns are carried back to Claude, it is caught up; only what
// happens next counts.
func TestMissedAfterRoundTrip(t *testing.T) {
	claude, opencode := handedOff()
	claude.Messages = append(claude.Messages, block(7, "h2"), msg(9, "c9"))
	threads := []domain.Thread{claude, opencode}

	assertMissed(t, threads, "claude")
	assertMissed(t, threads, "opencode", "c9")
}

func TestSessionPicksTheProvidersNewest(t *testing.T) {
	old := domain.Thread{ID: "claude-old", Provider: "claude", Messages: []domain.Message{msg(1, "a")}}
	recent := domain.Thread{ID: "claude-new", Provider: "claude", Messages: []domain.Message{msg(9, "b")}}
	pi := domain.Thread{ID: "pi-1", Provider: "pi", Messages: []domain.Message{msg(20, "c")}}

	got, ok := Session([]domain.Thread{recent, pi, old}, "claude")
	if !ok || got.ID != "claude-new" {
		t.Errorf("Session = %q, %v", got.ID, ok)
	}
	if _, ok := Session([]domain.Thread{old}, "opencode"); ok {
		t.Error("found a session for a provider with none")
	}
}

func TestHead(t *testing.T) {
	claude, opencode := handedOff()
	if h, ok := Head([]domain.Thread{claude, opencode}); !ok || h.ID != "opencode-1" {
		t.Errorf("Head = %q", h.ID)
	}
	if _, ok := Head(nil); ok {
		t.Error("an empty group has no head")
	}
}

func TestMessagesTagsProvider(t *testing.T) {
	claude, opencode := handedOff()
	msgs := Messages(Missed(Timeline([]domain.Thread{claude, opencode}), "pi"))
	var got []string
	for _, m := range msgs {
		got = append(got, fmt.Sprintf("%s:%s", m.Provider, m.Content))
	}
	want := []string{"claude:c1", "claude:c2", "opencode:o5", "opencode:o6"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSessionSkipsSessionsTheToolDeleted(t *testing.T) {
	gone := domain.Thread{ID: "claude-gone", Provider: "claude", SourceMissing: true, Messages: []domain.Message{msg(9, "newest")}}
	live := domain.Thread{ID: "claude-live", Provider: "claude", Messages: []domain.Message{msg(1, "older")}}

	if got, ok := Session([]domain.Thread{gone, live}, "claude"); !ok || got.ID != "claude-live" {
		t.Errorf("Session = %q, %v; want the live session", got.ID, ok)
	}
	if _, ok := Session([]domain.Thread{gone}, "claude"); ok {
		t.Error("a deleted session was offered for continuing")
	}
}

func TestEverythingLeavesOutBlocks(t *testing.T) {
	claude, opencode := handedOff()
	got := contents(Everything(Timeline([]domain.Thread{claude, opencode})))
	if want := []string{"c1", "c2", "o5", "o6"}; !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// A merge block holds briefs of threads outside the conversation, so a tool
// handed a merged session needs it; an ordinary block only repeats messages.
func TestMergeBlockIsCarried(t *testing.T) {
	merge := domain.Message{ID: "m", Role: domain.RoleUser, Timestamp: at(1), Kind: domain.KindHandoff, HandoffID: "m",
		Content: domain.HandoffMarker("m", domain.MergeDetail(2, "Claude Code and pi", 9)) + "\nbriefs"}
	pi := domain.Thread{ID: "pi-1", Provider: "pi", Messages: []domain.Message{merge, msg(2, "p2"), msg(3, "p3")}}
	claude := domain.Thread{ID: "claude-1", Provider: "claude", Messages: []domain.Message{block(4, "h1"), msg(5, "c5")}}
	threads := []domain.Thread{pi, claude}

	got := contents(Everything(Timeline(threads)))
	if want := []string{merge.Content, "p2", "p3", "c5"}; !slices.Equal(got, want) {
		t.Errorf("Everything = %q, want %q", got, want)
	}
	assertMissed(t, []domain.Thread{pi}, "opencode", merge.Content, "p2", "p3")
}
