package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/zuhailkhan/threadman/internal/domain"
)

type Writer struct {
	home string
}

func NewWriter() *Writer {
	return &Writer{home: codexHome()}
}

func (w *Writer) Name() string { return providerName }

// WriteThread writes a rollout in Codex's "legacy" history mode, which is the
// default for a session without a history_mode. Every message is written
// twice, as Codex does: a response_item, which is what the model is sent on
// resume, and an event_msg, which is what the session list and the transcript
// view read.
func (w *Writer) WriteThread(ctx context.Context, thread domain.Thread) (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	sessionID := id.String()
	now := time.Now()

	// Codex files rollouts by local date and stamps the name in local time.
	dir := filepath.Join(w.home, "sessions", now.Format("2006"), now.Format("01"), now.Format("02"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create sessions dir: %w", err)
	}
	path := filepath.Join(dir, "rollout-"+now.Format("2006-01-02T15-04-05")+"-"+sessionID+plainSuffix)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("create rollout file: %w", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)

	if err := enc.Encode(map[string]any{"timestamp": isoTime(now), "type": "session_meta", "payload": map[string]any{
		"session_id":        sessionID,
		"id":                sessionID,
		"timestamp":         isoTime(now),
		"cwd":               thread.WorkspacePath,
		"originator":        "threadman",
		"cli_version":       "0.0.0",
		"source":            "cli",
		"model_provider":    sessionModelProvider(w.home),
		"base_instructions": nil,
	}}); err != nil {
		return "", err
	}

	if err := writeMessages(enc, thread.Messages, now); err != nil {
		return "", err
	}
	return sessionID, nil
}

func writeMessages(enc *json.Encoder, messages []domain.Message, now time.Time) error {
	for _, msg := range messages {
		ts := msg.Timestamp
		if ts.IsZero() {
			ts = now
		}
		var item, event map[string]any
		switch msg.Role {
		case domain.RoleUser:
			item = messageItem("user", "input_text", msg.Content)
			event = map[string]any{"type": "user_message", "message": msg.Content, "kind": "plain"}
		case domain.RoleAssistant:
			item = messageItem("assistant", "output_text", msg.Content)
			event = map[string]any{"type": "agent_message", "message": msg.Content}
		default:
			continue
		}
		if err := enc.Encode(map[string]any{"timestamp": isoTime(ts), "type": "response_item", "payload": item}); err != nil {
			return err
		}
		if err := enc.Encode(map[string]any{"timestamp": isoTime(ts), "type": "event_msg", "payload": event}); err != nil {
			return err
		}
	}
	return nil
}

// AppendMessages adds messages to the end of an existing rollout, so
// `codex resume` continues the same session with them as its latest history.
// They are written in the history mode the rollout already uses.
func (w *Writer) AppendMessages(ctx context.Context, target domain.Thread, messages []domain.Message) error {
	path := target.SourceFilePath
	if path == "" {
		return fmt.Errorf("codex session %s has no rollout file", target.OriginalID)
	}
	state, err := readRolloutState(path)
	if err != nil {
		return fmt.Errorf("read rollout: %w", err)
	}

	// Codex appends only to plain files, and restores a compressed rollout
	// to plain before it does. Do the same.
	if strings.HasSuffix(path, compressedSuffix) {
		plain := strings.TrimSuffix(path, ".zst")
		if err := decompressTo(path, plain); err != nil {
			return fmt.Errorf("restore compressed rollout: %w", err)
		}
		path = plain
	}

	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0)
	if err != nil {
		return fmt.Errorf("open rollout file: %w", err)
	}
	defer f.Close()

	if info, err := f.Stat(); err == nil && info.Size() > 0 {
		last := make([]byte, 1)
		if _, err := f.ReadAt(last, info.Size()-1); err == nil && last[0] != '\n' {
			if _, err := f.Write([]byte{'\n'}); err != nil {
				return err
			}
		}
	}
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	if state.paginated {
		err = writePaginated(enc, messages, state, time.Now())
	} else {
		err = writeMessages(enc, messages, time.Now())
	}
	if err != nil {
		return err
	}
	return f.Close()
}

