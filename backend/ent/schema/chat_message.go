package schema

import (
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// ChatMessage holds one persisted message in a chat conversation.
type ChatMessage struct {
	ent.Schema
}

func (ChatMessage) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "chat_messages"},
	}
}

func (ChatMessage) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

func (ChatMessage) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("conversation_id"),
		field.Int64("user_id"),
		field.String("role").
			MaxLen(16),
		field.String("content").
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.String("status").
			MaxLen(16).
			Default("complete"),
		field.String("error_message").
			SchemaType(map[string]string{dialect.Postgres: "text"}).
			Default(""),
		field.JSON("metadata", map[string]any{}).
			Optional(),
	}
}

func (ChatMessage) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("conversation", ChatConversation.Type).
			Ref("messages").
			Field("conversation_id").
			Unique().
			Required(),
		edge.From("user", User.Type).
			Ref("chat_messages").
			Field("user_id").
			Unique().
			Required(),
	}
}

func (ChatMessage) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("conversation_id", "created_at"),
		index.Fields("user_id", "created_at"),
		index.Fields("role"),
		index.Fields("status"),
	}
}
