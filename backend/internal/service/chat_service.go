package service

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

const chatExportBatchSize = 100

type ChatService struct {
	repo       ChatConversationRepository
	apiKeyRepo APIKeyRepository
	userRepo   UserRepository
}

func NewChatService(repo ChatConversationRepository, apiKeyRepo APIKeyRepository, userRepo UserRepository) *ChatService {
	return &ChatService{repo: repo, apiKeyRepo: apiKeyRepo, userRepo: userRepo}
}

func (s *ChatService) EnsureUserCanUseChat(ctx context.Context, userID int64) error {
	if s.userRepo == nil {
		return nil
	}
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return err
	}
	if user == nil {
		return ErrUserNotFound
	}
	if !user.ChatEnabled {
		return ErrChatDisabled
	}
	return nil
}

func (s *ChatService) ListConversations(ctx context.Context, userID int64, params pagination.PaginationParams) ([]ChatConversation, *pagination.PaginationResult, error) {
	return s.repo.ListConversations(ctx, userID, params)
}

func (s *ChatService) StreamExportConversations(ctx context.Context, userID int64, visit func(ChatConversation) error) error {
	var cursor *ChatConversationExportCursor
	for {
		records, err := s.repo.ListConversationsForExport(ctx, userID, cursor, chatExportBatchSize)
		if err != nil {
			return fmt.Errorf("list chat conversations for export: %w", err)
		}
		if len(records) == 0 {
			return nil
		}
		for i := range records {
			if err := visit(records[i].Conversation); err != nil {
				return err
			}
		}
		last := records[len(records)-1]
		cursor = &ChatConversationExportCursor{UpdatedAt: last.Cursor.UpdatedAt, ID: last.Cursor.ID}
		if len(records) < chatExportBatchSize {
			return nil
		}
	}
}

func (s *ChatService) CreateConversation(ctx context.Context, userID int64, req CreateChatConversationRequest) (*ChatConversation, error) {
	title := normalizeChatTitle(req.Title)
	model := strings.TrimSpace(req.Model)
	systemPrompt := strings.TrimSpace(req.SystemPrompt)
	reasoningEffort := normalizeChatReasoningEffort(req.ReasoningEffort)
	if err := validateConversationFields(title, model, systemPrompt, reasoningEffort); err != nil {
		return nil, err
	}
	if err := s.validateAPIKey(ctx, userID, req.APIKeyID); err != nil {
		return nil, err
	}

	conversation := &ChatConversation{
		UserID:          userID,
		Title:           title,
		APIKeyID:        req.APIKeyID,
		Model:           model,
		SystemPrompt:    systemPrompt,
		ReasoningEffort: reasoningEffort,
	}
	if err := s.repo.CreateConversation(ctx, conversation); err != nil {
		return nil, fmt.Errorf("create chat conversation: %w", err)
	}
	return conversation, nil
}

func (s *ChatService) GetConversation(ctx context.Context, userID, conversationID int64) (*ChatConversation, error) {
	conversation, err := s.repo.GetConversation(ctx, userID, conversationID, true)
	if err != nil {
		return nil, fmt.Errorf("get chat conversation: %w", err)
	}
	return conversation, nil
}

func (s *ChatService) UpdateConversation(ctx context.Context, userID, conversationID int64, req UpdateChatConversationRequest) (*ChatConversation, error) {
	conversation, err := s.repo.GetConversation(ctx, userID, conversationID, false)
	if err != nil {
		return nil, fmt.Errorf("get chat conversation: %w", err)
	}

	if req.Title != nil {
		conversation.Title = normalizeChatTitle(*req.Title)
	}
	if req.Model != nil {
		conversation.Model = strings.TrimSpace(*req.Model)
	}
	if req.SystemPrompt != nil {
		conversation.SystemPrompt = strings.TrimSpace(*req.SystemPrompt)
	}
	if req.ReasoningEffort != nil {
		conversation.ReasoningEffort = normalizeChatReasoningEffort(*req.ReasoningEffort)
	}
	if req.ClearAPIKeyID {
		conversation.APIKeyID = nil
	} else if req.APIKeyID != nil {
		conversation.APIKeyID = req.APIKeyID
	}
	if err := validateConversationFields(conversation.Title, conversation.Model, conversation.SystemPrompt, conversation.ReasoningEffort); err != nil {
		return nil, err
	}
	if err := s.validateAPIKey(ctx, userID, conversation.APIKeyID); err != nil {
		return nil, err
	}
	if err := s.repo.UpdateConversation(ctx, conversation); err != nil {
		return nil, fmt.Errorf("update chat conversation: %w", err)
	}
	return s.repo.GetConversation(ctx, userID, conversationID, true)
}

