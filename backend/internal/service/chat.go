package service

import (
	"context"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

const (
	ChatRoleUser      = "user"
	ChatRoleAssistant = "assistant"

	ChatMessageStatusComplete  = "complete"
	ChatMessageStatusError     = "error"
	ChatMessageStatusCancelled = "cancelled"

	ChatTitleMaxLen           = 120
	ChatModelMaxLen           = 128
	ChatSystemPromptMaxLen    = 8 * 1024
	ChatReasoningEffortMaxLen = 32
	ChatMessageMaxLen         = 64 * 1024
)

var (
	ErrChatConversationNotFound = infraerrors.NotFound("CHAT_CONVERSATION_NOT_FOUND", "chat conversation not found")
	ErrChatMessageNotFound      = infraerrors.NotFound("CHAT_MESSAGE_NOT_FOUND", "chat message not found")
	ErrChatInvalidInput         = infraerrors.BadRequest("CHAT_INVALID_INPUT", "invalid chat input")
)

type ChatConversation struct {
	ID              int64
	UserID          int64
	Title           string
	APIKeyID        *int64
	Model           string
	SystemPrompt    string
	ReasoningEffort string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	DeletedAt       *time.Time
	Messages        []ChatMessage
	MessageCount    int
}

type ChatConversationExportCursor struct {
	UpdatedAt time.Time
	ID        int64
}

type ChatConversationExportRecord struct {
	Conversation ChatConversation
	Cursor       ChatConversationExportCursor
}

type ChatMessage struct {
	ID             int64
	ConversationID int64
	UserID         int64
	Role           string
	Content        string
	Status         string
	ErrorMessage   string
	Metadata       map[string]any
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type CreateChatConversationRequest struct {
	Title           string
	APIKeyID        *int64
	Model           string
	SystemPrompt    string
	ReasoningEffort string
}

type UpdateChatConversationRequest struct {
	Title           *string
	APIKeyID        *int64
	ClearAPIKeyID   bool
	Model           *string
	SystemPrompt    *string
	ReasoningEffort *string
}

type CreateChatMessageRequest struct {
	Role         string
	Content      string
	Status       string
	ErrorMessage string
	Metadata     map[string]any
}

type ChatConversationRepository interface {
	CreateConversation(ctx context.Context, conversation *ChatConversation) error
	GetConversation(ctx context.Context, userID, conversationID int64, withMessages bool) (*ChatConversation, error)
	ListConversations(ctx context.Context, userID int64, params pagination.PaginationParams) ([]ChatConversation, *pagination.PaginationResult, error)
	ListConversationsForExport(ctx context.Context, userID int64, cursor *ChatConversationExportCursor, limit int) ([]ChatConversationExportRecord, error)
	UpdateConversation(ctx context.Context, conversation *ChatConversation) error
	SoftDeleteConversation(ctx context.Context, userID, conversationID int64) error
	CreateMessage(ctx context.Context, message *ChatMessage) error
	DeleteMessage(ctx context.Context, userID, conversationID, messageID int64) error
}
