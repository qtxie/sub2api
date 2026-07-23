//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeUpstreamBaseURLKey(t *testing.T) {
	t.Parallel()

	require.Equal(t, "https://relay.example.com/v1", normalizeUpstreamBaseURLKey("https://relay.example.com/v1/"))
	require.Equal(t, "https://relay.example.com/v1", normalizeUpstreamBaseURLKey("HTTPS://RELAY.EXAMPLE.COM/v1"))
	require.Equal(t, "https://relay.example.com:443/v1", normalizeUpstreamBaseURLKey("https://RELAY.example.com:443/v1/"))
	require.Equal(t, "", normalizeUpstreamBaseURLKey("   "))
}

func TestNormalizeAccountUpstreamBaseURLKeyAPIKeyAndOAuth(t *testing.T) {
	t.Parallel()

	apiKey := &Account{
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://relay-a.example/v1/"},
	}
	require.Equal(t, "https://relay-a.example/v1", NormalizeAccountUpstreamBaseURLKey(apiKey))

	oauth := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
	}
	require.Equal(t, "https://chatgpt.com", NormalizeAccountUpstreamBaseURLKey(oauth))

	oauthCustom := &Account{
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"base_url": "https://custom-oauth.example/v1"},
	}
	require.Equal(t, "https://custom-oauth.example/v1", NormalizeAccountUpstreamBaseURLKey(oauthCustom))
}

func TestMarkOpenAIFailoverAccountExcludedPreOutputTimeout(t *testing.T) {
	t.Parallel()

	failedIDs := make(map[int64]struct{})
	excludedURLs := make(map[string]struct{})
	account := &Account{
		ID:          11,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://relay.example/v1"},
	}

	MarkOpenAIFailoverAccountExcluded(failedIDs, excludedURLs, account, &UpstreamFailoverError{
		Reason: GatewayFailureReasonFirstOutputTimeout,
	})
	_, okID := failedIDs[11]
	require.True(t, okID)
	_, okURL := excludedURLs["https://relay.example/v1"]
	require.True(t, okURL)

	// Non pre-output failures only exclude the account ID.
	failedIDs2 := make(map[int64]struct{})
	excludedURLs2 := make(map[string]struct{})
	MarkOpenAIFailoverAccountExcluded(failedIDs2, excludedURLs2, account, &UpstreamFailoverError{
		StatusCode: http.StatusTooManyRequests,
	})
	_, okID = failedIDs2[11]
	require.True(t, okID)
	require.Empty(t, excludedURLs2)
}

func TestIsAccountExcludedByUpstreamBaseURL(t *testing.T) {
	t.Parallel()

	excluded := map[string]struct{}{
		"https://relay.example/v1": {},
	}
	same := &Account{
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://relay.example/v1/"},
	}
	other := &Account{
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://other.example/v1"},
	}
	require.True(t, isAccountExcludedByUpstreamBaseURL(same, excluded))
	require.False(t, isAccountExcludedByUpstreamBaseURL(other, excluded))
	require.False(t, isAccountExcludedByUpstreamBaseURL(same, nil))
}

func TestWithOpenAIExcludedUpstreamBaseURLsContext(t *testing.T) {
	t.Parallel()

	urls := map[string]struct{}{"https://relay.example/v1": {}}
	ctx := WithOpenAIExcludedUpstreamBaseURLs(context.Background(), urls)
	got := openAIExcludedUpstreamBaseURLsFromContext(ctx)
	require.Equal(t, urls, got)
	require.Nil(t, openAIExcludedUpstreamBaseURLsFromContext(context.Background()))
}

func TestIsOpenAIResponseHeaderTimeoutError(t *testing.T) {
	t.Parallel()

	require.True(t, isOpenAIResponseHeaderTimeoutError(errors.New(`Get "https://x": net/http: timeout awaiting response headers`)))
	require.True(t, isOpenAIResponseHeaderTimeoutError(errors.New(`Post "https://x": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`)))
	require.False(t, isOpenAIResponseHeaderTimeoutError(errors.New("connection refused")))
	require.False(t, isOpenAIResponseHeaderTimeoutError(nil))
}

func TestUpstreamFailoverErrorIsOpenAIPreOutputTimeout(t *testing.T) {
	t.Parallel()

	require.True(t, (&UpstreamFailoverError{Reason: GatewayFailureReasonFirstOutputTimeout}).IsOpenAIPreOutputTimeout())
	require.True(t, (&UpstreamFailoverError{Reason: GatewayFailureReasonResponseHeaderTimeout}).IsOpenAIPreOutputTimeout())
	require.False(t, (&UpstreamFailoverError{StatusCode: 502}).IsOpenAIPreOutputTimeout())
	require.False(t, (*UpstreamFailoverError)(nil).IsOpenAIPreOutputTimeout())
}

func TestHandleOpenAIUpstreamTransportErrorTagsResponseHeaderTimeout(t *testing.T) {
	t.Parallel()

	svc := &OpenAIGatewayService{}
	account := &Account{ID: 7, Name: "a", Platform: PlatformOpenAI}
	err := svc.handleOpenAIUpstreamTransportError(
		context.Background(),
		nil,
		account,
		errors.New(`Post "https://api.example": net/http: timeout awaiting response headers`),
		false,
	)
	var failoverErr *UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr))
	require.Equal(t, GatewayFailureReasonResponseHeaderTimeout, failoverErr.Reason)
	require.Equal(t, http.StatusGatewayTimeout, failoverErr.StatusCode)
	require.True(t, failoverErr.IsOpenAIPreOutputTimeout())
}