func (s *ChatService) DeleteConversation(ctx context.Context, userID, conversationID int64) error {
	if err := s.repo.SoftDeleteConversation(ctx, userID, conversationID); err != nil {
		return fmt.Errorf("delete chat conversation: %w", err)
	}
	return nil
}

func (s *ChatService) AppendMessage(ctx context.Context, userID, conversationID int64, req CreateChatMessageRequest) (*ChatMessage, error) {
	if _, err := s.repo.GetConversation(ctx, userID, conversationID, false); err != nil {
		return nil, fmt.Errorf("get chat conversation: %w", err)
	}

	role := strings.TrimSpace(req.Role)
	status := strings.TrimSpace(req.Status)
	if status == "" {
		status = ChatMessageStatusComplete
	}
	content := strings.TrimSpace(req.Content)
	errorMessage := strings.TrimSpace(req.ErrorMessage)
	if err := validateMessageFields(role, content, status, errorMessage); err != nil {
		return nil, err
	}
	message := &ChatMessage{
		ConversationID: conversationID,
		UserID:         userID,
		Role:           role,
		Content:        content,
		Status:         status,
		ErrorMessage:   errorMessage,
		Metadata:       req.Metadata,
	}
	if message.Metadata == nil {
		message.Metadata = map[string]any{}
	}
	if err := s.repo.CreateMessage(ctx, message); err != nil {
		return nil, fmt.Errorf("create chat message: %w", err)
	}
	return message, nil
}

func (s *ChatService) DeleteMessage(ctx context.Context, userID, conversationID, messageID int64) error {
	if _, err := s.repo.GetConversation(ctx, userID, conversationID, false); err != nil {
		return fmt.Errorf("get chat conversation: %w", err)
	}
	if err := s.repo.DeleteMessage(ctx, userID, conversationID, messageID); err != nil {
		return fmt.Errorf("delete chat message: %w", err)
	}
	return nil
}

func (s *ChatService) validateAPIKey(ctx context.Context, userID int64, apiKeyID *int64) error {
	if apiKeyID == nil || *apiKeyID <= 0 {
		return nil
	}
	ids, err := s.apiKeyRepo.VerifyOwnership(ctx, userID, []int64{*apiKeyID})
	if err != nil {
		return fmt.Errorf("verify api key ownership: %w", err)
	}
	if len(ids) != 1 {
		return ErrAPIKeyNotFound
	}
	return nil
}

func normalizeChatTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return "New chat"
	}
	return truncateUTF8Bytes(title, ChatTitleMaxLen)
}

func validateConversationFields(title, model, systemPrompt, reasoningEffort string) error {
	if !validRuneLen(title, 1, ChatTitleMaxLen) {
		return ErrChatInvalidInput
	}
	if utf8.RuneCountInString(model) > ChatModelMaxLen {
		return ErrChatInvalidInput
	}
	if len(systemPrompt) > ChatSystemPromptMaxLen {
		return ErrChatInvalidInput
	}
	if len(reasoningEffort) > ChatReasoningEffortMaxLen || !validChatReasoningEffort(reasoningEffort) {
		return ErrChatInvalidInput
	}
	return nil
}

func normalizeChatReasoningEffort(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.ReplaceAll(value, "_", "-")
}

func validChatReasoningEffort(value string) bool {
	switch value {
	case "", "auto", "none", "minimal", "low", "medium", "high", "max", "ultra", "x-high", "xhigh":
		return true
	default:
		return false
	}
}

func validateMessageFields(role, content, status, errorMessage string) error {
	if role != ChatRoleUser && role != ChatRoleAssistant {
		return ErrChatInvalidInput
	}
	if content == "" || len(content) > ChatMessageMaxLen {
		return ErrChatInvalidInput
	}
	switch status {
	case ChatMessageStatusComplete, ChatMessageStatusError, ChatMessageStatusCancelled:
	default:
		return ErrChatInvalidInput
	}
	if len(errorMessage) > ChatMessageMaxLen {
		return ErrChatInvalidInput
	}
	return nil
}

func validRuneLen(value string, min, max int) bool {
	n := utf8.RuneCountInString(value)
	return n >= min && n <= max
}

func truncateUTF8Bytes(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	last := 0
	for i := range value {
		if i > maxBytes {
			return strings.TrimSpace(value[:last])
		}
		last = i
	}
	return value
}
