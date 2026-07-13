package service

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const openAIPreOutputCoordinatorKey = "openai_pre_output_coordinator"
const openAIPreOutputMaxPreambleBytes = 1 << 20
const openAIPreOutputDefaultWriteTimeout = 5 * time.Second

var (
	errOpenAIFirstOutputTimeout = errors.New("openai first output timeout")
	errOpenAIPreOutputBudget    = errors.New("openai total pre-output budget exhausted")
	errOpenAIClientDisconnected = errors.New("openai client disconnected during billing drain")
	errOpenAIPreambleTooLarge   = errors.New("openai pre-output preamble exceeded limit")
)

// OpenAIPreOutputSettings configures the ordinary HTTP /responses pre-output
// lifecycle. Durations <= 0 disable the corresponding behavior.
type OpenAIPreOutputSettings struct {
	FirstOutputTimeout     time.Duration
	TotalBudget            time.Duration
	HeartbeatInterval      time.Duration
	PreDisconnectDrain     time.Duration
	PostDisconnectDrain    time.Duration
	DownstreamWriteTimeout time.Duration
}

type openAIPreOutputCoordinator struct {
	mu       sync.Mutex
	writerMu sync.Mutex
	writer   gin.ResponseWriter
	settings OpenAIPreOutputSettings
	started  time.Time
	deadline time.Time

	transportStarted bool
	semanticStarted  bool
	clientConnected  bool
	stopped          bool

	attemptStarted  time.Time
	attemptDeadline time.Time
	attemptID       uint64
	nextAttemptID   uint64
	attemptTimeout  error
	timeoutCause    error
	attemptCancel   context.CancelCauseFunc
	attemptDone     chan struct{}
	semanticCommit  bool
	disconnect      chan struct{}
	disconnectOnce  sync.Once
	stop            chan struct{}
	stopOnce        sync.Once
	heartbeatDone   chan struct{}
}

// StartOpenAIPreOutput starts one coordinator shared by all account attempts.
// Heartbeats bypass attempt-local preamble buffers and therefore never make an
// account attempt semantically committed.
func StartOpenAIPreOutput(c *gin.Context, settings OpenAIPreOutputSettings) func() {
	if c == nil || c.Writer == nil || settings.FirstOutputTimeout <= 0 || settings.TotalBudget <= 0 {
		return func() {}
	}
	now := time.Now()
	p := &openAIPreOutputCoordinator{
		writer:          c.Writer,
		settings:        settings,
		started:         now,
		deadline:        now.Add(settings.TotalBudget),
		clientConnected: true,
		disconnect:      make(chan struct{}),
		stop:            make(chan struct{}),
	}
	if p.settings.DownstreamWriteTimeout <= 0 {
		p.settings.DownstreamWriteTimeout = openAIPreOutputDefaultWriteTimeout
	}
	c.Set(openAIPreOutputCoordinatorKey, p)

	if settings.HeartbeatInterval > 0 {
		p.heartbeatDone = make(chan struct{})
		go p.runHeartbeat(settings.HeartbeatInterval)
	}
	return p.Stop
}

func getOpenAIPreOutput(c *gin.Context) *openAIPreOutputCoordinator {
	if c == nil {
		return nil
	}
	v, ok := c.Get(openAIPreOutputCoordinatorKey)
	if !ok {
		return nil
	}
	p, _ := v.(*openAIPreOutputCoordinator)
	return p
}

func (p *openAIPreOutputCoordinator) runHeartbeat(interval time.Duration) {
	if p.heartbeatDone != nil {
		defer close(p.heartbeatDone)
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-timer.C:
		}
		if !p.beat() {
			return
		}
		timer.Reset(interval)
	}
}

func (p *openAIPreOutputCoordinator) beat() bool {
	p.writerMu.Lock()
	defer p.writerMu.Unlock()

	p.mu.Lock()
	if p.stopped || p.semanticStarted || !p.clientConnected {
		p.mu.Unlock()
		return false
	}
	if !p.transportStarted {
		header := p.writer.Header()
		header.Set("Content-Type", "text/event-stream")
		header.Set("Cache-Control", "no-cache")
		header.Set("Connection", "keep-alive")
		header.Set("X-Accel-Buffering", "no")
		p.writer.WriteHeader(http.StatusOK)
		p.transportStarted = true
	}
	p.mu.Unlock()

	err := withOpenAIDownstreamWriteDeadline(p.writer, p.settings.DownstreamWriteTimeout, func() error {
		if _, err := p.writer.Write([]byte(": keepalive\n\n")); err != nil {
			return err
		}
		p.writer.Flush()
		return nil
	})
	if err != nil {
		p.markClientDisconnected()
		return false
	}
	return true
}

