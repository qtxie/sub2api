package handler

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

type ChatHandler struct {
	chatService    *service.ChatService
	apiKeyService  *service.APIKeyService
	gatewayService *service.GatewayService
	cfg            *config.Config
	httpClient     *http.Client
}

func NewChatHandler(chatService *service.ChatService, apiKeyService *service.APIKeyService, gatewayService *service.GatewayService, cfg *config.Config) *ChatHandler {
	return &ChatHandler{
		chatService:    chatService,
		apiKeyService:  apiKeyService,
		gatewayService: gatewayService,
		cfg:            cfg,
		httpClient:     &http.Client{},
	}
}

type chatConversationRequest struct {
	Title           string `json:"title"`
	APIKeyID        *int64 `json:"api_key_id"`
	Model           string `json:"model"`
	SystemPrompt    string `json:"system_prompt"`
	ReasoningEffort string `json:"reasoning_effort"`
}

type updateChatConversationRequest struct {
	Title           *string `json:"title"`
	APIKeyID        *int64  `json:"api_key_id"`
	Model           *string `json:"model"`
	SystemPrompt    *string `json:"system_prompt"`
	ReasoningEffort *string `json:"reasoning_effort"`
}

type createChatMessageRequest struct {
	Role         string         `json:"role"`
	Content      string         `json:"content"`
	Status       string         `json:"status"`
	ErrorMessage string         `json:"error_message"`
	Metadata     map[string]any `json:"metadata"`
}

type chatStreamRequest struct {
	Attachments []chatStreamAttachmentRequest `json:"attachments"`
}

type chatStreamAttachmentRequest struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	MIMEType string `json:"mime_type"`
	Size     int64  `json:"size"`
	DataURL  string `json:"data_url"`
	Text     string `json:"text"`
}

type chatConversationResponse struct {
	ID              int64                 `json:"id"`
	UserID          int64                 `json:"user_id"`
	Title           string                `json:"title"`
	APIKeyID        *int64                `json:"api_key_id"`
	Model           string                `json:"model"`
	SystemPrompt    string                `json:"system_prompt"`
	ReasoningEffort string                `json:"reasoning_effort"`
	MessageCount    int                   `json:"message_count"`
	Messages        []chatMessageResponse `json:"messages,omitempty"`
	CreatedAt       int64                 `json:"created_at"`
	UpdatedAt       int64                 `json:"updated_at"`
}

type chatMessageResponse struct {
	ID             int64          `json:"id"`
	ConversationID int64          `json:"conversation_id"`
	UserID         int64          `json:"user_id"`
	Role           string         `json:"role"`
	Content        string         `json:"content"`
	Status         string         `json:"status"`
	ErrorMessage   string         `json:"error_message"`
	Metadata       map[string]any `json:"metadata"`
	CreatedAt      int64          `json:"created_at"`
	UpdatedAt      int64          `json:"updated_at"`
}

type chatModelListResponse struct {
	Models []string `json:"models"`
}

type gatewayChatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type chatStreamEvent struct {
	Type         string               `json:"type"`
	Content      string               `json:"content,omitempty"`
	Message      *chatMessageResponse `json:"message,omitempty"`
	SavedMessage *chatMessageResponse `json:"saved_message,omitempty"`
	Error        string               `json:"error,omitempty"`
}

type chatExportResponse struct {
	Version       int                        `json:"version"`
	ExportedAt    int64                      `json:"exported_at"`
	Conversations []chatConversationResponse `json:"conversations"`
}

const (
	chatStreamMaxAttachments = 8
	chatStreamMaxImageBytes  = 10 * 1024 * 1024
	chatStreamMaxFileBytes   = 256 * 1024
	chatStreamMaxTotalBytes  = 16 * 1024 * 1024
)

