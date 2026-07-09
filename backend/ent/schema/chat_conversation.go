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

// ChatConversation holds a user's chat history thread.
type ChatConversation struct {
	ent.Schema
}

func (ChatConversation) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "chat_conversations"},
	}
}

func (ChatConversation) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
		mixins.SoftDeleteMixin{},
	}
}

func (ChatConversation) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("user_id"),
		field.String("title").
			MaxLen(120).
			Default("New chat"),
		field.Int64("api_key_id").
			Optional().
			Nillable(),
		field.String("model").
			MaxLen(128).
			Default(""),
		field.String("system_prompt").
			SchemaType(map[string]string{dialect.Postgres: "text"}).
			Default(""),
		field.String("reasoning_effort").
			MaxLen(32).
			Default(""),
	}
}

func (ChatConversation) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).
			Ref("chat_conversations").
			Field("user_id").
			Unique().
			Required(),
		edge.From("api_key", APIKey.Type).
			Ref("chat_conversations").
			Field("api_key_id").
			Unique(),
		edge.To("messages", ChatMessage.Type).
			Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}

func (ChatConversation) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id"),
		index.Fields("api_key_id"),
		index.Fields("updated_at"),
		index.Fields("deleted_at"),
	}
}
