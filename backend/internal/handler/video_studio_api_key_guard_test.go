package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type videoStudioPendingGuardStub struct {
	pending bool
	err     error
	userID  int64
	keyID   int64
}

func (s *videoStudioPendingGuardStub) HasPendingForAPIKey(_ context.Context, userID, keyID int64) (bool, error) {
	s.userID = userID
	s.keyID = keyID
	return s.pending, s.err
}

func TestVideoStudioAPIKeyGuardBlocksDisruptiveMutation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	guard := &videoStudioPendingGuardStub{pending: true}
	handler := &APIKeyHandler{videoStudioPendingGuard: guard}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodDelete, "/keys/7", nil)

	require.False(t, handler.ensureNoPendingVideoTask(ctx, 42, 7))
	require.Equal(t, int64(42), guard.userID)
	require.Equal(t, int64(7), guard.keyID)
	require.Equal(t, http.StatusConflict, recorder.Code)
	require.Contains(t, recorder.Body.String(), "pending video generation")

	require.True(t, videoStudioAPIKeyUpdateCanDisruptPendingTask(UpdateAPIKeyRequest{Status: "inactive"}))
	require.True(t, videoStudioAPIKeyUpdateCanDisruptPendingTask(UpdateAPIKeyRequest{IPWhitelist: &[]string{"127.0.0.1"}}))
	require.False(t, videoStudioAPIKeyUpdateCanDisruptPendingTask(UpdateAPIKeyRequest{Name: "renamed"}))
}

func TestVideoStudioAPIKeyGuardFailsClosedOnStoreError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &APIKeyHandler{videoStudioPendingGuard: &videoStudioPendingGuardStub{err: errors.New("redis unavailable")}}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/keys/7", nil)

	require.False(t, handler.ensureNoPendingVideoTask(ctx, 42, 7))
	require.Equal(t, http.StatusInternalServerError, recorder.Code)
}