func (h *ChatHandler) ListModels(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if !h.requireChatAccess(c, subject.UserID) {
		return
	}

	apiKeyID, err := strconv.ParseInt(c.Query("api_key_id"), 10, 64)
	if err != nil || apiKeyID <= 0 {
		response.BadRequest(c, "Invalid API key ID")
		return
	}

	apiKey, err := h.apiKeyService.GetByID(c.Request.Context(), apiKeyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if apiKey == nil || apiKey.UserID != subject.UserID {
		response.NotFound(c, "API key not found")
		return
	}
	if !apiKey.IsActive() {
		response.BadRequest(c, "API key is not active")
		return
	}

	response.Success(c, chatModelListResponse{
		Models: chatModelIDsForGroup(c.Request.Context(), h.gatewayService, apiKey.Group),
	})
}

func (h *ChatHandler) ExportConversations(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if !h.requireChatAccess(c, subject.UserID) {
		return
	}

	exportedAt := time.Now().Unix()
	filename := "sub2api-chat-export-" + time.Now().UTC().Format("2006-01-02") + ".json"
	encoder := json.NewEncoder(c.Writer)
	started := false
	first := true
	err := h.chatService.StreamExportConversations(c.Request.Context(), subject.UserID, func(conversation service.ChatConversation) error {
		if !started {
			started = true
			c.Header("Content-Type", "application/json; charset=utf-8")
			c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
			c.Status(http.StatusOK)
			if _, err := c.Writer.Write([]byte(`{"version":1,"exported_at":` + strconv.FormatInt(exportedAt, 10) + `,"conversations":[`)); err != nil {
				return err
			}
			flushResponse(c)
		}
		if !first {
			if _, err := c.Writer.Write([]byte(",")); err != nil {
				return err
			}
		}
		first = false
		if err := encoder.Encode(chatConversationToResponse(&conversation, true)); err != nil {
			return err
		}
		flushResponse(c)
		return nil
	})
	if err != nil {
		if !started {
			response.ErrorFrom(c, err)
		}
		return
	}
	if !started {
		c.Header("Content-Type", "application/json; charset=utf-8")
		c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
		c.Status(http.StatusOK)
		_, _ = c.Writer.Write([]byte(`{"version":1,"exported_at":` + strconv.FormatInt(exportedAt, 10) + `,"conversations":[]}`))
		return
	}
	_, _ = c.Writer.Write([]byte(`]}`))
}

func (h *ChatHandler) ListConversations(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if !h.requireChatAccess(c, subject.UserID) {
		return
	}

	page, pageSize := response.ParsePagination(c)
	params := pagination.PaginationParams{Page: page, PageSize: pageSize}
	conversations, result, err := h.chatService.ListConversations(c.Request.Context(), subject.UserID, params)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	out := make([]chatConversationResponse, 0, len(conversations))
	for i := range conversations {
		out = append(out, chatConversationToResponse(&conversations[i], false))
	}
	response.Paginated(c, out, result.Total, page, pageSize)
}

func (h *ChatHandler) CreateConversation(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if !h.requireChatAccess(c, subject.UserID) {
		return
	}

	var req chatConversationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	conversation, err := h.chatService.CreateConversation(c.Request.Context(), subject.UserID, service.CreateChatConversationRequest{
		Title:           req.Title,
		APIKeyID:        req.APIKeyID,
		Model:           req.Model,
		SystemPrompt:    req.SystemPrompt,
		ReasoningEffort: req.ReasoningEffort,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Created(c, chatConversationToResponse(conversation, true))
}

func (h *ChatHandler) GetConversation(c *gin.Context) {
	subject, conversationID, ok := h.authAndConversationID(c)
	if !ok {
		return
	}
	conversation, err := h.chatService.GetConversation(c.Request.Context(), subject.UserID, conversationID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, chatConversationToResponse(conversation, true))
}

func (h *ChatHandler) UpdateConversation(c *gin.Context) {
	subject, conversationID, ok := h.authAndConversationID(c)
	if !ok {
		return
	}

	var raw map[string]json.RawMessage
	if err := c.ShouldBindJSON(&raw); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	var req updateChatConversationRequest
	b, _ := json.Marshal(raw)
	if err := json.Unmarshal(b, &req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	_, apiKeyPresent := raw["api_key_id"]
	clearAPIKeyID := apiKeyPresent && req.APIKeyID == nil

	conversation, err := h.chatService.UpdateConversation(c.Request.Context(), subject.UserID, conversationID, service.UpdateChatConversationRequest{
		Title:           req.Title,
		APIKeyID:        req.APIKeyID,
		ClearAPIKeyID:   clearAPIKeyID,
		Model:           req.Model,
		SystemPrompt:    req.SystemPrompt,
		ReasoningEffort: req.ReasoningEffort,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, chatConversationToResponse(conversation, true))
}

func (h *ChatHandler) DeleteConversation(c *gin.Context) {
	subject, conversationID, ok := h.authAndConversationID(c)
	if !ok {
		return
	}
	if err := h.chatService.DeleteConversation(c.Request.Context(), subject.UserID, conversationID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "Chat conversation deleted successfully"})
}

func (h *ChatHandler) AppendMessage(c *gin.Context) {
	subject, conversationID, ok := h.authAndConversationID(c)
	if !ok {
		return
	}

	var req createChatMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	message, err := h.chatService.AppendMessage(c.Request.Context(), subject.UserID, conversationID, service.CreateChatMessageRequest{
		Role:         req.Role,
		Content:      req.Content,
		Status:       req.Status,
		ErrorMessage: req.ErrorMessage,
		Metadata:     req.Metadata,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Created(c, chatMessageToResponse(message))
}

func (h *ChatHandler) StreamConversation(c *gin.Context) {
	subject, conversationID, ok := h.authAndConversationID(c)
	if !ok {
		return
	}

	var streamReq chatStreamRequest
	if err := c.ShouldBindJSON(&streamReq); err != nil && !errors.Is(err, io.EOF) {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	attachments, err := validateChatStreamAttachments(streamReq.Attachments)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	if h.apiKeyService == nil {
		response.InternalError(c, "API key service is not configured")
		return
	}

	conversation, err := h.chatService.GetConversation(c.Request.Context(), subject.UserID, conversationID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if conversation.APIKeyID == nil || *conversation.APIKeyID <= 0 {
		response.BadRequest(c, "Conversation API key is required")
		return
	}
	if strings.TrimSpace(conversation.Model) == "" {
		response.BadRequest(c, "Conversation model is required")
		return
	}

	apiKey, err := h.apiKeyService.GetByID(c.Request.Context(), *conversation.APIKeyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if apiKey == nil || apiKey.UserID != subject.UserID {
		response.NotFound(c, "API key not found")
		return
	}
	if !apiKey.IsActive() {
		response.BadRequest(c, "API key is not active")
		return
	}

	gatewayReq, err := h.buildGatewayChatRequest(conversation, attachments)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	reqBody, err := json.Marshal(gatewayReq)
	if err != nil {
		response.InternalError(c, "Failed to build chat request")
		return
	}

	upstreamReq, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, h.localGatewayURL(c, "/v1/chat/completions"), bytes.NewReader(reqBody))
	if err != nil {
		response.InternalError(c, "Failed to create chat request")
		return
	}
	upstreamReq.Header.Set("Authorization", "Bearer "+apiKey.Key)
	upstreamReq.Header.Set("Content-Type", "application/json")
	upstreamReq.Header.Set("Accept", "text/event-stream")
	if lang := c.GetHeader("Accept-Language"); lang != "" {
		upstreamReq.Header.Set("Accept-Language", lang)
	}
	if ua := c.GetHeader("User-Agent"); ua != "" {
		upstreamReq.Header.Set("User-Agent", ua)
	}

	upstreamResp, err := h.httpClient.Do(upstreamReq)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(c.Request.Context().Err(), context.Canceled) {
			_, _ = h.saveStreamedAssistantMessage(c.Request.Context(), subject.UserID, conversationID, "", context.Canceled)
			return
		}
		h.writePreStreamFailure(c, subject.UserID, conversationID, "Gateway request failed")
		return
	}
	defer upstreamResp.Body.Close()

	if upstreamResp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(upstreamResp.Body, 64*1024))
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = "Gateway request failed"
		}
		h.writePreStreamFailure(c, subject.UserID, conversationID, message)
		return
	}

	startChatStreamResponse(c)

	content, streamErr := h.forwardGatewayChatStream(c, upstreamResp.Body)
	message, saveErr := h.saveStreamedAssistantMessage(c.Request.Context(), subject.UserID, conversationID, content, streamErr)
	if saveErr != nil && streamErr == nil {
		streamErr = saveErr
	}

	if streamErr != nil {
		errText := streamErr.Error()
		var saved *chatMessageResponse
		if message != nil {
			resp := chatMessageToResponse(message)
			saved = &resp
		}
		writeChatStreamEvent(c, "error", chatStreamEvent{Type: "error", Error: errText, SavedMessage: saved})
		return
	}

	resp := chatMessageToResponse(message)
	writeChatStreamEvent(c, "done", chatStreamEvent{Type: "done", Message: &resp})
}

func (h *ChatHandler) DeleteMessage(c *gin.Context) {
	subject, conversationID, ok := h.authAndConversationID(c)
	if !ok {
		return
	}
	messageID, err := strconv.ParseInt(c.Param("message_id"), 10, 64)
	if err != nil || messageID <= 0 {
		response.BadRequest(c, "Invalid message ID")
		return
	}
	if err := h.chatService.DeleteMessage(c.Request.Context(), subject.UserID, conversationID, messageID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "Chat message deleted successfully"})
}

func (h *ChatHandler) authAndConversationID(c *gin.Context) (middleware2.AuthSubject, int64, bool) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return middleware2.AuthSubject{}, 0, false
	}
	if !h.requireChatAccess(c, subject.UserID) {
		return middleware2.AuthSubject{}, 0, false
	}
	conversationID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || conversationID <= 0 {
		response.BadRequest(c, "Invalid conversation ID")
		return middleware2.AuthSubject{}, 0, false
	}
	return subject, conversationID, true
}

func (h *ChatHandler) requireChatAccess(c *gin.Context, userID int64) bool {
	if h.chatService == nil {
		response.InternalError(c, "Chat service is not configured")
		return false
	}
	if err := h.chatService.EnsureUserCanUseChat(c.Request.Context(), userID); err != nil {
		response.ErrorFrom(c, err)
		return false
	}
	return true
}

func (h *ChatHandler) buildGatewayChatRequest(conversation *service.ChatConversation, attachments []chatStreamAttachmentRequest) (map[string]any, error) {
	messages := make([]gatewayChatMessage, 0, len(conversation.Messages)+1)
	if prompt := strings.TrimSpace(conversation.SystemPrompt); prompt != "" {
		messages = append(messages, gatewayChatMessage{Role: "system", Content: prompt})
	}
	lastUserIndex := -1
	for i := range conversation.Messages {
		message := conversation.Messages[i]
		if message.Role != service.ChatRoleUser && message.Role != service.ChatRoleAssistant {
			continue
		}
		if strings.TrimSpace(message.Content) == "" {
			continue
		}
		messages = append(messages, gatewayChatMessage{Role: message.Role, Content: message.Content})
		if message.Role == service.ChatRoleUser {
			lastUserIndex = len(messages) - 1
		}
	}
	if len(attachments) > 0 {
		if lastUserIndex < 0 {
			return nil, fmt.Errorf("attachments require a user message")
		}
		messages[lastUserIndex].Content = gatewayContentWithAttachments(fmt.Sprint(messages[lastUserIndex].Content), attachments)
	}

	body := map[string]any{
		"model":    strings.TrimSpace(conversation.Model),
		"messages": messages,
		"stream":   true,
	}
	if effort := strings.TrimSpace(conversation.ReasoningEffort); effort != "" {
		body["reasoning_effort"] = effort
	}
	return body, nil
}

func validateChatStreamAttachments(in []chatStreamAttachmentRequest) ([]chatStreamAttachmentRequest, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > chatStreamMaxAttachments {
		return nil, fmt.Errorf("too many attachments")
	}

	out := make([]chatStreamAttachmentRequest, 0, len(in))
	totalBytes := int64(0)
	for _, item := range in {
		item.Type = strings.ToLower(strings.TrimSpace(item.Type))
		item.Name = strings.TrimSpace(item.Name)
		item.MIMEType = strings.ToLower(strings.TrimSpace(item.MIMEType))
		item.DataURL = strings.TrimSpace(item.DataURL)
		item.Text = strings.TrimSpace(item.Text)
		if item.Name == "" || len(item.Name) > 200 || len(item.MIMEType) > 128 {
			return nil, fmt.Errorf("invalid attachment")
		}
		switch item.Type {
		case "image":
			decodedBytes, err := validateChatImageAttachment(item)
			if err != nil {
				return nil, err
			}
			item.Size = decodedBytes
			totalBytes += decodedBytes
		case "file":
			if err := validateChatFileAttachment(item); err != nil {
				return nil, err
			}
			item.Size = int64(len(item.Text))
			totalBytes += item.Size
		default:
			return nil, fmt.Errorf("unsupported attachment type")
		}
		if totalBytes > chatStreamMaxTotalBytes {
			return nil, fmt.Errorf("attachments are too large")
		}
		out = append(out, item)
	}
	return out, nil
}

func validateChatImageAttachment(item chatStreamAttachmentRequest) (int64, error) {
	if !strings.HasPrefix(item.MIMEType, "image/") || item.DataURL == "" {
		return 0, fmt.Errorf("invalid image attachment")
	}
	prefix := "data:" + item.MIMEType + ";base64,"
	if !strings.HasPrefix(strings.ToLower(item.DataURL), strings.ToLower(prefix)) {
		return 0, fmt.Errorf("invalid image attachment")
	}
	encoded := item.DataURL[len(prefix):]
	if strings.TrimSpace(encoded) == "" {
		return 0, fmt.Errorf("invalid image attachment")
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return 0, fmt.Errorf("invalid image attachment")
	}
	if len(decoded) == 0 || len(decoded) > chatStreamMaxImageBytes {
		return 0, fmt.Errorf("image attachment is too large")
	}
	return int64(len(decoded)), nil
}

func validateChatFileAttachment(item chatStreamAttachmentRequest) error {
	if !isSupportedChatTextAttachment(item.Name, item.MIMEType) {
		return fmt.Errorf("unsupported file attachment")
	}
	if item.Text == "" || len(item.Text) > chatStreamMaxFileBytes {
		return fmt.Errorf("file attachment is too large")
	}
	return nil
}

func isSupportedChatTextAttachment(name, mimeType string) bool {
	if strings.HasPrefix(mimeType, "text/") {
		return true
	}
	switch mimeType {
	case "application/json", "application/xml", "application/yaml", "application/x-yaml", "application/javascript":
		return true
	}
	lowerName := strings.ToLower(name)
	for _, suffix := range []string{
		".txt", ".md", ".markdown", ".csv", ".json", ".jsonl", ".xml", ".yaml", ".yml",
		".log", ".html", ".htm", ".css", ".js", ".jsx", ".ts", ".tsx", ".go", ".py",
		".java", ".c", ".cc", ".cpp", ".h", ".hpp", ".rs", ".sql", ".sh", ".ps1",
	} {
		if strings.HasSuffix(lowerName, suffix) {
			return true
		}
	}
	return false
}

func gatewayContentWithAttachments(text string, attachments []chatStreamAttachmentRequest) []map[string]any {
	parts := []map[string]any{{
		"type": "text",
		"text": text,
	}}
	for _, item := range attachments {
		switch item.Type {
		case "image":
			parts = append(parts, map[string]any{
				"type": "image_url",
				"image_url": map[string]string{
					"url": item.DataURL,
				},
			})
		case "file":
			parts = append(parts, map[string]any{
				"type": "text",
				"text": formatChatFileAttachmentText(item),
			})
		}
	}
	return parts
}

func formatChatFileAttachmentText(item chatStreamAttachmentRequest) string {
	mimeType := item.MIMEType
	if mimeType == "" {
		mimeType = "text/plain"
	}
	return fmt.Sprintf("Attached file %q (%s, %d bytes):\n\n%s", item.Name, mimeType, item.Size, item.Text)
}

func (h *ChatHandler) localGatewayURL(c *gin.Context, path string) string {
	host := ""
	if h.cfg != nil {
		host = h.cfg.Server.Address()
	}
	if strings.TrimSpace(host) == "" {
		host = c.Request.Host
	}
	host = normalizeLocalGatewayHost(host)
	return (&url.URL{Scheme: "http", Host: host, Path: path}).String()
}

func normalizeLocalGatewayHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return "127.0.0.1:8080"
	}
	h, p, err := net.SplitHostPort(host)
	if err != nil {
		if host == "0.0.0.0" || host == "::" || host == "[::]" {
			return "127.0.0.1"
		}
		return host
	}
	if h == "" || h == "0.0.0.0" || h == "::" {
		h = "127.0.0.1"
	}
	return net.JoinHostPort(h, p)
}

