package repository

import (
	"context"
	"database/sql"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/chatconversation"
	"github.com/Wei-Shaw/sub2api/ent/chatmessage"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type chatConversationRepository struct {
	client *dbent.Client
}

func NewChatConversationRepository(client *dbent.Client, _ *sql.DB) service.ChatConversationRepository {
	return &chatConversationRepository{client: client}
}

func (r *chatConversationRepository) activeQuery() *dbent.ChatConversationQuery {
	return r.client.ChatConversation.Query().Where(chatconversation.DeletedAtIsNil())
}

func (r *chatConversationRepository) CreateConversation(ctx context.Context, conversation *service.ChatConversation) error {
	builder := r.client.ChatConversation.Create().
		SetUserID(conversation.UserID).
		SetTitle(conversation.Title).
		SetModel(conversation.Model).
		SetSystemPrompt(conversation.SystemPrompt).
		SetReasoningEffort(conversation.ReasoningEffort).
		SetNillableAPIKeyID(conversation.APIKeyID)

	created, err := builder.Save(ctx)
	if err != nil {
		return err
	}
	*conversation = *chatConversationEntityToService(created, false)
	return nil
}

func (r *chatConversationRepository) GetConversation(ctx context.Context, userID, conversationID int64, withMessages bool) (*service.ChatConversation, error) {
	q := r.activeQuery().Where(chatconversation.IDEQ(conversationID), chatconversation.UserIDEQ(userID))
	if withMessages {
		q = q.WithMessages(func(mq *dbent.ChatMessageQuery) {
			mq.Order(dbent.Asc(chatmessage.FieldCreatedAt), dbent.Asc(chatmessage.FieldID))
		})
	}
	m, err := q.Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, service.ErrChatConversationNotFound
		}
		return nil, err
	}
	out := chatConversationEntityToService(m, withMessages)
	if withMessages {
		out.MessageCount = len(out.Messages)
	}
	return out, nil
}

func (r *chatConversationRepository) ListConversations(ctx context.Context, userID int64, params pagination.PaginationParams) ([]service.ChatConversation, *pagination.PaginationResult, error) {
	q := r.activeQuery().Where(chatconversation.UserIDEQ(userID))
	total, err := q.Count(ctx)
	if err != nil {
		return nil, nil, err
	}
	rows, err := q.
		Offset(params.Offset()).
		Limit(params.Limit()).
		Order(dbent.Desc(chatconversation.FieldUpdatedAt), dbent.Desc(chatconversation.FieldID)).
		All(ctx)
	if err != nil {
		return nil, nil, err
	}

	out := make([]service.ChatConversation, 0, len(rows))
	for _, row := range rows {
		out = append(out, *chatConversationEntityToService(row, false))
	}
	return out, paginationResultFromTotal(int64(total), params), nil
}

func (r *chatConversationRepository) ListConversationsForExport(ctx context.Context, userID int64, cursor *service.ChatConversationExportCursor, limit int) ([]service.ChatConversationExportRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	q := r.activeQuery().Where(chatconversation.UserIDEQ(userID))
	if cursor != nil {
		q = q.Where(chatconversation.Or(
			chatconversation.UpdatedAtLT(cursor.UpdatedAt),
			chatconversation.And(
				chatconversation.UpdatedAtEQ(cursor.UpdatedAt),
				chatconversation.IDLT(cursor.ID),
			),
		))
	}
	rows, err := q.
		Order(dbent.Desc(chatconversation.FieldUpdatedAt), dbent.Desc(chatconversation.FieldID)).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]service.ChatConversationExportRecord, 0, len(rows))
	for _, row := range rows {
		conversation, err := r.GetConversation(ctx, userID, row.ID, true)
		if err != nil {
			return nil, err
		}
		conversation.MessageCount = len(conversation.Messages)
		out = append(out, service.ChatConversationExportRecord{
			Conversation: *conversation,
			Cursor: service.ChatConversationExportCursor{
				UpdatedAt: row.UpdatedAt,
				ID:        row.ID,
			},
		})
	}
	return out, nil
}

