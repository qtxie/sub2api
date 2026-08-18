package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type videoStudioTrackerStoreStub struct {
	task        *VideoStudioTask
	dueAt       *time.Time
	claim       *VideoStudioTaskClaim
	commitCount int
}

func (s *videoStudioTrackerStoreStub) Enqueue(_ context.Context, task *VideoStudioTask, _ time.Duration, dueAt time.Time) error {
	clone := *task
	s.task = &clone
	s.dueAt = &dueAt
	return nil
}

func (s *videoStudioTrackerStoreStub) Get(_ context.Context, _ string) (*VideoStudioTask, error) {
	if s.task == nil {
		return nil, ErrVideoStudioTaskNotFound
	}
	clone := *s.task
	return &clone, nil
}

func (s *videoStudioTrackerStoreStub) HasPendingForAPIKey(_ context.Context, userID, apiKeyID int64, now time.Time) (bool, error) {
	return s.task != nil && s.task.UserID == userID && s.task.APIKeyID == apiKeyID &&
		s.task.Status == VideoStudioTaskStatusPending && now.Before(s.task.ExpiresAt), nil
}

func (s *videoStudioTrackerStoreStub) ClaimDue(_ context.Context, _ time.Time, _ time.Duration) (*VideoStudioTaskClaim, error) {
	if s.claim == nil {
		return nil, ErrVideoStudioQueueEmpty
	}
	claim := s.claim
	s.claim = nil
	return claim, nil
}

func (s *videoStudioTrackerStoreStub) CommitClaim(_ context.Context, claim *VideoStudioTaskClaim, _ time.Duration, nextPollAt *time.Time) error {
	s.commitCount++
	clone := *claim.Task
	s.task = &clone
	s.dueAt = nextPollAt
	return nil
}

type videoStudioPollerStub struct {
	result *VideoStudioPollResult
	err    error
}

func (s videoStudioPollerStub) PollVideoStudioTask(context.Context, *VideoStudioTask) (*VideoStudioPollResult, error) {
	return s.result, s.err
}

func TestVideoStudioTrackerRegisterAndOwnerIsolation(t *testing.T) {
	store := &videoStudioTrackerStoreStub{}
	tracker := NewVideoStudioTracker(store, videoStudioPollerStub{}, VideoStudioTrackerOptions{
		TaskTTL:      2 * time.Hour,
		PollInterval: 3 * time.Second,
	})

	err := tracker.Register(context.Background(), VideoStudioTask{
		RequestID: "request-1", UserID: 7, APIKeyID: 9,
		Model: "grok-imagine-video-1.5", Duration: 8, AspectRatio: "16:9", Resolution: "720p",
	})
	require.NoError(t, err)
	require.Equal(t, VideoStudioTaskStatusPending, store.task.Status)
	require.WithinDuration(t, store.task.CreatedAt.Add(2*time.Hour), store.task.ExpiresAt, time.Second)
	require.WithinDuration(t, time.Now().Add(3*time.Second), *store.dueAt, time.Second)
	pending, err := tracker.HasPendingForAPIKey(context.Background(), 7, 9)
	require.NoError(t, err)
	require.True(t, pending)

	got, err := tracker.Get(context.Background(), "request-1", 7, 9)
	require.NoError(t, err)
	require.Equal(t, "720p", got.Resolution)
	_, err = tracker.Get(context.Background(), "request-1", 8, 9)
	require.ErrorIs(t, err, ErrVideoStudioTaskNotFound)
}