func (h *ChatHandler) forwardGatewayChatStream(c *gin.Context, body io.Reader) (string, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), service.ChatMessageMaxLen*4+4096)
	var out strings.Builder
	var dataLines []string

	processPayload := func(payload string) error {
		payload = strings.TrimSpace(payload)
		if payload == "" || payload == "[DONE]" {
			return nil
		}
		delta, streamErr := extractGatewayStreamDelta([]byte(payload), out.Len() > 0)
		if streamErr != nil {
			return streamErr
		}
		if delta == "" {
			return nil
		}
		if out.Len()+len(delta) > service.ChatMessageMaxLen {
			return fmt.Errorf("response too large")
		}
		out.WriteString(delta)
		writeChatStreamEvent(c, "delta", chatStreamEvent{Type: "delta", Content: delta})
		return nil
	}
	flushEvent := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		payload := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		return processPayload(payload)
	}

	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == "" {
			if err := flushEvent(); err != nil {
				return out.String(), err
			}
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(c.Request.Context().Err(), context.Canceled) {
			return out.String(), context.Canceled
		}
		return out.String(), err
	}
	if err := flushEvent(); err != nil {
		return out.String(), err
	}
	if out.Len() == 0 {
		return "", fmt.Errorf("empty response")
	}
	return out.String(), nil
}