func (r *chatConversationRepository) UpdateConversation(ctx context.Context, conversation *service.ChatConversation) error {
	now := time.Now()
	builder := r.client.ChatConversation.Update().
		Where(chatconversation.IDEQ(conversation.ID), chatconversation.UserIDEQ(conversation.UserID), chatconversation.DeletedAtIsNil()).
		SetTitle(conversation.Title).
		SetModel(conversation.Model).
		SetSystemPrompt(conversation.SystemPrompt).
		SetReasoningEffort(conversation.ReasoningEffort).
		SetUpdatedAt(now)
	if conversation.APIKeyID != nil {
		builder.SetAPIKeyID(*conversation.APIKeyID)
	} else {
		builder.ClearAPIKeyID()
	}
	n, err := builder.Save(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return service.ErrChatConversationNotFound
	}
	conversation.UpdatedAt = now
	return nil
}

func (r *chatConversationRepository) SoftDeleteConversation(ctx context.Context, userID, conversationID int64) error {
	n, err := r.client.ChatConversation.Update().
		Where(chatconversation.IDEQ(conversationID), chatconversation.UserIDEQ(userID), chatconversation.DeletedAtIsNil()).
		SetDeletedAt(time.Now()).
		SetUpdatedAt(time.Now()).
		Save(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return service.ErrChatConversationNotFound
	}
	return nil
}

func (r *chatConversationRepository) CreateMessage(ctx context.Context, message *service.ChatMessage) error {
	now := time.Now()
	n, err := r.client.ChatConversation.Update().
		Where(chatconversation.IDEQ(message.ConversationID), chatconversation.UserIDEQ(message.UserID), chatconversation.DeletedAtIsNil()).
		SetUpdatedAt(now).
		Save(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return service.ErrChatConversationNotFound
	}
	created, err := r.client.ChatMessage.Create().
		SetConversationID(message.ConversationID).
		SetUserID(message.UserID).
		SetRole(message.Role).
		SetContent(message.Content).
		SetStatus(message.Status).
		SetErrorMessage(message.ErrorMessage).
		SetMetadata(message.Metadata).
		Save(ctx)
	if err != nil {
		return err
	}
	*message = *chatMessageEntityToService(created)
	return nil
}

func (r *chatConversationRepository) DeleteMessage(ctx context.Context, userID, conversationID, messageID int64) error {
	n, err := r.client.ChatMessage.Delete().
		Where(chatmessage.IDEQ(messageID), chatmessage.ConversationIDEQ(conversationID), chatmessage.UserIDEQ(userID)).
		Exec(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return service.ErrChatMessageNotFound
	}
	_, err = r.client.ChatConversation.Update().
		Where(chatconversation.IDEQ(conversationID), chatconversation.UserIDEQ(userID), chatconversation.DeletedAtIsNil()).
		SetUpdatedAt(time.Now()).
		Save(ctx)
	return err
}

func chatConversationEntityToService(m *dbent.ChatConversation, withMessages bool) *service.ChatConversation {
	if m == nil {
		return nil
	}
	out := &service.ChatConversation{
		ID:              m.ID,
		UserID:          m.UserID,
		Title:           m.Title,
		APIKeyID:        m.APIKeyID,
		Model:           m.Model,
		SystemPrompt:    m.SystemPrompt,
		ReasoningEffort: m.ReasoningEffort,
		CreatedAt:       m.CreatedAt,
		UpdatedAt:       m.UpdatedAt,
		DeletedAt:       m.DeletedAt,
	}
	if withMessages {
		out.Messages = make([]service.ChatMessage, 0, len(m.Edges.Messages))
		for _, msg := range m.Edges.Messages {
			out.Messages = append(out.Messages, *chatMessageEntityToService(msg))
		}
	}
	return out
}

func chatMessageEntityToService(m *dbent.ChatMessage) *service.ChatMessage {
	if m == nil {
		return nil
	}
	metadata := m.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	return &service.ChatMessage{
		ID:             m.ID,
		ConversationID: m.ConversationID,
		UserID:         m.UserID,
		Role:           m.Role,
		Content:        m.Content,
		Status:         m.Status,
		ErrorMessage:   m.ErrorMessage,
		Metadata:       metadata,
		CreatedAt:      m.CreatedAt,
		UpdatedAt:      m.UpdatedAt,
	}
}