func (p *openAIPreOutputCoordinator) Stop() {
	p.stopState(context.Canceled)
	if p.heartbeatDone != nil {
		<-p.heartbeatDone
	}
}

func (p *openAIPreOutputCoordinator) stopState(cause error) bool {
	p.mu.Lock()
	committed := p.transportStarted
	p.stopped = true
	if p.attemptCancel != nil {
		p.attemptCancel(cause)
	}
	p.mu.Unlock()
	p.stopOnce.Do(func() { close(p.stop) })
	return committed
}

func (p *openAIPreOutputCoordinator) beginAttempt(clientCtx context.Context) (context.Context, func(), error) {
	p.mu.Lock()
	if p.stopped || p.semanticStarted {
		p.mu.Unlock()
		return clientCtx, func() {}, nil
	}
	if !p.clientConnected {
		p.mu.Unlock()
		return nil, nil, errOpenAIClientDisconnected
	}
	now := time.Now()
	remaining := p.deadline.Sub(now)
	if remaining < 2*time.Second {
		p.mu.Unlock()
		return nil, nil, errOpenAIPreOutputBudget
	}
	allowance, timeoutCause := openAIPreOutputAttemptTimeout(p.settings.FirstOutputTimeout, remaining)
	p.nextAttemptID++
	attemptID := p.nextAttemptID
	base := context.Background()
	if clientCtx != nil {
		base = context.WithoutCancel(clientCtx)
	}
	base = context.WithValue(base, openAIPreOutputAttemptContextKey{}, openAIPreOutputAttemptRef{coordinator: p, id: attemptID})
	attemptCtx, cancel := context.WithCancelCause(base)
	done := make(chan struct{})
	p.attemptStarted = now
	p.attemptDeadline = now.Add(allowance)
	p.attemptID = attemptID
	p.attemptTimeout = timeoutCause
	p.timeoutCause = nil
	p.attemptCancel = cancel
	p.attemptDone = done
	p.mu.Unlock()

	go p.watchAttempt(clientCtx, cancel, done, allowance, timeoutCause, attemptID)
	var once sync.Once
	finish := func() {
		once.Do(func() {
			close(done)
			p.mu.Lock()
			if p.attemptDone == done {
				p.attemptDone = nil
				p.attemptCancel = nil
				p.attemptID = 0
				p.attemptDeadline = time.Time{}
				p.attemptTimeout = nil
				p.timeoutCause = nil
			}
			p.mu.Unlock()
			cancel(context.Canceled)
		})
	}
	return attemptCtx, finish, nil
}

func openAIPreOutputAttemptTimeout(firstOutputTimeout, remainingBudget time.Duration) (time.Duration, error) {
	if firstOutputTimeout <= 0 || firstOutputTimeout > remainingBudget {
		return remainingBudget, errOpenAIPreOutputBudget
	}
	return firstOutputTimeout, errOpenAIFirstOutputTimeout
}

func (p *openAIPreOutputCoordinator) watchAttempt(clientCtx context.Context, cancel context.CancelCauseFunc, done <-chan struct{}, allowance time.Duration, timeoutCause error, attemptID uint64) {
	timer := time.NewTimer(allowance)
	defer timer.Stop()
	timerCh := timer.C
	var clientDone <-chan struct{}
	if clientCtx != nil {
		clientDone = clientCtx.Done()
	}
	for {
		select {
		case <-done:
			return
		case <-timerCh:
			p.mu.Lock()
			if p.attemptID != attemptID {
				p.mu.Unlock()
				return
			}
			semantic := p.semanticStarted || p.semanticCommit
			if !semantic {
				p.timeoutCause = timeoutCause
			}
			p.mu.Unlock()
			if !semantic {
				cancel(timeoutCause)
				return
			}
			// Once semantic output begins the TTFT deadline is disabled, but the
			// watcher remains to enforce the post-output billing drain bound.
			timerCh = nil
		case <-clientDone:
			clientDone = nil
			p.markClientDisconnected()
		case <-p.disconnect:
			p.runDisconnectDrain(cancel, done)
			return
		}
	}
}

func (p *openAIPreOutputCoordinator) runDisconnectDrain(cancel context.CancelCauseFunc, done <-chan struct{}) {
	p.mu.Lock()
	semantic := p.semanticStarted
	drain := p.settings.PreDisconnectDrain
	if semantic {
		drain = p.settings.PostDisconnectDrain
	}
	p.mu.Unlock()
	if drain <= 0 {
		cancel(errOpenAIClientDisconnected)
		return
	}
	drainTimer := time.NewTimer(drain)
	defer drainTimer.Stop()
	select {
	case <-done:
	case <-drainTimer.C:
		cancel(errOpenAIClientDisconnected)
	}
}

