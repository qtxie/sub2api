package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newOpenAIStreamDrainTestContext(t *testing.T, ctx context.Context) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
	return c
}

func TestOpenAIStreamDrainBoundsPreOutputDisconnect(t *testing.T) {
	clientCtx, cancelClient := context.WithCancel(context.Background())
	c := newOpenAIStreamDrainTestContext(t, clientCtx)
	stop := StartOpenAIStreamDrain(c, clientCtx, OpenAIStreamDrainSettings{
		PreDisconnectDrain:  25 * time.Millisecond,
		PostDisconnectDrain: time.Second,
	})
	defer stop()

	workCtx := OpenAIStreamDrainContext(c, clientCtx)
	upstreamCtx, release := openAIUpstreamContext(workCtx)
	defer release()
	require.Same(t, workCtx, upstreamCtx)

	cancelClient()
	require.Eventually(t, func() bool { return !OpenAIStreamDrainClientConnected(c) }, time.Second, time.Millisecond)
	select {
	case <-workCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("pre-output drain did not cancel the upstream context")
	}
	require.ErrorIs(t, context.Cause(workCtx), errOpenAIStreamDrainExpired)
}

func TestOpenAIStreamDrainUsesPostOutputDurationAfterSemanticCommit(t *testing.T) {
	clientCtx, cancelClient := context.WithCancel(context.Background())
	c := newOpenAIStreamDrainTestContext(t, clientCtx)
	stop := StartOpenAIStreamDrain(c, clientCtx, OpenAIStreamDrainSettings{
		PreDisconnectDrain:  20 * time.Millisecond,
		PostDisconnectDrain: 200 * time.Millisecond,
	})
	defer stop()

	drain := getOpenAIStreamDrain(c)
	require.Equal(t, 20*time.Millisecond, drain.disconnectDrainDuration())
	MarkOpenAIStreamDrainSemantic(c)
	require.True(t, OpenAIStreamDrainSemanticStarted(c))
	require.Equal(t, 200*time.Millisecond, drain.disconnectDrainDuration())

	workCtx := OpenAIStreamDrainContext(c, clientCtx)
	cancelClient()
	select {
	case <-workCtx.Done():
		t.Fatal("post-output drain used the shorter pre-output duration")
	case <-time.After(60 * time.Millisecond):
	}
	select {
	case <-workCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("post-output drain did not cancel the upstream context")
	}
}

func TestOpenAIStreamDrainHandlesExplicitWriterDisconnect(t *testing.T) {
	clientCtx := context.Background()
	c := newOpenAIStreamDrainTestContext(t, clientCtx)
	stop := StartOpenAIStreamDrain(c, clientCtx, OpenAIStreamDrainSettings{
		PreDisconnectDrain: 20 * time.Millisecond,
	})
	defer stop()

	workCtx := OpenAIStreamDrainContext(c, clientCtx)
	MarkOpenAIStreamDrainClientDisconnected(c)
	select {
	case <-workCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("explicit disconnect did not bound the upstream context")
	}
	require.False(t, OpenAIStreamDrainClientConnected(c))
}