func (h *ChatHandler) saveStreamedAssistantMessage(ctx context.Context, userID, conversationID int64, content string, streamErr error) (*service.ChatMessage, error) {
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	status := service.ChatMessageStatusComplete
	errorMessage := ""
	content = strings.TrimSpace(content)
	if streamErr != nil {
		if errors.Is(streamErr, context.Canceled) {
			status = service.ChatMessageStatusCancelled
			if content == "" {
				content = "Stopped"
			}
		} else {
			status = service.ChatMessageStatusError
			errorMessage = streamErr.Error()
			if content == "" {
				content = "Failed"
			}
		}
	}
	return h.chatService.AppendMessage(saveCtx, userID, conversationID, service.CreateChatMessageRequest{
		Role:         service.ChatRoleAssistant,
		Content:      content,
		Status:       status,
		ErrorMessage: errorMessage,
	})
}

func (h *ChatHandler) writePreStreamFailure(c *gin.Context, userID, conversationID int64, message string) {
	message = strings.TrimSpace(message)
	if message == "" {
		message = "Gateway request failed"
	}
	savedMessage, _ := h.saveStreamedAssistantMessage(c.Request.Context(), userID, conversationID, "", fmt.Errorf("%s", message))
	var saved *chatMessageResponse
	if savedMessage != nil {
		resp := chatMessageToResponse(savedMessage)
		saved = &resp
	}
	startChatStreamResponse(c)
	writeChatStreamEvent(c, "error", chatStreamEvent{Type: "error", Error: message, SavedMessage: saved})
}

