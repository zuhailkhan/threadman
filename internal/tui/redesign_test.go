package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/zuhailkhan/threadman/internal/domain"
	"github.com/zuhailkhan/threadman/internal/handoff"
	"github.com/zuhailkhan/threadman/internal/ports"
	syncsvc "github.com/zuhailkhan/threadman/internal/sync"
)

func listModel(t *testing.T, threads ...domain.Thread) Model {
	t.Helper()
	m := Model{state: stateList, width: 120, height: 30, threads: threads,
		summarizer: handoff.NewSummarizer(filepath.Join(t.TempDir(), "config.json"))}
	m.groups = buildGroups(threads)
	m.applyFilter()
	return m
}

func thread(id, provider, title string, at time.Time, content ...string) domain.Thread {
	t := domain.Thread{ID: id, Provider: provider, Title: title, CreatedAt: at}
	for i, c := range content {
		t.Messages = append(t.Messages, domain.Message{ID: fmt.Sprint(i), Role: domain.RoleUser, Content: c, Timestamp: at})
	}
	return t
}

func TestTabCyclesToolFilter(t *testing.T) {
	now := time.Now()
	m := listModel(t,
		thread("claude-1", "claude", "one", now, "a"),
		thread("claude-2", "claude", "two", now, "b"),
		thread("pi-1", "pi", "three", now, "c"))

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = asModel(next)
	if m.toolFilter != "claude" || len(m.filteredGroups) != 2 {
		t.Fatalf("after tab: filter %q, %d rows", m.toolFilter, len(m.filteredGroups))
	}
	if v := m.View(); !strings.Contains(v, "2 of 3") {
		t.Errorf("filtered list should say how many it shows:\n%s", v)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = asModel(next)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = asModel(next)
	if m.toolFilter != "" || len(m.filteredGroups) != 3 {
		t.Fatalf("tab should wrap back to all: filter %q, %d rows", m.toolFilter, len(m.filteredGroups))
	}
}

func TestListGroupsByDate(t *testing.T) {
	now := time.Now()
	m := listModel(t,
		thread("a", "claude", "fresh", now, "x"),
		thread("b", "claude", "old", now.AddDate(0, -3, 0), "y"))
	v := m.View()
	today, earlier := strings.Index(v, "TODAY"), strings.Index(v, "EARLIER")
	if today < 0 || earlier < 0 || today > earlier {
		t.Errorf("want TODAY then EARLIER headings:\n%s", v)
	}
}

func TestSearchMatchesMessagesAndShowsWhere(t *testing.T) {
	now := time.Now()
	m := listModel(t,
		thread("a", "claude", "parser work", now, "the tokenizer drops unicode quotes"),
		thread("b", "pi", "other", now, "nothing here"))
	m, _ = press(t, m, "/")
	for _, r := range "tokenizer" {
		m, _ = press(t, m, string(r))
	}
	if len(m.filteredGroups) != 1 || m.filteredGroups[0].Primary.ID != "a" {
		t.Fatalf("search should find the thread by its message: %+v", m.filteredGroups)
	}
	if v := m.View(); !strings.Contains(v, "tokenizer drops unicode") {
		t.Errorf("row should show the matching text:\n%s", v)
	}
}

func TestHelpOverlay(t *testing.T) {
	m := listModel(t, thread("a", "claude", "one", time.Now(), "x"))
	m, _ = press(t, m, "?")
	if !m.help || !strings.Contains(m.View(), "prev/next prompt") {
		t.Fatalf("? should show every key:\n%s", m.View())
	}
	// Keys go to the overlay, not the list.
	m, cmd := press(t, m, "q")
	if m.help || cmd != nil {
		t.Errorf("q should close the overlay without quitting (help %v)", m.help)
	}
}

func TestReadOnlyToolsCantBeDeletedOrMergedInto(t *testing.T) {
	now := time.Now()
	root := thread("claude-a", "claude", "t", now, "x")
	root.CanonicalID = "claude-a"
	agy := thread("antigravity-a", "antigravity", "t", now, "x")
	agy.CanonicalID = "claude-a"
	m := listModel(t, root, agy)
	m.writers = []ports.ThreadWriter{&fakeWriter{name: "claude"}, &readOnlyWriter{fakeWriter{name: "antigravity"}}}

	if got := m.targetWriters(); len(got) != 1 || got[0].Name() != "claude" {
		t.Errorf("merge targets = %v", got)
	}

	m, _ = press(t, m, "d")
	m, _ = press(t, m, "a")
	for i, target := range m.deleteTargets {
		if target.Provider == "antigravity" && m.deleteSelected[i] {
			t.Error("a read-only session was selected for deletion")
		}
	}
	if v := m.View(); !strings.Contains(v, "read-only") {
		t.Errorf("delete dialog should mark the read-only copy:\n%s", v)
	}
}

func TestOnboardingInstallsHooksAfterImport(t *testing.T) {
	m := Model{state: stateOnboarding, width: 100, height: 30, onboardingHooks: true,
		onboardingProviders: []onboardingProvider{{name: "claude", selected: true}}}
	if v := m.View(); !strings.Contains(v, "Import after every reply") {
		t.Fatalf("first run should offer live updates:\n%s", v)
	}

	// Space on the last line turns the hooks off.
	m, _ = press(t, m, "j")
	m, _ = press(t, m, " ")
	if m.onboardingHooks {
		t.Fatal("hooks still on after toggling")
	}

	next, cmd := m.Update(syncDoneMsg{results: []syncsvc.SyncResult{{Provider: "claude"}}})
	if got := asModel(next); got.state != stateList || cmd == nil {
		t.Errorf("without hooks the list should load: state %v", got.state)
	}

	m.onboardingHooks = true
	next, _ = m.Update(syncDoneMsg{results: []syncsvc.SyncResult{{Provider: "claude"}}})
	if got := asModel(next); got.state != stateOnboarding || !strings.Contains(got.status, "hooks") {
		t.Errorf("with hooks they should be installed next: state %v, status %q", got.state, got.status)
	}
}

func TestListScrollsToKeepCursorVisible(t *testing.T) {
	now := time.Now()
	var threads []domain.Thread
	for i := 0; i < 40; i++ {
		threads = append(threads, thread(fmt.Sprintf("t%d", i), "claude", fmt.Sprintf("thread %02d", i), now, "x"))
	}
	m := listModel(t, threads...)
	for i := 0; i < 30; i++ {
		m, _ = press(t, m, "j")
	}
	if v := m.View(); !strings.Contains(v, "thread 30") {
		t.Errorf("the cursor's row is off screen (top %d)", m.listTop)
	}
	m, _ = press(t, m, "g")
	if m.cursor != 0 || m.listTop != 0 {
		t.Errorf("g should go to the top: cursor %d, top %d", m.cursor, m.listTop)
	}
}

func TestReaderJumpsBetweenPrompts(t *testing.T) {
	now := time.Now()
	th := domain.Thread{ID: "a", Provider: "claude"}
	for i := 0; i < 6; i++ {
		role := domain.RoleUser
		if i%2 == 1 {
			role = domain.RoleAssistant
		}
		th.Messages = append(th.Messages, domain.Message{ID: fmt.Sprint(i), Role: role, Timestamp: now,
			Content: strings.Repeat(fmt.Sprintf("message %d\n", i), 8)})
	}
	m := Model{state: stateThread, thread: th}
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 20})
	m = asModel(next)
	next, _ = m.Update(groupLoadedMsg{primary: th, members: []domain.Thread{th}, initial: true})
	m = asModel(next)
	if len(m.prompts) != 3 {
		t.Fatalf("prompts = %d, want 3", len(m.prompts))
	}

	m.viewport.GotoTop()
	m, _ = press(t, m, "]")
	if m.viewport.YOffset != m.prompts[1].line || m.activePrompt() != 1 {
		t.Errorf("] went to line %d, want %d", m.viewport.YOffset, m.prompts[1].line)
	}
	m, _ = press(t, m, "[")
	if m.viewport.YOffset != 0 {
		t.Errorf("[ went to line %d, want the top", m.viewport.YOffset)
	}
}