// writePaginated encodes messages for a rollout in the paginated history
// mode. Such a rollout numbers every record, and holds the conversation as
// item_completed events grouped into turns, so the messages are written as
// one finished turn that continues the numbering.
func writePaginated(enc *json.Encoder, messages []domain.Message, state rolloutState, now time.Time) error {
	turn, err := uuid.NewV7()
	if err != nil {
		return err
	}
	turnID := turn.String()
	ordinal := state.nextOrdinal
	write := func(ts time.Time, kind string, payload map[string]any) error {
		err := enc.Encode(map[string]any{"timestamp": isoTime(ts), "ordinal": ordinal, "type": kind, "payload": payload})
		ordinal++
		return err
	}

	if err := write(now, "event_msg", map[string]any{
		"type": "task_started", "turn_id": turnID, "root_turn_id": turnID,
		"started_at": now.Unix(), "model_context_window": nil,
	}); err != nil {
		return err
	}

	var lastAgent any
	for _, msg := range messages {
		ts := msg.Timestamp
		if ts.IsZero() {
			ts = now
		}
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		var item, turnItem map[string]any
		switch msg.Role {
		case domain.RoleUser:
			item = messageItem("user", "input_text", msg.Content)
			turnItem = map[string]any{"type": "UserMessage", "id": id.String(),
				"content": []map[string]any{{"type": "text", "text": msg.Content, "text_elements": []any{}}}}
		case domain.RoleAssistant:
			item = messageItem("assistant", "output_text", msg.Content)
			turnItem = map[string]any{"type": "AgentMessage", "id": id.String(),
				"content": []map[string]any{{"type": "Text", "text": msg.Content}}}
			lastAgent = msg.Content
		default:
			continue
		}
		if err := write(ts, "response_item", item); err != nil {
			return err
		}
		if err := write(ts, "event_msg", map[string]any{
			"type": "item_completed", "thread_id": state.threadID, "turn_id": turnID, "item": turnItem,
		}); err != nil {
			return err
		}
	}

	return write(now, "event_msg", map[string]any{
		"type": "task_complete", "turn_id": turnID, "root_turn_id": turnID,
		"last_agent_message": lastAgent, "started_at": now.Unix(), "completed_at": now.Unix(),
	})
}

// rolloutState is what appending to a rollout needs to know about it.
type rolloutState struct {
	threadID    string
	paginated   bool
	nextOrdinal uint64
}

func readRolloutState(path string) (rolloutState, error) {
	r, closeFn, err := openRollout(path)
	if err != nil {
		return rolloutState{}, err
	}
	defer closeFn()

	var state rolloutState
	haveMeta := false
	err = eachLine(r, func(data []byte) {
		var line struct {
			rolloutLine
			Ordinal *uint64 `json:"ordinal"`
		}
		if json.Unmarshal(data, &line) != nil {
			return
		}
		if line.Ordinal != nil && *line.Ordinal >= state.nextOrdinal {
			state.nextOrdinal = *line.Ordinal + 1
		}
		if line.Type == "session_meta" && !haveMeta {
			var meta struct {
				ID          string `json:"id"`
				HistoryMode string `json:"history_mode"`
			}
			if json.Unmarshal(line.Payload, &meta) == nil && meta.ID != "" {
				haveMeta = true
				state.threadID = meta.ID
				state.paginated = meta.HistoryMode == "paginated"
			}
		}
	})
	if err != nil {
		return rolloutState{}, err
	}
	if !haveMeta {
		return rolloutState{}, fmt.Errorf("not a codex rollout: %s", path)
	}
	return state, nil
}

// decompressTo writes the plain form of a compressed rollout to dst, which
// must not exist yet.
func decompressTo(src, dst string) error {
	r, closeFn, err := openRollout(src)
	if err != nil {
		return err
	}
	defer closeFn()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}

func messageItem(role, partType, text string) map[string]any {
	return map[string]any{
		"type":    "message",
		"role":    role,
		"content": []map[string]any{{"type": partType, "text": text}},
	}
}

func (w *Writer) OpenCommand(sessionID string, workspacePath string) *exec.Cmd {
	cmd := exec.Command("codex", "resume", sessionID)
	cmd.Dir = workspacePath
	return cmd
}

func (w *Writer) DeleteThread(ctx context.Context, thread domain.Thread) error {
	return os.Remove(thread.SourceFilePath)
}

func isoTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}
