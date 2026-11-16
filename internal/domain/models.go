package domain

import "time"

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
)

type Message struct {
	ID        string    `json:"id"`
	Role      Role      `json:"role"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`

	Kind MessageKind `json:"kind,omitempty"`

	// HandoffID links a KindHandoff message to its Handoff record.
	HandoffID string `json:"handoff_id,omitempty"`

	// Tools are the tool calls an assistant made in this turn.
	Tools []ToolCall `json:"tools,omitempty"`

	// Provider names the tool the message was written in, when it differs
	// from its thread's: set on a conversation gathered from several tools.
	// It is not stored.
	Provider string `json:"provider,omitempty"`
}

type Thread struct {
	ID string `json:"id"`

	Provider string `json:"provider"`

	OriginalID string `json:"original_id"`

	Title string `json:"title"`

	CustomTitle string `json:"custom_title"`

	WorkspacePath string `json:"workspace_path"`

	SourceFilePath string `json:"source_file_path"`

	CanonicalID string `json:"canonical_id"`

	Messages []Message `json:"messages"`

	CreatedAt time.Time `json:"created_at"`

	LastSyncedAt time.Time `json:"last_synced_at"`

	Archived bool `json:"archived"`

	// SourceMissing is set when the provider no longer has the session, for
	// example because the tool cleaned up old sessions. The thread and its
	// messages are kept, but it can't be resumed or appended to.
	SourceMissing bool `json:"source_missing,omitempty"`
}
