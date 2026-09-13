//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type openAIAPIKeyRotationStoreStub struct {
	mu          sync.Mutex
	fingerprint string
	active      int
	failures    int
	generation  int64
}

func (s *openAIAPIKeyRotationStoreStub) GetSessionAccountID(context.Context, int64, string) (int64, error) {
	return 0, ErrStickySessionNotFound
}
func (s *openAIAPIKeyRotationStoreStub) SetSessionAccountID(context.Context, int64, string, int64, time.Duration) error {
	return nil
}
func (s *openAIAPIKeyRotationStoreStub) RefreshSessionTTL(context.Context, int64, string, time.Duration) error {
	return nil
}
func (s *openAIAPIKeyRotationStoreStub) DeleteSessionAccountID(context.Context, int64, string) error {
	return nil
}
func (s *openAIAPIKeyRotationStoreStub) SetGrokVideoPendingBilling(context.Context, string, []byte, time.Duration) error {
	return nil
}
func (s *openAIAPIKeyRotationStoreStub) GetGrokVideoPendingBilling(context.Context, string) ([]byte, error) {
	return nil, nil
}
func (s *openAIAPIKeyRotationStoreStub) ClaimGrokVideoBilled(context.Context, string, time.Duration) (bool, error) {
	return true, nil
}
func (s *openAIAPIKeyRotationStoreStub) ReleaseGrokVideoBilled(context.Context, string) error {
	return nil
}
func (s *openAIAPIKeyRotationStoreStub) SetReasoningContent(context.Context, string, string, time.Duration) error {
	return nil
}
func (s *openAIAPIKeyRotationStoreStub) GetReasoningContent(context.Context, string) (string, error) {
	return "", ErrReasoningContentNotFound
}

func (s *openAIAPIKeyRotationStoreStub) ensurePool(fingerprint string, poolSize int) {
	if s.fingerprint != fingerprint || s.active < 0 || s.active >= poolSize {
		s.fingerprint = fingerprint
		s.active = 0
		s.failures = 0
		s.generation = 0
	}
}

func (s *openAIAPIKeyRotationStoreStub) ResolveOpenAIAPIKeyIndex(_ context.Context, _ int64, fingerprint string, poolSize int) (OpenAIAPIKeyRotationSelection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePool(fingerprint, poolSize)
	return OpenAIAPIKeyRotationSelection{ActiveIndex: s.active, Generation: s.generation}, nil
}

func (s *openAIAPIKeyRotationStoreStub) RecordOpenAIAPIKeyStreamFailure(_ context.Context, _ int64, fingerprint string, attemptedIndex int, attemptedGeneration int64, poolSize, threshold int) (OpenAIAPIKeyRotationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fingerprint != fingerprint {
		return OpenAIAPIKeyRotationResult{}, nil
	}
	if s.active != attemptedIndex || s.generation != attemptedGeneration {
		return OpenAIAPIKeyRotationResult{ActiveIndex: s.active, FailureCount: s.failures, Generation: s.generation}, nil
	}
	s.failures++
	if s.failures >= threshold {
		s.active = (s.active + 1) % poolSize
		s.generation++
		s.failures = 0
		return OpenAIAPIKeyRotationResult{ActiveIndex: s.active, Generation: s.generation, Rotated: true, Recorded: true}, nil
	}
	return OpenAIAPIKeyRotationResult{ActiveIndex: s.active, FailureCount: s.failures, Generation: s.generation, Recorded: true}, nil
}

func (s *openAIAPIKeyRotationStoreStub) ResetOpenAIAPIKeyStreamFailures(_ context.Context, _ int64, fingerprint string, attemptedIndex int, attemptedGeneration int64) (int, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fingerprint != fingerprint || s.active != attemptedIndex || s.generation != attemptedGeneration {
		return 0, false, nil
	}
	previous := s.failures
	s.failures = 0
	return previous, true, nil
}

func TestNormalizeOpenAIAPIKeyListCredentials(t *testing.T) {
	credentials := map[string]any{
		"api_key":      "sk-primary",
		"api_key_list": []any{" sk-fallback ", "sk-primary", "sk-fallback", ""},
	}
	require.NoError(t, NormalizeOpenAIAPIKeyListCredentials(PlatformOpenAI, AccountTypeAPIKey, credentials))
	require.Equal(t, []string{"sk-fallback"}, credentials[OpenAIAPIKeyListCredentialKey])

	require.Error(t, NormalizeOpenAIAPIKeyListCredentials(PlatformOpenAI, AccountTypeAPIKey, map[string]any{
		"api_key_list": "sk-not-an-array",
	}))
	require.Error(t, NormalizeOpenAIAPIKeyListCredentials(PlatformAnthropic, AccountTypeAPIKey, map[string]any{
		"api_key_list": []any{"sk-fallback"},
	}))
}

func TestOpenAIAPIKeyRotationAfterThreeCommittedStreamFailures(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &openAIAPIKeyRotationStoreStub{}
	svc := &OpenAIGatewayService{cache: store}
	account := &Account{
		ID:       77,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":      "sk-primary",
			"api_key_list": []any{"sk-fallback"},
		},
	}
	upstream := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n" +
		"data: {\"type\":\"response.failed\",\"error\":{\"code\":\"server_error\",\"message\":\"upstream processing failed\"}}\n\n"

	for attempt := 1; attempt <= 3; attempt++ {
		ctx := withOpenAIAPIKeyRotationTracker(context.Background())
		token, _, err := svc.GetAccessToken(ctx, account)
		require.NoError(t, err)
		require.Equal(t, "sk-primary", token)

		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       ioNopCloserString(upstream),
		}
		_, err = svc.handleStreamingResponseWithReasoningAndTimeout(
			ctx, resp, c, account, time.Now(), "gpt-test", "gpt-test", "", 0,
		)
		require.Error(t, err)
	}

	ctx := withOpenAIAPIKeyRotationTracker(context.Background())
	token, _, err := svc.GetAccessToken(ctx, account)
	require.NoError(t, err)
	require.Equal(t, "sk-fallback", token)
}

func TestOpenAIAPIKeyRotationIgnoresPreOutputFailedEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &openAIAPIKeyRotationStoreStub{}
	svc := &OpenAIGatewayService{cache: store}
	account := &Account{
		ID:       78,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":      "sk-primary",
			"api_key_list": []any{"sk-fallback"},
		},
	}
	ctx := withOpenAIAPIKeyRotationTracker(context.Background())
	_, _, err := svc.GetAccessToken(ctx, account)
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: ioNopCloserString(
			"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp\"}}\n\n" +
				"data: {\"type\":\"response.failed\",\"error\":{\"code\":\"server_error\",\"message\":\"failed\"}}\n\n",
		),
	}
	_, err = svc.handleStreamingResponseWithReasoningAndTimeout(
		ctx, resp, c, account, time.Now(), "gpt-test", "gpt-test", "", 0,
	)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Zero(t, store.failures)
	require.Zero(t, store.active)
}

func ioNopCloserString(value string) *nopStringReadCloser {
	return &nopStringReadCloser{Reader: strings.NewReader(value)}
}

type nopStringReadCloser struct {
	*strings.Reader
}

func (r *nopStringReadCloser) Close() error { return nil }