type openAIPreOutputAttemptContextKey struct{}

type openAIPreOutputAttemptRef struct {
	coordinator *openAIPreOutputCoordinator
	id          uint64
}

func isOpenAIPreOutputAttemptContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	ref, _ := ctx.Value(openAIPreOutputAttemptContextKey{}).(openAIPreOutputAttemptRef)
	return ref.coordinator != nil && ref.id > 0
}

// openAIUpstreamContext keeps coordinator cancellation attached to ordinary
// /responses attempts while preserving the legacy detached context for paths
// outside the pre-output lifecycle.
func openAIUpstreamContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if isOpenAIPreOutputAttemptContext(ctx) {
		return ctx, func() {}
	}
	return detachUpstreamContext(ctx)
}

func (p *openAIPreOutputCoordinator) withWriterLock(fn func() error) error {
	p.writerMu.Lock()
	defer p.writerMu.Unlock()
	return withOpenAIDownstreamWriteDeadline(p.writer, p.settings.DownstreamWriteTimeout, fn)
}

func (p *openAIPreOutputCoordinator) commitSemantic(attemptCtx context.Context, fn func() error) (totalMs, attemptMs int, transitioned bool, err error) {
	p.writerMu.Lock()
	defer p.writerMu.Unlock()

	var ref openAIPreOutputAttemptRef
	if attemptCtx != nil {
		ref, _ = attemptCtx.Value(openAIPreOutputAttemptContextKey{}).(openAIPreOutputAttemptRef)
	}
	p.mu.Lock()
	if p.nextAttemptID > 0 && (p.attemptID == 0 || ref.coordinator != p || ref.id != p.attemptID) {
		p.mu.Unlock()
		return 0, 0, false, context.Canceled
	}
	if p.semanticStarted {
		totalMs, attemptMs = p.semanticLatencyLocked()
		p.mu.Unlock()
		return totalMs, attemptMs, false, withOpenAIDownstreamWriteDeadline(p.writer, p.settings.DownstreamWriteTimeout, fn)
	}
	if p.stopped {
		p.mu.Unlock()
		return 0, 0, false, context.Canceled
	}
	if !p.clientConnected {
		p.mu.Unlock()
		return 0, 0, false, errOpenAIClientDisconnected
	}
	timeoutCause := p.timeoutCause
	if timeoutCause == nil && !p.attemptDeadline.IsZero() && !time.Now().Before(p.attemptDeadline) {
		timeoutCause = p.attemptTimeout
		p.timeoutCause = timeoutCause
	}
	if timeoutCause != nil {
		cancel := p.attemptCancel
		p.mu.Unlock()
		if cancel != nil {
			cancel(timeoutCause)
		}
		return 0, 0, false, p.failureError(timeoutCause)
	}
	p.semanticCommit = true
	p.mu.Unlock()

	if err := withOpenAIDownstreamWriteDeadline(p.writer, p.settings.DownstreamWriteTimeout, fn); err != nil {
		p.mu.Lock()
		p.semanticCommit = false
		p.mu.Unlock()
		return 0, 0, false, err
	}

	p.mu.Lock()
	p.semanticCommit = false
	if !p.semanticStarted {
		p.semanticStarted = true
		p.transportStarted = true
		transitioned = true
	}
	totalMs, attemptMs = p.semanticLatencyLocked()
	p.mu.Unlock()
	return totalMs, attemptMs, transitioned, nil
}

func withOpenAIDownstreamWriteDeadline(writer http.ResponseWriter, timeout time.Duration, fn func() error) error {
	if timeout <= 0 {
		return fn()
	}
	controller := http.NewResponseController(writer)
	deadlineSet := controller.SetWriteDeadline(time.Now().Add(timeout)) == nil
	if deadlineSet {
		defer func() { _ = controller.SetWriteDeadline(time.Time{}) }()
	}
	return fn()
}

func (p *openAIPreOutputCoordinator) signalClientDisconnected() {
	p.disconnectOnce.Do(func() {
		close(p.disconnect)
	})
}

func (p *openAIPreOutputCoordinator) markClientDisconnected() {
	p.mu.Lock()
	if !p.clientConnected {
		p.mu.Unlock()
		p.signalClientDisconnected()
		return
	}
	p.clientConnected = false
	p.mu.Unlock()
	p.signalClientDisconnected()
}