// A session a handoff created is titled from its block's marker line; the
// list names it by its first prompt instead, or by what the block carries.
func TestHandoffSessionTitle(t *testing.T) {
	marker := domain.HandoffMarker("c6fcaae5", "from OpenCode · full transcript · 3 messages")
	withPrompt := domain.Thread{Title: marker, Messages: []domain.Message{
		{Role: domain.RoleUser, Content: marker + "\n\n[User]\nhi"},
		{Role: domain.RoleAssistant, Content: "ok"},
		{Role: domain.RoleUser, Content: "what did the other assistant run?"},
	}}
	if got := displayTitle(withPrompt); got != "what did the other assistant run?" {
		t.Errorf("with a prompt: %q", got)
	}
	blockOnly := domain.Thread{Title: marker, Messages: []domain.Message{{Role: domain.RoleUser, Content: marker, Kind: domain.KindHandoff}}}
	if got := displayTitle(blockOnly); got != "Handoff from OpenCode · full transcript · 3 messages" {
		t.Errorf("block only: %q", got)
	}
	renamed := withPrompt
	renamed.CustomTitle = "mine"
	if got := displayTitle(renamed); got != "mine" {
		t.Errorf("a custom title wins: %q", got)
	}
}

func TestCountsArePlural(t *testing.T) {
	if plural(1, "msg") != "1 msg" || plural(2, "msg") != "2 msgs" || plural(0, "thread") != "0 threads" {
		t.Error("plural")
	}
}