func TestVideoStudioTrackerRunOnceReschedulesPending(t *testing.T) {
	now := time.Now().UTC()
	task := &VideoStudioTask{
		RequestID: "request-2", UserID: 7, APIKeyID: 9,
		Status: VideoStudioTaskStatusPending, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	store := &videoStudioTrackerStoreStub{claim: &VideoStudioTaskClaim{Task: task, Token: "claim"}}
	progress := 42
	tracker := NewVideoStudioTracker(store, videoStudioPollerStub{result: &VideoStudioPollResult{
		Status: VideoStudioTaskStatusPending, Progress: &progress,
	}}, VideoStudioTrackerOptions{PollInterval: 5 * time.Second})

	require.NoError(t, tracker.RunOnce(context.Background()))
	require.Equal(t, 1, store.commitCount)
	require.Equal(t, VideoStudioTaskStatusPending, store.task.Status)
	require.NotNil(t, store.task.Progress)
	require.Equal(t, 42, *store.task.Progress)
	require.NotNil(t, store.dueAt)
	require.WithinDuration(t, time.Now().Add(5*time.Second), *store.dueAt, time.Second)
}

func TestVideoStudioTrackerRunOnceCompletesTerminalTask(t *testing.T) {
	now := time.Now().UTC()
	task := &VideoStudioTask{
		RequestID: "request-3", UserID: 7, APIKeyID: 9,
		Status: VideoStudioTaskStatusPending, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	progress := 100
	store := &videoStudioTrackerStoreStub{claim: &VideoStudioTaskClaim{Task: task, Token: "claim"}}
	tracker := NewVideoStudioTracker(store, videoStudioPollerStub{result: &VideoStudioPollResult{
		Status: VideoStudioTaskStatusDone, Progress: &progress,
	}}, VideoStudioTrackerOptions{})

	require.NoError(t, tracker.RunOnce(context.Background()))
	require.Equal(t, VideoStudioTaskStatusDone, store.task.Status)
	require.NotNil(t, store.task.CompletedAt)
	require.Nil(t, store.dueAt)
}

func TestVideoStudioTrackerPermanentPollFailureIsTerminal(t *testing.T) {
	now := time.Now().UTC()
	task := &VideoStudioTask{
		RequestID: "request-4", UserID: 7, APIKeyID: 9,
		Status: VideoStudioTaskStatusPending, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	store := &videoStudioTrackerStoreStub{claim: &VideoStudioTaskClaim{Task: task, Token: "claim"}}
	tracker := NewVideoStudioTracker(store, videoStudioPollerStub{err: &VideoStudioPollError{
		StatusCode: 404, Permanent: true, Message: "task is unavailable",
	}}, VideoStudioTrackerOptions{})

	require.NoError(t, tracker.RunOnce(context.Background()))
	require.Equal(t, VideoStudioTaskStatusFailed, store.task.Status)
	require.Equal(t, "task is unavailable", store.task.LastError)
	require.Nil(t, store.dueAt)
}

func TestVideoStudioTrackerTransientPollFailureBacksOff(t *testing.T) {
	now := time.Now().UTC()
	task := &VideoStudioTask{
		RequestID: "request-5", UserID: 7, APIKeyID: 9,
		Status: VideoStudioTaskStatusPending, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	store := &videoStudioTrackerStoreStub{claim: &VideoStudioTaskClaim{Task: task, Token: "claim"}}
	tracker := NewVideoStudioTracker(store, videoStudioPollerStub{err: errors.New("network unavailable")}, VideoStudioTrackerOptions{
		PollInterval: 2 * time.Second,
	})

	require.NoError(t, tracker.RunOnce(context.Background()))
	require.Equal(t, 1, store.task.Attempts)
	require.Equal(t, VideoStudioTaskStatusPending, store.task.Status)
	require.NotNil(t, store.dueAt)
}

func TestIsValidVideoStudioRequestID(t *testing.T) {
	require.True(t, IsValidVideoStudioRequestID("d97415a1-5796-b7ec-379f-4e6819e08fdf"))
	require.False(t, IsValidVideoStudioRequestID("../request"))
	require.False(t, IsValidVideoStudioRequestID("request/id"))
}

type videoStudioAPIKeyLoaderStub struct {
	apiKey *APIKey
	err    error
}

func (s videoStudioAPIKeyLoaderStub) GetByID(context.Context, int64) (*APIKey, error) {
	return s.apiKey, s.err
}

func TestVideoStudioGatewayPollerUsesLocalAuthenticatedGateway(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/videos/request-6", r.URL.Path)
		require.Equal(t, "Bearer panel-key", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"done","progress":104,"video":{"url":"https://vidgen.x.ai/result.mp4"}}`))
	}))
	defer server.Close()

	cfg := videoStudioTestServerConfig(t, server.URL)
	poller := NewVideoStudioGatewayPoller(videoStudioAPIKeyLoaderStub{apiKey: &APIKey{
		ID: 9, UserID: 7, Key: "panel-key",
	}}, cfg)
	poller.httpClient = server.Client()

	result, err := poller.PollVideoStudioTask(context.Background(), &VideoStudioTask{
		RequestID: "request-6", UserID: 7, APIKeyID: 9,
	})
	require.NoError(t, err)
	require.Equal(t, VideoStudioTaskStatusDone, result.Status)
	require.NotNil(t, result.Progress)
	require.Equal(t, 100, *result.Progress)
}

func TestVideoStudioGatewayPollerClassifiesRetryableResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"slow down"}}`))
	}))
	defer server.Close()

	poller := NewVideoStudioGatewayPoller(videoStudioAPIKeyLoaderStub{apiKey: &APIKey{
		ID: 9, UserID: 7, Key: "panel-key",
	}}, videoStudioTestServerConfig(t, server.URL))
	poller.httpClient = server.Client()

	_, err := poller.PollVideoStudioTask(context.Background(), &VideoStudioTask{
		RequestID: "request-7", UserID: 7, APIKeyID: 9,
	})
	var pollErr *VideoStudioPollError
	require.ErrorAs(t, err, &pollErr)
	require.False(t, pollErr.Permanent)
	require.Equal(t, 7*time.Second, pollErr.RetryAfter)
	require.Equal(t, "slow down", pollErr.Message)
}

func TestVideoStudioGatewayPollerRejectsUnsupportedStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"queued"}`))
	}))
	defer server.Close()

	poller := NewVideoStudioGatewayPoller(videoStudioAPIKeyLoaderStub{apiKey: &APIKey{
		ID: 9, UserID: 7, Key: "panel-key",
	}}, videoStudioTestServerConfig(t, server.URL))
	poller.httpClient = server.Client()

	_, err := poller.PollVideoStudioTask(context.Background(), &VideoStudioTask{
		RequestID: "request-8", UserID: 7, APIKeyID: 9,
	})
	require.EqualError(t, err, "video gateway returned unsupported task status")
}

func videoStudioTestServerConfig(t *testing.T, rawURL string) *config.Config {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	require.NoError(t, err)
	port, err := strconv.Atoi(parsed.Port())
	require.NoError(t, err)
	return &config.Config{Server: config.ServerConfig{Host: parsed.Hostname(), Port: port}}
}
