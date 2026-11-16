package handoff

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zuhailkhan/threadman/internal/domain"
	"github.com/zuhailkhan/threadman/internal/llm"
)

type fakeLLM struct {
	system, user string
	calls        int
	reply        string
	err          error
}

func (f *fakeLLM) Complete(ctx context.Context, system, user string) (string, error) {
	f.calls++
	f.system, f.user = system, user
	return f.reply, f.err
}

func (f *fakeLLM) Ping(ctx context.Context) error { return nil }

// sampleThread builds a thread of n alternating messages, "user 1",
// "assistant 2", "user 3", and so on.
func sampleThread(n int) domain.Thread {
	t := domain.Thread{
		ID:            "claude-abc",
		Provider:      "claude",
		OriginalID:    "abc",
		Title:         "Fix the parser",
		WorkspacePath: "/work/proj",
	}
	start := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	for i := 1; i <= n; i++ {
		role := domain.RoleUser
		if i%2 == 0 {
			role = domain.RoleAssistant
		}
		t.Messages = append(t.Messages, domain.Message{
			Role:      role,
			Content:   fmt.Sprintf("%s %d", role, i),
			Timestamp: start.Add(time.Duration(i) * time.Minute),
		})
	}
	return t
}

var testNow = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

// buildChecked runs Build and checks the invariants every handoff shares: the
// source is untouched, the output is one handoff block whose content is the
// record's, and the marker names the record.
func buildChecked(t *testing.T, c *fakeLLM, src domain.Thread, mode Mode) (string, domain.Handoff) {
	t.Helper()
	before := append([]domain.Message(nil), src.Messages...)

	var client llm.Client
	if c != nil {
		client = c
	}
	out, h, err := Build(context.Background(), client, "Ollama · llama3.2", src, mode, testNow)
	if err != nil {
		t.Fatal(err)
	}

	for i := range before {
		if !reflect.DeepEqual(src.Messages[i], before[i]) {
			t.Fatal("source thread was modified")
		}
	}
	if out.ID != src.ID || out.WorkspacePath != src.WorkspacePath || out.Title != src.Title {
		t.Errorf("thread metadata not carried over: %+v", out)
	}
	if len(out.Messages) != 1 {
		t.Fatalf("got %d messages, want one handoff block", len(out.Messages))
	}
	m := out.Messages[0]
	if m.Role != domain.RoleUser || m.Kind != domain.KindHandoff || m.HandoffID != h.ID || m.Content != h.Content {
		t.Errorf("block message = %+v, record %+v", m, h)
	}
	if id, _, ok := domain.ParseHandoffMarker(m.Content); !ok || id != h.ID {
		t.Errorf("block does not start with its marker:\n%s", m.Content)
	}
	if h.SourceThreadID != src.ID || h.SourceMessageCount != len(src.Messages) || !h.CreatedAt.Equal(testNow) {
		t.Errorf("record = %+v", h)
	}
	return m.Content, h
}

func assertContains(t *testing.T, s string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(s, w) {
			t.Errorf("missing %q in:\n%s", w, s)
		}
	}
}

func assertOrder(t *testing.T, s string, parts ...string) {
	t.Helper()
	at := 0
	for _, p := range parts {
		i := strings.Index(s[at:], p)
		if i < 0 {
			t.Errorf("%q missing or out of order in:\n%s", p, s)
			return
		}
		at += i + len(p)
	}
}

func TestBuildFullTranscript(t *testing.T) {
	src := sampleThread(4)
	src.Messages[3].ID = "m4"

	block, h := buildChecked(t, nil, src, ModeFull)

	if h.Mode != "full" || h.Summarizer != "" || h.SourceLastMessageID != "m4" {
		t.Errorf("record = %+v", h)
	}
	assertContains(t, block,
		"· from Claude Code · full transcript · 4 messages]",
		"threadman wrote this message, not the user",
		"You wrote none of it",
		"threadman show claude-abc",
	)
	assertOrder(t, block,
		"## Conversation",
		"[User · 2026-11-16 10:01]\nuser 1",
		"[Assistant · Claude Code · 2026-11-16 10:02]\nassistant 2",
		"[User · 2026-11-16 10:03]\nuser 3",
		"[Assistant · Claude Code · 2026-11-16 10:04]\nassistant 4",
	)
	if strings.Contains(block, "## Brief") {
		t.Error("a full transcript has no brief")
	}
}

