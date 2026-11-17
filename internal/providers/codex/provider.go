// Package codex reads sessions written by the OpenAI Codex CLI. Each session
// is a "rollout": a JSONL file under <codex home>/sessions/YYYY/MM/DD whose
// lines are {timestamp, type, payload} records. Codex compresses rollouts it
// has not touched for a while to .jsonl.zst; both forms are read.
package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/zuhailkhan/threadman/internal/domain"
	"github.com/zuhailkhan/threadman/internal/ports"
)

const (
	providerName     = "codex"
	plainSuffix      = ".jsonl"
	compressedSuffix = ".jsonl.zst"
	// Codex puts this marker before the request when it adds context to a
	// user message, and shows only what follows it.
	userMessageBegin = "## My request for Codex:"
)

type Provider struct {
	home string
}

func NewProvider() *Provider {
	return &Provider{home: codexHome()}
}

// codexHome mirrors Codex's own resolution: CODEX_HOME, then ~/.codex.
func codexHome() string {
	if d := os.Getenv("CODEX_HOME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex")
}

func (p *Provider) Name() string { return providerName }

func (p *Provider) sessionsDir() string { return filepath.Join(p.home, "sessions") }

// rolloutName is a parsed rollout file name:
// rollout-YYYY-MM-DDThh-mm-ss-<thread id>[_<rollout id>].jsonl[.zst]
type rolloutName struct {
	stamp      string
	threadID   string
	compressed bool
}

func parseRolloutName(name string) (rolloutName, bool) {
	var r rolloutName
	switch {
	case strings.HasSuffix(name, compressedSuffix):
		r.compressed = true
		name = strings.TrimSuffix(name, compressedSuffix)
	case strings.HasSuffix(name, plainSuffix):
		name = strings.TrimSuffix(name, plainSuffix)
	default:
		return r, false
	}
	core, ok := strings.CutPrefix(name, "rollout-")
	if !ok || len(core) < 21 || core[19] != '-' {
		return r, false
	}
	r.stamp = core[:19]
	// A thread that was reverted gets a new file whose name carries the same
	// thread ID followed by "_" and a separate rollout ID.
	r.threadID, _, _ = strings.Cut(core[20:], "_")
	return r, r.threadID != ""
}

// rolloutFiles returns the file to read for each thread ID: the newest
// rollout, and the plain file when both forms of it exist.
func (p *Provider) rolloutFiles() (map[string]string, error) {
	root := p.sessionsDir()
	if _, err := os.Stat(root); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ports.ErrSourceUnavailable, root)
		}
		return nil, err
	}

	type pick struct {
		path string
		name rolloutName
	}
	best := map[string]pick{}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name, ok := parseRolloutName(d.Name())
		if !ok {
			return nil
		}
		cur, seen := best[name.threadID]
		if !seen || name.stamp > cur.name.stamp ||
			(name.stamp == cur.name.stamp && cur.name.compressed && !name.compressed) {
			best[name.threadID] = pick{path: path, name: name}
		}
		return nil
	})

	out := make(map[string]string, len(best))
	for id, b := range best {
		out[id] = b.path
	}
	return out, nil
}

// threadNames reads session_index.jsonl, an append-only log of thread names
// in which the last entry for an ID wins.
func (p *Provider) threadNames() map[string]string {
	names := map[string]string{}
	f, err := os.Open(filepath.Join(p.home, "session_index.jsonl"))
	if err != nil {
		return names
	}
	defer f.Close()
	_ = eachLine(f, func(line []byte) {
		var e struct {
			ID         string `json:"id"`
			ThreadName string `json:"thread_name"`
		}
		if json.Unmarshal(line, &e) == nil && e.ID != "" {
			names[e.ID] = strings.TrimSpace(e.ThreadName)
		}
	})
	return names
}

func (p *Provider) DiscoverThreads(ctx context.Context) ([]domain.Thread, error) {
	files, err := p.rolloutFiles()
	if err != nil {
		return nil, err
	}
	names := p.threadNames()

	threads := []domain.Thread{}
	for _, path := range files {
		t, err := parse(path, false)
		if err != nil {
			continue
		}
		if name := names[t.OriginalID]; name != "" {
			t.Title = name
		}
		threads = append(threads, t)
	}
	return threads, nil
}

func (p *Provider) GetThreadDetails(ctx context.Context, t domain.Thread) (domain.Thread, error) {
	full, err := parse(t.SourceFilePath, true)
	if err != nil {
		return t, err
	}
	t.Messages = full.Messages
	return t, nil
}

type rolloutLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type sessionMeta struct {
	ID             string          `json:"id"`
	Timestamp      string          `json:"timestamp"`
	CWD            string          `json:"cwd"`
	Source         json.RawMessage `json:"source"`
	ThreadSource   string          `json:"thread_source"`
	ParentThreadID string          `json:"parent_thread_id"`
}

// isUserThread reports whether the session is one the user held, rather than
// one Codex started for itself: a sub-agent, a guardian review or memory
// consolidation.
func (m sessionMeta) isUserThread() bool {
	if m.ParentThreadID != "" {
		return false
	}
	switch m.ThreadSource {
	case "subagent", "guardian_review", "memory_consolidation":
		return false
	}
	// source is a plain string ("cli", "vscode", "exec", ...) for sessions a
	// person started and an object for sub-agent and internal ones.
	if len(m.Source) > 0 && m.Source[0] == '{' {
		return false
	}
	return true
}

type textPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// eventPayload covers the event_msg payloads that carry conversation text.
// Older ("legacy") rollouts record user_message and agent_message events;
// newer ("paginated") ones record item_completed events wrapping the same
// content.
type eventPayload struct {
	Type     string `json:"type"`
	Message  string `json:"message"`
	NumTurns int    `json:"num_turns"`
	Item     struct {
		Type    string     `json:"type"`
		Content []textPart `json:"content"`
	} `json:"item"`
}

