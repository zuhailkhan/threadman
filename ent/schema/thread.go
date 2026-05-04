package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

type Thread struct {
	ent.Schema
}

func (Thread) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").Unique(),
		field.String("provider"),
		field.String("original_id"),
		field.String("title"),
		field.String("workspace_path"),
		field.String("source_file_path"),
		field.Time("created_at"),
		field.Time("last_synced_at"),
	}
}

func (Thread) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("messages", Message.Type),
	}
}