func (p *openAIPreOutputCoordinator) markSemanticOutput() (totalMs, attemptMs int, transitioned bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.semanticStarted {
		totalMs, attemptMs = p.semanticLatencyLocked()
		return totalMs, attemptMs, false
	}
	p.semanticStarted = true
	p.transportStarted = true
	totalMs, attemptMs = p.semanticLatencyLocked()
	return totalMs, attemptMs, true
}

func (p *openAIPreOutputCoordinator) semanticLatencyLocked() (totalMs, attemptMs int) {
	totalMs = int(time.Since(p.started).Milliseconds())
	if !p.attemptStarted.IsZero() {
		attemptMs = int(time.Since(p.attemptStarted).Milliseconds())
	}
	return totalMs, attemptMs
}

func (p *openAIPreOutputCoordinator) failureError(err error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.semanticStarted {
		return err
	}
	attemptLatencyMs := int64(0)
	if !p.attemptStarted.IsZero() {
		attemptLatencyMs = time.Since(p.attemptStarted).Milliseconds()
	}
	switch {
	case errors.Is(err, errOpenAIFirstOutputTimeout):
		return &UpstreamFailoverError{
			StatusCode:         http.StatusGatewayTimeout,
			Reason:             "first_output_timeout",
			FirstOutputTimeout: true,
			AttemptLatencyMs:   attemptLatencyMs,
		}
	case errors.Is(err, errOpenAIPreOutputBudget):
		return &UpstreamFailoverError{
			StatusCode:               http.StatusGatewayTimeout,
			Reason:                   "pre_output_budget_exhausted",
			PreOutputBudgetExhausted: true,
			AttemptLatencyMs:         attemptLatencyMs,
		}
	default:
		return err
	}
}

// contextCause unwraps cancellation causes from a context-backed error path.
func contextCause(ctx context.Context, fallback error) error {
	if ctx != nil {
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
	}
	return fallback
}

// OpenAIPreOutputBeginAttempt derives an upstream context detached from the
// client but explicitly controlled by the coordinator.
func OpenAIPreOutputBeginAttempt(c *gin.Context, clientCtx context.Context) (context.Context, func(), error) {
	p := getOpenAIPreOutput(c)
	if p == nil {
		if clientCtx == nil {
			return context.Background(), func() {}, nil
		}
		return clientCtx, func() {}, nil
	}
	return p.beginAttempt(clientCtx)
}

func OpenAIPreOutputFailureError(c *gin.Context, attemptCtx context.Context, err error) error {
	p := getOpenAIPreOutput(c)
	if p == nil {
		return err
	}
	return p.failureError(contextCause(attemptCtx, err))
}

func OpenAIPreOutputWithWriterLock(c *gin.Context, fn func() error) error {
	if p := getOpenAIPreOutput(c); p != nil {
		return p.withWriterLock(fn)
	}
	return fn()
}

// OpenAIPreOutputCommitSemantic atomically arbitrates the current attempt's
// timeout against its first meaningful downstream write.
func OpenAIPreOutputCommitSemantic(c *gin.Context, attemptCtx context.Context, fn func() error) (totalMs, attemptMs int, transitioned bool, err error) {
	if p := getOpenAIPreOutput(c); p != nil {
		return p.commitSemantic(attemptCtx, fn)
	}
	return 0, 0, false, fn()
}

// OpenAIPreOutputSetHeaders serializes response-header mutation against the
// heartbeat commit and becomes a no-op after HTTP 200 has been written.
func OpenAIPreOutputSetHeaders(c *gin.Context, fn func()) {
	if p := getOpenAIPreOutput(c); p != nil {
		p.writerMu.Lock()
		defer p.writerMu.Unlock()
		p.mu.Lock()
		if p.transportStarted {
			p.mu.Unlock()
			return
		}
		p.mu.Unlock()
		fn()
		return
	}
	fn()
}

func OpenAIPreOutputMarkSemantic(c *gin.Context) (totalMs, attemptMs int, transitioned bool) {
	if p := getOpenAIPreOutput(c); p != nil {
		return p.markSemanticOutput()
	}
	return 0, 0, false
}

func OpenAIPreOutputMarkClientDisconnected(c *gin.Context) {
	if p := getOpenAIPreOutput(c); p != nil {
		p.markClientDisconnected()
	}
}

func OpenAIPreOutputEnabled(c *gin.Context) bool { return getOpenAIPreOutput(c) != nil }

