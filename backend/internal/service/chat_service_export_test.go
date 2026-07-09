package service

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

func TestChatServiceStreamExportUsesStablePageCursor(t *testing.T) {
	originalCursorTime := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	hydratedUpdatedAt := originalCursorTime.Add(2 * time.Hour)
	repo := &chatExportCursorRepo{
		firstPage: make([]ChatConversationExportRecord, chatExportBatchSize),
	}
	for i := range repo.firstPage {
		repo.firstPage[i] = ChatConversationExportRecord{
			Conversation: ChatConversation{
				ID:        int64(i + 1),
				UserID:    10,
				Title:     "Exported chat",
				UpdatedAt: hydratedUpdatedAt,
			},
			Cursor: ChatConversationExportCursor{
				UpdatedAt: originalCursorTime.Add(-time.Duration(i) * time.Minute),
				ID:        int64(i + 1),
			},
		}
	}
	expectedNextCursor := repo.firstPage[len(repo.firstPage)-1].Cursor

	var visited int
	svc := NewChatService(repo, nil)
	err := svc.StreamExportConversations(context.Background(), 10, func(conversation ChatConversation) error {
		visited++
		require.Equal(t, hydratedUpdatedAt, conversation.UpdatedAt)
		return nil
	})

	require.NoError(t, err)
	require.Equal(t, chatExportBatchSize, visited)
	require.Len(t, repo.cursors, 2)
	require.Nil(t, repo.cursors[0])
	require.NotNil(t, repo.cursors[1])
	require.Equal(t, expectedNextCursor, *repo.cursors[1])
}

func TestNormalizeChatTitleTruncatesMultibyteTitleToByteLimit(t *testing.T) {
	firstMessage := `不使用任何外部工具回答以下问题：

在一个黑色的袋子里放有三种口味的糖果，每种糖果有两种不同的形状（圆形和五角星形，不同的形状靠手感可以分辨）。现已知不同口味的糖和不同形状的数量统计如下表。参赛者需要在活动前决定摸出的糖果数目，那么，最少取出多少个糖果才能保证手中同时拥有不同形状的苹果味和桃子味的糖？（同时手中有圆形苹果味匹配五角星桃子味糖果，或者有圆形桃子味匹配五角星苹果味糖果都满足要求）`
	frontendTitle := strings.TrimSpace(strings.Join(strings.Fields(firstMessage), " "))
	if utf8.RuneCountInString(frontendTitle) > 60 {
		runes := []rune(frontendTitle)
		frontendTitle = string(runes[:57]) + "..."
	}
	require.Greater(t, len(frontendTitle), ChatTitleMaxLen)

	title := normalizeChatTitle(frontendTitle)

	require.NotEmpty(t, title)
	require.True(t, utf8.ValidString(title))
	require.LessOrEqual(t, len(title), ChatTitleMaxLen)
	require.LessOrEqual(t, utf8.RuneCountInString(title), ChatTitleMaxLen)
}

type chatExportCursorRepo struct {
	firstPage []ChatConversationExportRecord
	cursors   []*ChatConversationExportCursor
}

func (r *chatExportCursorRepo) ListConversationsForExport(_ context.Context, _ int64, cursor *ChatConversationExportCursor, _ int) ([]ChatConversationExportRecord, error) {
	r.cursors = append(r.cursors, copyChatExportCursor(cursor))
	if len(r.cursors) == 1 {
		return r.firstPage, nil
	}
	return nil, nil
}

func copyChatExportCursor(cursor *ChatConversationExportCursor) *ChatConversationExportCursor {
	if cursor == nil {
		return nil
	}
	copied := *cursor
	return &copied
}

func (r *chatExportCursorRepo) CreateConversation(context.Context, *ChatConversation) error {
	return nil
}

func (r *chatExportCursorRepo) GetConversation(context.Context, int64, int64, bool) (*ChatConversation, error) {
	return nil, nil
}

func (r *chatExportCursorRepo) ListConversations(context.Context, int64, pagination.PaginationParams) ([]ChatConversation, *pagination.PaginationResult, error) {
	return nil, nil, nil
}

func (r *chatExportCursorRepo) UpdateConversation(context.Context, *ChatConversation) error {
	return nil
}

func (r *chatExportCursorRepo) SoftDeleteConversation(context.Context, int64, int64) error {
	return nil
}

func (r *chatExportCursorRepo) CreateMessage(context.Context, *ChatMessage) error {
	return nil
}

func (r *chatExportCursorRepo) DeleteMessage(context.Context, int64, int64, int64) error {
	return nil
}
