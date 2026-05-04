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
}

type Thread struct {
	ID string `json:"id"`

	Provider string `json:"provider"`

	OriginalID string `json:"original_id"`

	Title string `json:"title"`

	WorkspacePath string `json:"workspace_path"`

	SourceFilePath string `json:"source_file_path"`

	Messages []Message `json:"messages"`

	CreatedAt time.Time `json:"created_at"`

	LastSyncedAt time.Time `json:"last_synced_at"`
}