type responseItem struct {
	Type    string     `json:"type"`
	Role    string     `json:"role"`
	Content []textPart `json:"content"`
}

func joinText(parts []textPart, kinds ...string) string {
	var sb strings.Builder
	for _, p := range parts {
		for _, k := range kinds {
			if p.Type == k {
				sb.WriteString(p.Text)
				break
			}
		}
	}
	return sb.String()
}

func stripUserPrefix(text string) string {
	if i := strings.Index(text, userMessageBegin); i >= 0 {
		text = text[i+len(userMessageBegin):]
	}
	return strings.TrimSpace(text)
}

func parse(path string, withMessages bool) (domain.Thread, error) {
	r, closeFn, err := openRollout(path)
	if err != nil {
		return domain.Thread{}, err
	}
	defer closeFn()

	var (
		meta     sessionMeta
		haveMeta bool
		// events holds the conversation as Codex showed it. raw holds the
		// messages sent to the model, used only for rollouts without events.
		events, raw []domain.Message
		createdAt   time.Time
		lastAt      time.Time
		lineNo      int
	)
	err = eachLine(r, func(data []byte) {
		lineNo++
		var line rolloutLine
		if json.Unmarshal(data, &line) != nil || line.Type == "" {
			return
		}
		ts := parseTime(line.Timestamp)
		if !ts.IsZero() {
			if createdAt.IsZero() {
				createdAt = ts
			}
			if ts.After(lastAt) {
				lastAt = ts
			}
		}
		id := fmt.Sprintf("line-%d", lineNo)

		switch line.Type {
		case "session_meta":
			// A forked session repeats its parents' metadata after its own.
			if !haveMeta && json.Unmarshal(line.Payload, &meta) == nil && meta.ID != "" {
				haveMeta = true
			}

		case "event_msg":
			var ev eventPayload
			if json.Unmarshal(line.Payload, &ev) != nil {
				return
			}
			role, text := domain.Role(""), ""
			switch ev.Type {
			case "user_message":
				role, text = domain.RoleUser, stripUserPrefix(ev.Message)
			case "agent_message":
				role, text = domain.RoleAssistant, ev.Message
			case "item_completed":
				switch ev.Item.Type {
				case "UserMessage":
					role, text = domain.RoleUser, stripUserPrefix(joinText(ev.Item.Content, "text"))
				case "AgentMessage":
					role, text = domain.RoleAssistant, joinText(ev.Item.Content, "Text")
				}
			case "thread_rolled_back":
				events = dropLastTurns(events, ev.NumTurns)
				return
			}
			if role != "" && strings.TrimSpace(text) != "" {
				events = append(events, domain.Message{ID: id, Role: role, Content: text, Timestamp: ts})
			}

		case "response_item":
			if len(events) > 0 {
				return
			}
			var item responseItem
			if json.Unmarshal(line.Payload, &item) != nil || item.Type != "message" {
				return
			}
			switch item.Role {
			case "user":
				// Codex sends context of its own (environment, instructions)
				// as user messages wrapped in tags.
				text := strings.TrimSpace(joinText(item.Content, "input_text"))
				if text != "" && !strings.HasPrefix(text, "<") {
					raw = append(raw, domain.Message{ID: id, Role: domain.RoleUser, Content: text, Timestamp: ts})
				}
			case "assistant":
				if text := joinText(item.Content, "output_text"); strings.TrimSpace(text) != "" {
					raw = append(raw, domain.Message{ID: id, Role: domain.RoleAssistant, Content: text, Timestamp: ts})
				}
			}
		}
	})
	if err != nil {
		return domain.Thread{}, err
	}
	if !haveMeta {
		return domain.Thread{}, fmt.Errorf("not a codex rollout: %s", path)
	}
	if !meta.isUserThread() {
		return domain.Thread{}, fmt.Errorf("not a user session: %s", path)
	}

	messages := events
	if len(messages) == 0 {
		messages = raw
	}
	title := ""
	for _, m := range messages {
		if m.Role == domain.RoleUser {
			title = truncate(m.Content, 50)
			break
		}
	}
	if ts := parseTime(meta.Timestamp); !ts.IsZero() {
		createdAt = ts
	}
	if !withMessages {
		messages = nil
	}

	return domain.Thread{
		ID:             providerName + "-" + meta.ID,
		Provider:       providerName,
		OriginalID:     meta.ID,
		Title:          title,
		WorkspacePath:  meta.CWD,
		SourceFilePath: path,
		Messages:       messages,
		CreatedAt:      createdAt,
		LastSyncedAt:   lastAt,
	}, nil
}

// dropLastTurns removes the last n user turns, which is what Codex does to the
// conversation when the user rolls it back.
func dropLastTurns(messages []domain.Message, n int) []domain.Message {
	for i := len(messages) - 1; i >= 0 && n > 0; i-- {
		if messages[i].Role == domain.RoleUser {
			messages = messages[:i]
			n--
		}
	}
	return messages
}

// openRollout opens a plain or compressed rollout for reading.
func openRollout(path string) (io.Reader, func(), error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	if !strings.HasSuffix(path, compressedSuffix) {
		return f, func() { f.Close() }, nil
	}
	dec, err := zstd.NewReader(f)
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return dec, func() { dec.Close(); f.Close() }, nil
}

// eachLine calls fn for every non-empty line. Rollout lines can hold inline
// images, so there is no limit on their length.
func eachLine(r io.Reader, fn func(line []byte)) error {
	br := bufio.NewReaderSize(r, 256*1024)
	for {
		line, err := br.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			fn(line)
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-3]) + "..."
}