func TestBuildDetailedSummary(t *testing.T) {
	src := sampleThread(10)
	llm := &fakeLLM{reply: "  ## Goal\nfix tabs  "}

	block, h := buildChecked(t, llm, src, ModeDetailed)

	if h.Mode != "detailed" || h.Summarizer != "Ollama · llama3.2" {
		t.Errorf("record = %+v", h)
	}
	assertContains(t, block,
		"· detailed summary · 10 messages]",
		"first 4 of the conversation's 10 messages",
		"written by a language model (Ollama · llama3.2)",
		"The last 6 messages follow it word for word",
	)
	// The brief, then turns 5-10 word for word and nothing earlier.
	assertOrder(t, block, "## Brief\n\n## Goal\nfix tabs\n", "## Last 6 messages", "user 5", "assistant 10")
	if strings.Contains(block, "]\nuser 3") {
		t.Error("summarized turns were also copied word for word")
	}

	// The model is sent only the older turns.
	assertContains(t, llm.user, "Working directory: /work/proj", "[turn 1 · user · 2026-11-16 10:01]", "assistant 4")
	if strings.Contains(llm.user, "user 5") || strings.Contains(llm.user, "assistant 10") {
		t.Errorf("turns 5-10 are handed over as they are and must not be sent to the model:\n%s", llm.user)
	}
	if llm.system != detailedPrompt {
		t.Error("detailed mode did not use the detailed prompt")
	}
}

func TestBuildLightSummary(t *testing.T) {
	llm := &fakeLLM{reply: "short"}
	block, h := buildChecked(t, llm, sampleThread(10), ModeLight)

	if llm.system != lightPrompt || h.Mode != "light" {
		t.Errorf("light mode: prompt match %v, record %+v", llm.system == lightPrompt, h)
	}
	assertOrder(t, block, "## Brief\n\nshort", "## Last 2 messages", "user 9", "assistant 10")
	if strings.Contains(block, "]\nassistant 8") {
		t.Error("light mode kept more than the last 2 messages")
	}
}

// The kept tail must start on a user message, so it is widened when the
// plain cut would land on an assistant message.
func TestBuildTailStartsOnUser(t *testing.T) {
	src := sampleThread(9) // ends on a user message; last 6 would start on assistant 4
	block, _ := buildChecked(t, &fakeLLM{reply: "s"}, src, ModeDetailed)
	assertContains(t, block, "first 2 of the conversation's 9 messages", "## Last 7 messages")
	assertOrder(t, block, "## Last 7 messages", "]\nuser 3")
}

// A thread with nothing older than the kept tail is handed over whole, and
// recorded as a full transcript.
func TestBuildShortThreadSkipsLLM(t *testing.T) {
	llm := &fakeLLM{reply: "unused"}
	block, h := buildChecked(t, llm, sampleThread(6), ModeDetailed)

	if llm.calls != 0 {
		t.Error("the LLM was called for a thread with nothing to summarize")
	}
	if h.Mode != "full" || h.Summarizer != "" {
		t.Errorf("record = %+v, want a full transcript with no summarizer", h)
	}
	assertContains(t, block, "· full transcript · 6 messages]", "## Conversation", "user 1")
}

// A block the source itself received is carried as a labelled account, and
// its marker does not survive, so it can't be mistaken for this handoff.
func TestBuildCarriesEarlierHandoff(t *testing.T) {
	src := sampleThread(2)
	earlier := domain.HandoffMarker("old-id", "from OpenCode · full transcript · 3 messages") + "\n\nearlier body"
	src.Messages = append([]domain.Message{{Role: domain.RoleUser, Content: earlier, Kind: domain.KindHandoff, HandoffID: "old-id"}}, src.Messages...)

	block, _ := buildChecked(t, nil, src, ModeFull)

	assertContains(t, block, "[Handoff received · from OpenCode · full transcript · 3 messages]\nearlier body")
	if strings.Contains(block, "old-id") {
		t.Errorf("the earlier marker was copied into the new block:\n%s", block)
	}
}

func TestBuildError(t *testing.T) {
	boom := errors.New("boom")
	_, _, err := Build(context.Background(), &fakeLLM{err: boom}, "", sampleThread(10), ModeLight, testNow)
	if !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
}

func TestSummarizerWithoutConfig(t *testing.T) {
	s := NewSummarizer(filepath.Join(t.TempDir(), "config.json"))

	st := s.Check(context.Background())
	if st.Configured || st.Ready() || st.Err != nil {
		t.Errorf("Check = %+v, want not configured and no error", st)
	}

	src := sampleThread(10)
	out, h, err := s.Prepare(context.Background(), src, ModeFull)
	if err != nil || h.Mode != "full" || len(out.Messages) != 1 {
		t.Errorf("full transcript should work without an LLM: %v, %+v", err, h)
	}
	if _, _, err := s.Prepare(context.Background(), src, ModeDetailed); err == nil {
		t.Error("summary without an LLM should fail")
	}
}

// mixedThread is a conversation gathered from two tools: each message names
// the tool it was written in.
func mixedThread() domain.Thread {
	src := sampleThread(4)
	src.Messages[0].Provider = "claude"
	src.Messages[1].Provider = "claude"
	src.Messages[2].Provider = "opencode"
	src.Messages[3].Provider = "opencode"
	return src
}

func TestBuildLabelsEachTurnWithItsTool(t *testing.T) {
	block, _ := buildChecked(t, nil, mixedThread(), ModeFull)

	assertContains(t, block, "· from Claude Code and OpenCode · full transcript · 4 messages]")
	assertOrder(t, block,
		"[User · 2026-11-16 10:01]",
		"[Assistant · Claude Code · 2026-11-16 10:02]",
		"[User · 2026-11-16 10:03]",
		"[Assistant · OpenCode · 2026-11-16 10:04]",
	)
}

