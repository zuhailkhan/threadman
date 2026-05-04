package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

type Message struct {
	ent.Schema
}

func (Message) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").Unique(),
		field.String("role"),
		field.String("content"),
		field.Time("timestamp"),
	}
}

func (Message) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("thread", Thread.Type).
			Ref("messages").
			Unique(),
	}
}
