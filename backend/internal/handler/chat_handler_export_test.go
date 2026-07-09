package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

func TestChatExportStreamsOnlyAuthenticatedUsersActiveConversations(t *testing.T) {
	gin.SetMode(gin.TestMode)
	client := newChatExportTestClient(t)
	ctx := t.Context()

	user := mustCreateChatExportUser(t, client, "chat-export-user@example.com")
	otherUser := mustCreateChatExportUser(t, client, "chat-export-other@example.com")

	active := mustCreateChatExportConversation(t, client, user.ID, "Active chat")
	mustCreateChatExportMessage(t, client, user.ID, active.ID, service.ChatRoleUser, "hello")
	mustCreateChatExportMessage(t, client, user.ID, active.ID, service.ChatRoleAssistant, "hi there")

	deleted := mustCreateChatExportConversation(t, client, user.ID, "Deleted chat")
	_, err := client.ChatConversation.UpdateOneID(deleted.ID).SetDeletedAt(time.Now()).Save(ctx)
	require.NoError(t, err)

	other := mustCreateChatExportConversation(t, client, otherUser.ID, "Other user chat")
	mustCreateChatExportMessage(t, client, otherUser.ID, other.ID, service.ChatRoleUser, "not yours")

	chatRepo := repository.NewChatConversationRepository(client, nil)
	chatService := service.NewChatService(chatRepo, nil, nil)
	handler := NewChatHandler(chatService, nil, nil, nil)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/chat/export", nil)
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: user.ID})

	handler.ExportConversations(c)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "application/json; charset=utf-8", w.Header().Get("Content-Type"))
	require.Contains(t, w.Header().Get("Content-Disposition"), "sub2api-chat-export-")

	var body struct {
		Version       int   `json:"version"`
		ExportedAt    int64 `json:"exported_at"`
		Conversations []struct {
			ID       int64  `json:"id"`
			UserID   int64  `json:"user_id"`
			Title    string `json:"title"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		} `json:"conversations"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, 1, body.Version)
	require.NotZero(t, body.ExportedAt)
	require.Len(t, body.Conversations, 1)
	require.Equal(t, active.ID, body.Conversations[0].ID)
	require.Equal(t, user.ID, body.Conversations[0].UserID)
	require.Equal(t, "Active chat", body.Conversations[0].Title)
	require.Len(t, body.Conversations[0].Messages, 2)
	require.Equal(t, "hello", body.Conversations[0].Messages[0].Content)
	require.Equal(t, "hi there", body.Conversations[0].Messages[1].Content)
}

func newChatExportTestClient(t *testing.T) *dbent.Client {
	t.Helper()

	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s_%d?mode=memory&cache=shared&_fk=1", "chat_export", time.Now().UnixNano()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func mustCreateChatExportUser(t *testing.T, client *dbent.Client, email string) *dbent.User {
	t.Helper()
	user, err := client.User.Create().
		SetEmail(email).
		SetPasswordHash("test-password-hash").
		SetRole(service.RoleUser).
		SetStatus(service.StatusActive).
		Save(t.Context())
	require.NoError(t, err)
	return user
}

func mustCreateChatExportConversation(t *testing.T, client *dbent.Client, userID int64, title string) *dbent.ChatConversation {
	t.Helper()
	conversation, err := client.ChatConversation.Create().
		SetUserID(userID).
		SetTitle(title).
		SetModel("gpt-test").
		SetSystemPrompt("").
		Save(t.Context())
	require.NoError(t, err)
	return conversation
}

func mustCreateChatExportMessage(t *testing.T, client *dbent.Client, userID, conversationID int64, role, content string) *dbent.ChatMessage {
	t.Helper()
	message, err := client.ChatMessage.Create().
		SetUserID(userID).
		SetConversationID(conversationID).
		SetRole(role).
		SetContent(content).
		SetStatus(service.ChatMessageStatusComplete).
		SetErrorMessage("").
		SetMetadata(map[string]any{}).
		Save(t.Context())
	require.NoError(t, err)
	return message
}