func startChatStreamResponse(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	flushResponse(c)
}

func writeChatStreamEvent(c *gin.Context, event string, payload chatStreamEvent) {
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event, b)
	flushResponse(c)
}

func extractGatewayStreamDelta(data []byte, hasOutput bool) (string, error) {
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", nil
	}
	if errText := extractGatewayStreamError(payload); errText != "" {
		return "", fmt.Errorf("%s", errText)
	}
	if isReasoningPayload(payload) {
		return "", nil
	}

	if choices, ok := payload["choices"].([]any); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]any); ok {
			if delta, ok := choice["delta"].(map[string]any); ok {
				if text := extractGatewayText(delta["content"]); text != "" {
					return text, nil
				}
				if text := extractGatewayText(delta["text"]); text != "" {
					return text, nil
				}
				if text := extractGatewayText(delta["output_text"]); text != "" {
					return text, nil
				}
			}
		}
	}

	if typ, _ := payload["type"].(string); strings.HasSuffix(typ, ".delta") {
		if text, ok := payload["delta"].(string); ok && text != "" {
			return text, nil
		}
		if text := extractGatewayText(payload["text"]); text != "" {
			return text, nil
		}
		if text := extractGatewayText(payload["output_text"]); text != "" {
			return text, nil
		}
	}

	if delta, ok := payload["delta"].(map[string]any); ok {
		if text := extractGatewayText(delta["text"]); text != "" {
			return text, nil
		}
		if text := extractGatewayText(delta["content"]); text != "" {
			return text, nil
		}
		if text := extractGatewayText(delta["output_text"]); text != "" {
			return text, nil
		}
	}
	if block, ok := payload["content_block"].(map[string]any); ok {
		if text := extractGatewayText(block["text"]); text != "" {
			return text, nil
		}
	}

	if candidates, ok := payload["candidates"].([]any); ok && len(candidates) > 0 {
		if candidate, ok := candidates[0].(map[string]any); ok {
			if content, ok := candidate["content"].(map[string]any); ok {
				if text := extractGatewayText(content["parts"]); text != "" {
					return text, nil
				}
			}
		}
	}

	if hasOutput {
		return "", nil
	}
	return extractGatewayCompletionContent(payload), nil
}

