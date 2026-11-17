package domain

import (
	"fmt"
	"strings"
	"time"
)

// MessageKind separates what a session's participants wrote from what
// threadman inserted into it.
type MessageKind string

const (
	// KindMessage is an ordinary message written in the session's own tool.
	KindMessage MessageKind = ""
	// KindHandoff is a block threadman wrote into a session to carry a
	// conversation over from another tool.
	KindHandoff MessageKind = "handoff"
)

// Handoff records one transfer of a conversation into another tool: what was
// sent, where, and how much of the source it covered.
type Handoff struct {
	ID string

	SourceThreadID string

	// MergedFrom lists, oldest first, the threads a merge brought together
	// into one new session; SourceThreadID is then the newest of them. It is
	// empty for an ordinary handoff.
	MergedFrom []string

	TargetProvider   string
	TargetOriginalID string

	// Mode is how the source was carried: "full", "detailed", "light", or
	// "merge" for several threads summarized into one new session.
	Mode string

	// Summarizer names the LLM that wrote the brief; empty for a full transcript.
	Summarizer string

	// SourceMessageCount and SourceLastMessageID mark how far into the source
	// the handoff reached.
	SourceMessageCount  int
	SourceLastMessageID string

	// Content is the exact text written into the target session.
	Content string

	CreatedAt time.Time
}

// MergedTitle names the session a merge of n threads writes, after the
// newest thread's title.
func MergedTitle(newest string, n int) string {
	if newest == "" {
		return fmt.Sprintf("Merged from %d threads", n)
	}
	return fmt.Sprintf("%s (merged from %d)", newest, n)
}

// TargetThreadID is the ID the target session gets once it is synced.
func (h Handoff) TargetThreadID() string {
	return h.TargetProvider + "-" + h.TargetOriginalID
}

const (
	handoffMarkerPrefix = "[threadman handoff "
	handoffMarkerSep    = " · "
)

// HandoffMarker is the first line of every handoff block. It carries the
// handoff ID so the block is recognised when the target session is synced
// back, and a short human-readable description.
func HandoffMarker(id, detail string) string {
	if detail == "" {
		return handoffMarkerPrefix + id + "]"
	}
	return handoffMarkerPrefix + id + handoffMarkerSep + detail + "]"
}

// ParseHandoffMarker reports whether content starts with a handoff marker,
// and returns the handoff ID and description from it.
func ParseHandoffMarker(content string) (id, detail string, ok bool) {
	line, _, _ := strings.Cut(content, "\n")
	line = strings.TrimRight(line, "\r")
	if !strings.HasPrefix(line, handoffMarkerPrefix) || !strings.HasSuffix(line, "]") {
		return "", "", false
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(line, handoffMarkerPrefix), "]")
	id, detail, _ = strings.Cut(inner, handoffMarkerSep)
	if id == "" || strings.ContainsAny(id, " []") {
		return "", "", false
	}
	return id, detail, true
}

// MergeDetail is how a merge block's marker describes it; it begins "merged
// from".
func MergeDetail(threads int, tools string, messages int) string {
	return fmt.Sprintf("%s%d threads · %s · %d messages", mergePrefix, threads, tools, messages)
}

const mergePrefix = "merged from "

// IsMergeBlock reports whether content is a block threadman wrote to merge
// several threads into one session.
func IsMergeBlock(content string) bool {
	_, detail, ok := ParseHandoffMarker(content)
	return ok && strings.HasPrefix(detail, mergePrefix)
}

// IsHandoffMarker reports whether s begins like a handoff block. Providers
// title a session from its first user message, which for a handed-off session
// is the block; a title that may have been truncated is still caught.
func IsHandoffMarker(s string) bool {
	return strings.HasPrefix(s, handoffMarkerPrefix)
}

// ProviderLabels are the display names of the providers threadman knows.
var ProviderLabels = map[string]string{
	"claude":      "Claude Code",
	"gemini":      "Gemini CLI",
	"opencode":    "OpenCode",
	"pi":          "pi",
	"antigravity": "Antigravity",
	"codex":       "Codex",
}

// ProviderLabel returns a provider's display name, or its ID when unknown.
func ProviderLabel(name string) string {
	if l := ProviderLabels[name]; l != "" {
		return l
	}
	return name
}