func OpenAIPreOutputCanFailover(c *gin.Context) bool {
	p := getOpenAIPreOutput(c)
	if p == nil {
		return true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.semanticStarted && p.clientConnected && !p.stopped && time.Until(p.deadline) >= 2*time.Second
}

func OpenAIPreOutputClientConnected(c *gin.Context) bool {
	p := getOpenAIPreOutput(c)
	if p == nil {
		return c == nil || c.Request == nil || c.Request.Context().Err() == nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.clientConnected
}

func OpenAIPreOutputTransportStarted(c *gin.Context) bool {
	p := getOpenAIPreOutput(c)
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.transportStarted
}

func OpenAIPreOutputSemanticStarted(c *gin.Context) bool {
	p := getOpenAIPreOutput(c)
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.semanticStarted
}

func OpenAIPreOutputBudgetRemaining(c *gin.Context) time.Duration {
	p := getOpenAIPreOutput(c)
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.semanticStarted {
		return 0
	}
	remaining := time.Until(p.deadline)
	if remaining < 0 {
		return 0
	}
	return remaining
}

func OpenAIPreOutputLatencySnapshot(c *gin.Context) (attemptMs, totalMs int64) {
	p := getOpenAIPreOutput(c)
	if p == nil {
		return 0, 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	totalMs = time.Since(p.started).Milliseconds()
	if !p.attemptStarted.IsZero() {
		attemptMs = time.Since(p.attemptStarted).Milliseconds()
	}
	return attemptMs, totalMs
}

func OpenAIPreOutputSchedulingContext(c *gin.Context, parent context.Context) (context.Context, context.CancelFunc) {
	p := getOpenAIPreOutput(c)
	if p == nil {
		return parent, func() {}
	}
	if parent == nil {
		parent = context.Background()
	}
	p.mu.Lock()
	deadline := p.deadline
	semantic := p.semanticStarted
	p.mu.Unlock()
	if semantic {
		return parent, func() {}
	}
	ctx, cancel := context.WithDeadline(parent, deadline)
	stop := make(chan struct{})
	var stopOnce sync.Once
	go func() {
		select {
		case <-p.disconnect:
			cancel()
		case <-p.stop:
			cancel()
		case <-ctx.Done():
			if parent.Err() != nil {
				p.markClientDisconnected()
			}
		case <-stop:
		}
	}()
	return ctx, func() {
		stopOnce.Do(func() { close(stop) })
		cancel()
	}
}

func OpenAIPreOutputBudgetError(c *gin.Context) *UpstreamFailoverError {
	if p := getOpenAIPreOutput(c); p != nil {
		p.mu.Lock()
		elapsed := int64(0)
		if !p.attemptStarted.IsZero() {
			elapsed = time.Since(p.attemptStarted).Milliseconds()
		}
		p.mu.Unlock()
		return &UpstreamFailoverError{StatusCode: http.StatusGatewayTimeout, Reason: "pre_output_budget_exhausted", PreOutputBudgetExhausted: true, AttemptLatencyMs: elapsed}
	}
	return &UpstreamFailoverError{StatusCode: http.StatusGatewayTimeout, Reason: "pre_output_budget_exhausted", PreOutputBudgetExhausted: true}
}

// StopOpenAIPreOutputCommitted stops the heartbeat before a final error writer
// takes ownership and reports whether a heartbeat already committed HTTP 200.
func StopOpenAIPreOutputCommitted(c *gin.Context) bool {
	p := getOpenAIPreOutput(c)
	if p == nil {
		return false
	}
	p.stopState(context.Canceled)
	// Cancellation must not wait behind a slow client write. Once cancellation
	// is visible, wait for the bounded heartbeat write to leave the writer. The
	// terminal writer acquires the same lock separately so its deadline can be
	// cleared before this HTTP connection is returned to the keep-alive pool.
	p.writerMu.Lock()
	p.mu.Lock()
	committed := p.transportStarted
	p.mu.Unlock()
	if !committed && p.writer != nil {
		// A failed semantic callback may still have committed response bytes.
		committed = p.writer.Written()
	}
	p.writerMu.Unlock()
	return committed
}

// DisableOpenAIPreOutput preserves legacy behavior for branches outside the
// initial rollout scope (WebSocket, compatibility bridges, and passthrough).
func DisableOpenAIPreOutput(c *gin.Context) {
	if p := getOpenAIPreOutput(c); p != nil {
		p.Stop()
		c.Set(openAIPreOutputCoordinatorKey, (*openAIPreOutputCoordinator)(nil))
	}
}

func appendOpenAIPreOutputPreamble(buf []byte, line string) ([]byte, error) {
	nextLen := len(buf) + len(line) + 1
	if nextLen > openAIPreOutputMaxPreambleBytes {
		return buf, errOpenAIPreambleTooLarge
	}
	buf = append(buf, line...)
	buf = append(buf, '\n')
	return buf, nil
}