func extractGatewayCompletionContent(payload map[string]any) string {
	if choices, ok := payload["choices"].([]any); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]any); ok {
			if message, ok := choice["message"].(map[string]any); ok {
				if text := extractGatewayText(message["content"]); text != "" {
					return text
				}
			}
			if delta, ok := choice["delta"].(map[string]any); ok {
				if text := extractGatewayText(delta["content"]); text != "" {
					return text
				}
			}
		}
	}
	if text := extractGatewayText(payload["output_text"]); text != "" {
		return text
	}
	if response, ok := payload["response"].(map[string]any); ok {
		if text := extractGatewayText(response["output_text"]); text != "" {
			return text
		}
		if text := extractGatewayText(response["output"]); text != "" {
			return text
		}
	}
	return extractGatewayText(payload["output"])
}

func extractGatewayStreamError(payload map[string]any) string {
	if errObj, ok := payload["error"].(map[string]any); ok {
		if text := extractGatewayText(errObj["message"]); text != "" {
			return text
		}
		if nested, ok := errObj["error"].(map[string]any); ok {
			if text := extractGatewayText(nested["message"]); text != "" {
				return text
			}
		}
	}
	typ, _ := payload["type"].(string)
	if typ == "error" {
		if text := extractGatewayText(payload["message"]); text != "" {
			return text
		}
	}
	if strings.HasSuffix(typ, ".failed") {
		if response, ok := payload["response"].(map[string]any); ok {
			if errObj, ok := response["error"].(map[string]any); ok {
				return extractGatewayText(errObj["message"])
			}
		}
	}
	return ""
}

