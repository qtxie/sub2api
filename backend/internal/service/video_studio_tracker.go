package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

const (
	VideoStudioTaskStatusPending = "pending"
	VideoStudioTaskStatusDone    = "done"
	VideoStudioTaskStatusFailed  = "failed"
	VideoStudioTaskStatusExpired = "expired"

	defaultVideoStudioTaskTTL       = 24 * time.Hour
	defaultVideoStudioPollInterval  = 5 * time.Second
	defaultVideoStudioRetryMax      = time.Minute
	defaultVideoStudioClaimLease    = 30 * time.Second
	defaultVideoStudioIdleInterval  = time.Second
	defaultVideoStudioPollTimeout   = 20 * time.Second
	defaultVideoStudioStatusMaxBody = int64(1 << 20)
)

var (
	ErrVideoStudioTaskNotFound  = errors.New("video studio task not found")
	ErrVideoStudioQueueEmpty    = errors.New("video studio task queue is empty")
	ErrVideoStudioTaskClaimLost = errors.New("video studio task claim was lost")
)

// VideoStudioTask is the private, short-lived record used to reconcile an xAI
// deferred video request. API-key credentials and upstream signed URLs are never
// stored in this record.
type VideoStudioTask struct {
	RequestID   string     `json:"request_id"`
	UserID      int64      `json:"user_id"`
	APIKeyID    int64      `json:"api_key_id"`
	Model       string     `json:"model"`
	Duration    int        `json:"duration"`
	AspectRatio string     `json:"aspect_ratio"`
	Resolution  string     `json:"resolution"`
	Status      string     `json:"status"`
	Progress    *int       `json:"progress,omitempty"`
	Attempts    int        `json:"attempts,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

type VideoStudioTaskClaim struct {
	Task  *VideoStudioTask
	Token string
}

// VideoStudioTaskStore provides a leased due queue. Claim implementations must
// make an abandoned job eligible again after the lease expires.
type VideoStudioTaskStore interface {
	Enqueue(ctx context.Context, task *VideoStudioTask, ttl time.Duration, dueAt time.Time) error
	Get(ctx context.Context, requestID string) (*VideoStudioTask, error)
	HasPendingForAPIKey(ctx context.Context, userID, apiKeyID int64, now time.Time) (bool, error)
	ClaimDue(ctx context.Context, now time.Time, lease time.Duration) (*VideoStudioTaskClaim, error)
	CommitClaim(ctx context.Context, claim *VideoStudioTaskClaim, ttl time.Duration, nextPollAt *time.Time) error
}

type VideoStudioPollResult struct {
	Status       string
	Progress     *int
	ErrorMessage string
	RetryAfter   time.Duration
}

type VideoStudioTaskPoller interface {
	PollVideoStudioTask(ctx context.Context, task *VideoStudioTask) (*VideoStudioPollResult, error)
}

type VideoStudioAPIKeyLoader interface {
	GetByID(ctx context.Context, id int64) (*APIKey, error)
}

type VideoStudioTrackerOptions struct {
	TaskTTL      time.Duration
	PollInterval time.Duration
	RetryMax     time.Duration
	ClaimLease   time.Duration
	IdleInterval time.Duration
	PollTimeout  time.Duration
}

type VideoStudioTracker struct {
	store  VideoStudioTaskStore
	poller VideoStudioTaskPoller
	opts   VideoStudioTrackerOptions

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func NewVideoStudioTracker(store VideoStudioTaskStore, poller VideoStudioTaskPoller, opts VideoStudioTrackerOptions) *VideoStudioTracker {
	return &VideoStudioTracker{store: store, poller: poller, opts: normalizeVideoStudioTrackerOptions(opts)}
}

func normalizeVideoStudioTrackerOptions(opts VideoStudioTrackerOptions) VideoStudioTrackerOptions {
	if opts.TaskTTL <= 0 {
		opts.TaskTTL = defaultVideoStudioTaskTTL
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = defaultVideoStudioPollInterval
	}
	if opts.RetryMax <= 0 {
		opts.RetryMax = defaultVideoStudioRetryMax
	}
	if opts.ClaimLease <= 0 {
		opts.ClaimLease = defaultVideoStudioClaimLease
	}
	if opts.IdleInterval <= 0 {
		opts.IdleInterval = defaultVideoStudioIdleInterval
	}
	if opts.PollTimeout <= 0 {
		opts.PollTimeout = defaultVideoStudioPollTimeout
	}
	return opts
}

func (t *VideoStudioTracker) Register(ctx context.Context, task VideoStudioTask) error {
	if t == nil || t.store == nil {
		return errors.New("video studio tracker is unavailable")
	}
	task.RequestID = strings.TrimSpace(task.RequestID)
	if !IsValidVideoStudioRequestID(task.RequestID) || task.UserID <= 0 || task.APIKeyID <= 0 {
		return errors.New("invalid video studio task")
	}
	now := time.Now().UTC()
	if task.CreatedAt.IsZero() {
		task.CreatedAt = now
	}
	task.UpdatedAt = now
	task.ExpiresAt = task.CreatedAt.Add(t.opts.TaskTTL)
	task.Status = VideoStudioTaskStatusPending
	task.Model = strings.TrimSpace(task.Model)
	task.AspectRatio = strings.TrimSpace(task.AspectRatio)
	task.Resolution = strings.TrimSpace(task.Resolution)
	return t.store.Enqueue(ctx, &task, time.Until(task.ExpiresAt), now.Add(t.opts.PollInterval))
}

func (t *VideoStudioTracker) Get(ctx context.Context, requestID string, userID, apiKeyID int64) (*VideoStudioTask, error) {
	if t == nil || t.store == nil || !IsValidVideoStudioRequestID(requestID) {
		return nil, ErrVideoStudioTaskNotFound
	}
	task, err := t.store.Get(ctx, requestID)
	if err != nil {
		return nil, err
	}
	if task == nil || task.UserID != userID || task.APIKeyID != apiKeyID {
		return nil, ErrVideoStudioTaskNotFound
	}
	return task, nil
}

func (t *VideoStudioTracker) HasPendingForAPIKey(ctx context.Context, userID, apiKeyID int64) (bool, error) {
	if t == nil || t.store == nil || userID <= 0 || apiKeyID <= 0 {
		return false, nil
	}
	return t.store.HasPendingForAPIKey(ctx, userID, apiKeyID, time.Now().UTC())
}

func (t *VideoStudioTracker) Start() {
	if t == nil || t.store == nil || t.poller == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel = cancel
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		t.run(ctx)
	}()
}

func (t *VideoStudioTracker) Stop() {
	if t == nil {
		return
	}
	t.mu.Lock()
	cancel := t.cancel
	t.cancel = nil
	t.mu.Unlock()
	if cancel != nil {
		cancel()
		t.wg.Wait()
	}
}

func (t *VideoStudioTracker) run(ctx context.Context) {
	for ctx.Err() == nil {
		err := t.RunOnce(ctx)
		if err != nil && !errors.Is(err, ErrVideoStudioQueueEmpty) && ctx.Err() == nil {
			logger.L().Warn("video_studio.tracker_poll_failed", zap.Error(err))
		}
		if errors.Is(err, ErrVideoStudioQueueEmpty) || err != nil {
			videoStudioSleep(ctx, t.opts.IdleInterval)
		}
	}
}

func (t *VideoStudioTracker) RunOnce(ctx context.Context) error {
	if t == nil || t.store == nil || t.poller == nil {
		return nil
	}
	claim, err := t.store.ClaimDue(ctx, time.Now().UTC(), t.opts.ClaimLease)
	if err != nil {
		return err
	}
	if claim == nil || claim.Task == nil {
		return ErrVideoStudioQueueEmpty
	}
	task := claim.Task
	now := time.Now().UTC()
	if !task.ExpiresAt.IsZero() && !now.Before(task.ExpiresAt) {
		task.Status = VideoStudioTaskStatusExpired
		task.LastError = "video generation tracking expired"
		task.UpdatedAt = now
		task.CompletedAt = &now
		return t.store.CommitClaim(ctx, claim, time.Second, nil)
	}

	pollCtx, cancel := context.WithTimeout(ctx, t.opts.PollTimeout)
	result, pollErr := t.poller.PollVideoStudioTask(pollCtx, task)
	cancel()
	now = time.Now().UTC()
	task.UpdatedAt = now
	remainingTTL := time.Until(task.ExpiresAt)
	if remainingTTL <= 0 {
		remainingTTL = time.Second
	}

	if pollErr != nil {
		task.Attempts++
		task.LastError = boundedVideoStudioError(pollErr.Error())
		var upstreamErr *VideoStudioPollError
		if errors.As(pollErr, &upstreamErr) && upstreamErr.Permanent {
			task.Status = VideoStudioTaskStatusFailed
			task.CompletedAt = &now
			return t.store.CommitClaim(ctx, claim, remainingTTL, nil)
		}
		delay := t.retryDelay(task.Attempts)
		if errors.As(pollErr, &upstreamErr) && upstreamErr.RetryAfter > delay {
			delay = upstreamErr.RetryAfter
		}
		next := now.Add(delay)
		return t.store.CommitClaim(ctx, claim, remainingTTL, &next)
	}

	task.Attempts = 0
	task.LastError = boundedVideoStudioError(result.ErrorMessage)
	task.Progress = result.Progress
	task.Status = normalizeVideoStudioTaskStatus(result.Status)
	switch task.Status {
	case VideoStudioTaskStatusDone, VideoStudioTaskStatusFailed, VideoStudioTaskStatusExpired:
		task.CompletedAt = &now
		return t.store.CommitClaim(ctx, claim, remainingTTL, nil)
	default:
		task.Status = VideoStudioTaskStatusPending
		delay := t.opts.PollInterval
		if result.RetryAfter > delay {
			delay = result.RetryAfter
		}
		next := now.Add(delay)
		return t.store.CommitClaim(ctx, claim, remainingTTL, &next)
	}
}

func (t *VideoStudioTracker) retryDelay(attempt int) time.Duration {
	delay := t.opts.PollInterval
	for i := 1; i < attempt && delay < t.opts.RetryMax; i++ {
		delay *= 2
	}
	if delay > t.opts.RetryMax {
		return t.opts.RetryMax
	}
	return delay
}

func videoStudioSleep(ctx context.Context, duration time.Duration) {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func normalizeVideoStudioTaskStatus(value string) string {
	if status, ok := parseVideoStudioTaskStatus(value); ok {
		return status
	}
	return VideoStudioTaskStatusPending
}

func parseVideoStudioTaskStatus(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case VideoStudioTaskStatusPending:
		return VideoStudioTaskStatusPending, true
	case VideoStudioTaskStatusDone:
		return VideoStudioTaskStatusDone, true
	case VideoStudioTaskStatusFailed:
		return VideoStudioTaskStatusFailed, true
	case VideoStudioTaskStatusExpired:
		return VideoStudioTaskStatusExpired, true
	default:
		return "", false
	}
}

func boundedVideoStudioError(value string) string {
	value = strings.TrimSpace(value)
	if runes := []rune(value); len(runes) > 512 {
		return string(runes[:512])
	}
	return value
}

func IsValidVideoStudioRequestID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) == 0 || len(value) > 192 {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

type VideoStudioPollError struct {
	StatusCode int
	RetryAfter time.Duration
	Permanent  bool
	Message    string
}

func (e *VideoStudioPollError) Error() string {
	if e == nil {
		return "video status request failed"
	}
	if strings.TrimSpace(e.Message) != "" {
		return e.Message
	}
	return fmt.Sprintf("video status request failed with status %d", e.StatusCode)
}

// VideoStudioGatewayPoller deliberately polls the local authenticated gateway,
// not xAI directly. That keeps account affinity, failover and one-shot billing on
// the same path used by API clients and the panel status endpoint.
type VideoStudioGatewayPoller struct {
	apiKeys    VideoStudioAPIKeyLoader
	cfg        *config.Config
	httpClient *http.Client
}

func NewVideoStudioGatewayPoller(apiKeys VideoStudioAPIKeyLoader, cfg *config.Config) *VideoStudioGatewayPoller {
	return &VideoStudioGatewayPoller{
		apiKeys: apiKeys,
		cfg:     cfg,
		httpClient: &http.Client{
			Timeout: defaultVideoStudioPollTimeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (p *VideoStudioGatewayPoller) PollVideoStudioTask(ctx context.Context, task *VideoStudioTask) (*VideoStudioPollResult, error) {
	if p == nil || p.apiKeys == nil || p.httpClient == nil || task == nil {
		return nil, errors.New("video status poller is unavailable")
	}
	apiKey, err := p.apiKeys.GetByID(ctx, task.APIKeyID)
	if err != nil || apiKey == nil || apiKey.UserID != task.UserID {
		return nil, &VideoStudioPollError{Message: "video task API key is unavailable"}
	}
	if strings.TrimSpace(apiKey.Key) == "" {
		return nil, &VideoStudioPollError{Message: "video task API key has no credential"}
	}
	gatewayURL, err := videoStudioLocalGatewayURL(p.cfg, "/v1/videos/"+url.PathEscape(task.RequestID))
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, gatewayURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+apiKey.Key)
	request.Header.Set("Accept", "application/json")
	response, err := p.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, defaultVideoStudioStatusMaxBody+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > defaultVideoStudioStatusMaxBody {
		return nil, errors.New("video status response is too large")
	}
	retryAfter := parseVideoStudioRetryAfter(response.Header.Get("Retry-After"))
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, &VideoStudioPollError{
			StatusCode: response.StatusCode,
			RetryAfter: retryAfter,
			Permanent:  response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusUnprocessableEntity,
			Message:    videoStudioGatewayErrorMessage(body, response.StatusCode),
		}
	}
	var payload struct {
		Status   string   `json:"status"`
		Progress *float64 `json:"progress"`
		Error    any      `json:"error"`
		Video    struct {
			URL string `json:"url"`
		} `json:"video"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, errors.New("video gateway returned invalid status JSON")
	}
	status, ok := parseVideoStudioTaskStatus(payload.Status)
	if !ok {
		return nil, errors.New("video gateway returned unsupported task status")
	}
	if status == VideoStudioTaskStatusDone && strings.TrimSpace(payload.Video.URL) == "" {
		return nil, errors.New("video gateway reported completion without content")
	}
	return &VideoStudioPollResult{
		Status:       status,
		Progress:     normalizeVideoStudioProgress(payload.Progress),
		ErrorMessage: videoStudioStatusError(payload.Error),
		RetryAfter:   retryAfter,
	}, nil
}

