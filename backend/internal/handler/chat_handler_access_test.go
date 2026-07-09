package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type chatHandlerAccessUserRepo struct {
	service.UserRepository
	user *service.User
}

func (r *chatHandlerAccessUserRepo) GetByID(_ context.Context, _ int64) (*service.User, error) {
	if r.user == nil {
		return nil, service.ErrUserNotFound
	}
	cloned := *r.user
	return &cloned, nil
}

func TestChatListConversationsRejectsDisabledUser(t *testing.T) {
	gin.SetMode(gin.TestMode)

	chatSvc := service.NewChatService(nil, nil, &chatHandlerAccessUserRepo{
		user: &service.User{ID: 42, ChatEnabled: false},
	})
	handler := NewChatHandler(chatSvc, nil, nil, nil)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/chat/conversations", nil)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 42})

	handler.ListConversations(c)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}