func extractGatewayText(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, item := range v {
			b.WriteString(extractGatewayText(item))
		}
		return b.String()
	case map[string]any:
		if isReasoningPayload(v) {
			return ""
		}
		for _, key := range []string{"text", "content", "output_text", "parts"} {
			if text := extractGatewayText(v[key]); text != "" {
				return text
			}
		}
		if message, ok := v["message"].(map[string]any); ok {
			return extractGatewayText(message["content"])
		}
	}
	return ""
}

func isReasoningPayload(payload map[string]any) bool {
	if typ, _ := payload["type"].(string); strings.Contains(strings.ToLower(typ), "reasoning") {
		return true
	}
	if _, ok := payload["reasoning_content"]; ok {
		return true
	}
	if _, ok := payload["reasoning_text"]; ok {
		return true
	}
	return false
}

func chatModelIDsForGroup(ctx context.Context, gatewayService *service.GatewayService, group *service.Group) []string {
	if gatewayService == nil {
		return nil
	}

	var groupID *int64
	platform := ""
	if group != nil {
		groupID = &group.ID
		platform = group.Platform
	}

	availableModels := gatewayService.GetAvailableModels(ctx, groupID, platform)
	if group != nil && group.CustomModelsListEnabled() {
		fallbackModels := defaultModelIDsForPlatform(platform)
		return filterModelsByCustomList(customModelsListSource(platform, availableModels, fallbackModels), fallbackModels, group.ModelsListConfig.Models)
	}
	if len(availableModels) > 0 {
		return availableModels
	}
	return defaultModelIDsForPlatform(platform)
}