func TestBuildCatchUp(t *testing.T) {
	src := sampleThread(2)
	src.Provider = "opencode"

	out, h, err := BuildCatchUp(context.Background(), nil, "", src, ModeFull, testNow)
	if err != nil {
		t.Fatal(err)
	}
	block := out.Messages[0].Content
	if out.Messages[0].Kind != domain.KindHandoff || h.Mode != "full" {
		t.Errorf("got %+v, record %+v", out.Messages[0], h)
	}
	assertContains(t, block,
		"· from OpenCode · full transcript · 2 new messages]",
		"This conversation continued in OpenCode after it was last here",
		"You wrote none of it",
	)
	if strings.Contains(block, "carries over a conversation that took place") {
		t.Errorf("a catch-up block described itself as a whole conversation:\n%s", block)
	}
}

func TestBuildCatchUpSummary(t *testing.T) {
	llm := &fakeLLM{reply: "brief"}
	out, _, err := BuildCatchUp(context.Background(), llm, "Ollama · llama3.2", sampleThread(10), ModeDetailed, testNow)
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, out.Messages[0].Content, "· detailed summary · 10 new messages]", "first 4 of the 10 new messages")
	assertContains(t, llm.user, "already knows this conversation up to the start of the transcript")
}

func toolThread() domain.Thread {
	src := sampleThread(4)
	src.Messages[1].Tools = []domain.ToolCall{
		{Name: "Bash", Input: "go test ./...", Output: "ok  example.com/p\n" + strings.Repeat("z", 2000)},
		{Name: "Read", Input: `{"file_path":"a.go"}`, Output: "no such file", Error: true},
	}
	src.Messages[3].Content = ""
	src.Messages[3].Tools = []domain.ToolCall{{Name: "Glob", Input: `{"pattern":"*.go"}`, Output: "a.go"}}
	return src
}

func TestBuildCarriesToolCalls(t *testing.T) {
	block, _ := buildChecked(t, nil, toolThread(), ModeFull)

	assertOrder(t, block,
		"[Assistant · Claude Code · 2026-11-16 10:02]\nassistant 2\n\n→ Bash: go test ./...\n    ok  example.com/p\n",
		"→ Read (failed): {\"file_path\":\"a.go\"}\n    no such file\n",
		// A turn that was only a tool call keeps its label and its call.
		"[Assistant · Claude Code · 2026-11-16 10:04]\n→ Glob: {\"pattern\":\"*.go\"}\n    a.go\n",
	)
	// Each result is cut to the block's budget, not the stored one.
	if !strings.Contains(block, fmt.Sprintf("… (%d more characters)", len("ok  example.com/p\n")+2000-blockToolOutput)) {
		t.Errorf("long output was not cut to %d characters:\n%s", blockToolOutput, block)
	}
}

func TestSummarizerSeesToolCalls(t *testing.T) {
	src := toolThread()
	src.Messages = append(src.Messages, sampleThread(10).Messages[4:]...)
	llm := &fakeLLM{reply: "brief"}
	if _, _, err := Build(context.Background(), llm, "", src, ModeDetailed, testNow); err != nil {
		t.Fatal(err)
	}
	assertContains(t, llm.user, "→ Bash: go test ./...", "→ Read (failed)")
	if strings.Count(llm.user, "z") > briefToolOutput {
		t.Errorf("the summarizer was sent more than %d characters of output", briefToolOutput)
	}
}

// A conversation whose tool output would make the block too large keeps every
// call but less of each result, and says so.
func TestBuildShrinksToolOutputToFit(t *testing.T) {
	src := sampleThread(2)
	out := strings.Repeat("o", 1000)
	for i := 0; i < 1000; i++ {
		src.Messages[1].Tools = append(src.Messages[1].Tools, domain.ToolCall{Name: "Bash", Input: fmt.Sprintf("step %d", i), Output: out})
	}

	block, _ := buildChecked(t, nil, src, ModeFull)

	if len(block) > maxBlockBytes {
		t.Errorf("block is %d bytes, over the %d budget", len(block), maxBlockBytes)
	}
	if !strings.Contains(block, "→ Bash: step 0\n") || !strings.Contains(block, "→ Bash: step 999") {
		t.Error("calls were dropped instead of their output")
	}
	if !strings.Contains(block, "cut to 200 characters to keep this message small") {
		t.Errorf("the block does not say its output was cut:\n%.600s", block)
	}
}

func TestBuildLeavesOutputOutWhenNothingElseFits(t *testing.T) {
	src := sampleThread(2)
	for i := 0; i < 4000; i++ {
		src.Messages[1].Tools = append(src.Messages[1].Tools, domain.ToolCall{Name: "Bash", Input: fmt.Sprintf("step %d", i), Output: strings.Repeat("o", 500)})
	}
	block, _ := buildChecked(t, nil, src, ModeFull)
	if !strings.Contains(block, "Their output is left out") || strings.Contains(block, "    ooo") {
		t.Errorf("output should be left out entirely:\n%.600s", block)
	}
}
