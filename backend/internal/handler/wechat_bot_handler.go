package handler

import (
	"strings"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type WeChatBotHandler struct {
	service *service.WeChatBotService
}

func NewWeChatBotHandler(botService *service.WeChatBotService) *WeChatBotHandler {
	return &WeChatBotHandler{service: botService}
}

func (h *WeChatBotHandler) GetUserStatus(c *gin.Context) {
	userID, ok := weChatBotUserID(c)
	if !ok {
		return
	}
	result, err := h.service.GetUserStatus(c.Request.Context(), userID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

type updateWeChatBotSettingsRequest struct {
	Enabled       bool   `json:"enabled"`
	NotifyAdmin   bool   `json:"notify_admin"`
	NotifyBalance bool   `json:"notify_balance"`
	NotifyLogin   bool   `json:"notify_login"`
	ChatEnabled   bool   `json:"chat_enabled"`
	ChatAPIKeyID  *int64 `json:"chat_api_key_id"`
	ChatModel     string `json:"chat_model"`
}

func (h *WeChatBotHandler) UpdateUserSettings(c *gin.Context) {
	userID, ok := weChatBotUserID(c)
	if !ok {
		return
	}
	var req updateWeChatBotSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid WeChat bot settings")
		return
	}
	result, err := h.service.UpdateUserSettings(c.Request.Context(), userID, service.WeChatBotSettingsUpdate{
		Enabled: req.Enabled, NotifyAdmin: req.NotifyAdmin,
		NotifyBalance: req.NotifyBalance, NotifyLogin: req.NotifyLogin,
		ChatEnabled: req.ChatEnabled, ChatAPIKeyID: req.ChatAPIKeyID, ChatModel: req.ChatModel,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func (h *WeChatBotHandler) Disconnect(c *gin.Context) {
	userID, ok := weChatBotUserID(c)
	if !ok {
		return
	}
	if err := h.service.Disconnect(c.Request.Context(), userID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"success": true})
}

func (h *WeChatBotHandler) SendTest(c *gin.Context) {
	userID, ok := weChatBotUserID(c)
	if !ok {
		return
	}
	if err := h.service.SendTestNotification(c.Request.Context(), userID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"queued": true})
}

func (h *WeChatBotHandler) GetAdminStatus(c *gin.Context) {
	result, err := h.service.GetAdminStatus(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func (h *WeChatBotHandler) CreateLoginQR(c *gin.Context) {
	userID, ok := weChatBotUserID(c)
	if !ok {
		return
	}
	result, err := h.service.CreateLoginQR(c.Request.Context(), userID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func (h *WeChatBotHandler) PollLoginQR(c *gin.Context) {
	userID, ok := weChatBotUserID(c)
	if !ok {
		return
	}
	loginID := strings.TrimSpace(c.Param("login_id"))
	if loginID == "" || len(loginID) > 80 {
		response.BadRequest(c, "Invalid login session")
		return
	}
	result, err := h.service.PollLoginQR(c.Request.Context(), userID, loginID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

type weChatBotBroadcastRequest struct {
	Title   string  `json:"title"`
	Message string  `json:"message" binding:"required"`
	UserIDs []int64 `json:"user_ids"`
}

func (h *WeChatBotHandler) Broadcast(c *gin.Context) {
	var req weChatBotBroadcastRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "A broadcast message is required")
		return
	}
	if utf8.RuneCountInString(req.Title) > 200 || utf8.RuneCountInString(req.Message) > 4500 || len(req.UserIDs) > 10000 {
		response.BadRequest(c, "WeChat broadcast is too large")
		return
	}
	for _, userID := range req.UserIDs {
		if userID <= 0 {
			response.BadRequest(c, "Invalid user ID")
			return
		}
	}
	queued, err := h.service.BroadcastAdmin(c.Request.Context(), req.Title, req.Message, req.UserIDs)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"queued": queued})
}

func weChatBotUserID(c *gin.Context) (int64, bool) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return 0, false
	}
	return subject.UserID, true
}
