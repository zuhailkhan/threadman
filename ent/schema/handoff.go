package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

// Handoff records a conversation being carried into another tool. It has no
// edges: either thread can be pruned or deleted while the record remains.
type Handoff struct {
	ent.Schema
}

func (Handoff) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").Unique(),
		field.String("source_thread_id"),
		field.String("target_thread_id"),
		field.String("target_provider"),
		field.String("mode"),
		field.String("summarizer").Default(""),
		field.Int("source_message_count"),
		field.String("source_last_message_id").Default(""),
		field.String("content"),
		field.Time("created_at"),
	}
}