func chatConversationToResponse(conversation *service.ChatConversation, includeMessages bool) chatConversationResponse {
	out := chatConversationResponse{
		ID:              conversation.ID,
		UserID:          conversation.UserID,
		Title:           conversation.Title,
		APIKeyID:        conversation.APIKeyID,
		Model:           conversation.Model,
		SystemPrompt:    conversation.SystemPrompt,
		ReasoningEffort: conversation.ReasoningEffort,
		MessageCount:    conversation.MessageCount,
		CreatedAt:       conversation.CreatedAt.Unix(),
		UpdatedAt:       conversation.UpdatedAt.Unix(),
	}
	if includeMessages {
		out.Messages = make([]chatMessageResponse, 0, len(conversation.Messages))
		for i := range conversation.Messages {
			out.Messages = append(out.Messages, chatMessageToResponse(&conversation.Messages[i]))
		}
		out.MessageCount = len(out.Messages)
	}
	return out
}

func chatMessageToResponse(message *service.ChatMessage) chatMessageResponse {
	metadata := message.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	return chatMessageResponse{
		ID:             message.ID,
		ConversationID: message.ConversationID,
		UserID:         message.UserID,
		Role:           message.Role,
		Content:        message.Content,
		Status:         message.Status,
		ErrorMessage:   message.ErrorMessage,
		Metadata:       metadata,
		CreatedAt:      message.CreatedAt.Unix(),
		UpdatedAt:      message.UpdatedAt.Unix(),
	}
}

func flushResponse(c *gin.Context) {
	if flusher, ok := c.Writer.(http.Flusher); ok {
		flusher.Flush()
	}
}