func videoStudioLocalGatewayURL(cfg *config.Config, path string) (string, error) {
	if cfg == nil || cfg.Server.Port <= 0 || cfg.Server.Port > 65535 {
		return "", errors.New("invalid local gateway configuration")
	}
	host := strings.TrimSpace(cfg.Server.Host)
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	if parsed := net.ParseIP(strings.Trim(host, "[]")); parsed != nil {
		host = parsed.String()
	}
	return (&url.URL{Scheme: "http", Host: net.JoinHostPort(host, strconv.Itoa(cfg.Server.Port)), Path: "/" + strings.TrimLeft(path, "/")}).String(), nil
}

func normalizeVideoStudioProgress(value *float64) *int {
	if value == nil {
		return nil
	}
	progress := int(*value)
	if progress < 0 {
		progress = 0
	}
	if progress > 100 {
		progress = 100
	}
	return &progress
}

func videoStudioStatusError(value any) string {
	switch typed := value.(type) {
	case string:
		return boundedVideoStudioError(typed)
	case map[string]any:
		if message, ok := typed["message"].(string); ok {
			return boundedVideoStudioError(message)
		}
	}
	return ""
}

func videoStudioGatewayErrorMessage(body []byte, statusCode int) string {
	var payload struct {
		Error any    `json:"error"`
		Msg   string `json:"message"`
	}
	if json.Unmarshal(body, &payload) == nil {
		if message := videoStudioStatusError(payload.Error); message != "" {
			return message
		}
		if message := boundedVideoStudioError(payload.Msg); message != "" {
			return message
		}
	}
	return fmt.Sprintf("video status request failed with status %d", statusCode)
}

func parseVideoStudioRetryAfter(value string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}
