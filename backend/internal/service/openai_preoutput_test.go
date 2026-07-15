package service

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
)

type blockingPreOutputWriter struct {
	*httptest.ResponseRecorder
	writeStarted chan struct{}
	releaseWrite chan struct{}
	once         sync.Once
}

func (w *blockingPreOutputWriter) Write(data []byte) (int, error) {
	w.once.Do(func() { close(w.writeStarted) })
	<-w.releaseWrite
	return w.ResponseRecorder.Write(data)
}

type concurrentPreOutputWriter struct {
	*httptest.ResponseRecorder
	firstWriteStarted chan struct{}
	releaseFirstWrite chan struct{}
	concurrentWrite   chan struct{}
	firstOnce         sync.Once
	concurrentOnce    sync.Once
	activeWrites      int32
}

type cancelAsEOFReadCloser struct {
	ctx context.Context
}

func (r cancelAsEOFReadCloser) Read(_ []byte) (int, error) {
	<-r.ctx.Done()
	return 0, io.EOF
}

func (cancelAsEOFReadCloser) Close() error { return nil }

func (w *concurrentPreOutputWriter) Write(data []byte) (int, error) {
	active := atomic.AddInt32(&w.activeWrites, 1)
	defer atomic.AddInt32(&w.activeWrites, -1)
	if active > 1 {
		w.concurrentOnce.Do(func() { close(w.concurrentWrite) })
		return len(data), nil
	}
	wait := false
	w.firstOnce.Do(func() {
		wait = true
		close(w.firstWriteStarted)
	})
	if wait {
		<-w.releaseFirstWrite
	}
	return w.ResponseRecorder.Write(data)
}

func newPreOutputTestContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/v1/responses", nil)
	c, _ := gin.CreateTestContext(recorder)
	c.Request = request
	return c, recorder
}

func TestOpenAIPreOutputHeartbeatDoesNotCommitSemanticOutput(t *testing.T) {
	c, recorder := newPreOutputTestContext(t)
	stop := StartOpenAIPreOutput(c, OpenAIPreOutputSettings{
		FirstOutputTimeout: 200 * time.Millisecond,
		TotalBudget:        5 * time.Second,
		HeartbeatInterval:  15 * time.Millisecond,
	})
	defer stop()

	buffered := bufio.NewWriterSize(c.Writer, 256)
	if _, err := buffered.WriteString("data: {\"type\":\"response.created\"}\n\n"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(35 * time.Millisecond)
	if !OpenAIPreOutputCanFailover(c) {
		t.Fatalf("heartbeat-only response should remain eligible for failover: transport=%v semantic=%v connected=%v budget=%s", OpenAIPreOutputTransportStarted(c), OpenAIPreOutputSemanticStarted(c), OpenAIPreOutputClientConnected(c), OpenAIPreOutputBudgetRemaining(c))
	}
	if recorder.Body.Len() == 0 {
		t.Fatal("expected heartbeat to commit transport")
	}
	if OpenAIPreOutputSemanticStarted(c) {
		t.Fatal("heartbeat must not mark semantic output")
	}

	totalMs, attemptMs, transitioned := OpenAIPreOutputMarkSemantic(c)
	if !transitioned || totalMs < 0 || attemptMs != 0 {
		t.Fatalf("unexpected semantic transition: total=%d attempt=%d transitioned=%v", totalMs, attemptMs, transitioned)
	}
	if err := OpenAIPreOutputWithWriterLock(c, buffered.Flush); err != nil {
		t.Fatal(err)
	}
	if OpenAIPreOutputCanFailover(c) {
		t.Fatal("semantic output must prohibit failover")
	}
}

func TestOpenAIPreOutputFirstOutputTimeoutIsTyped(t *testing.T) {
	c, _ := newPreOutputTestContext(t)
	stop := StartOpenAIPreOutput(c, OpenAIPreOutputSettings{
		FirstOutputTimeout: 25 * time.Millisecond,
		TotalBudget:        2 * time.Second,
	})
	defer stop()

	attemptCtx, finish, err := OpenAIPreOutputBeginAttempt(c, c.Request.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	<-attemptCtx.Done()
	timeoutErr := OpenAIPreOutputFailureError(c, attemptCtx, attemptCtx.Err())
	var failoverErr *UpstreamFailoverError
	if !errors.As(timeoutErr, &failoverErr) || !failoverErr.FirstOutputTimeout {
		t.Fatalf("expected typed first-output timeout, got %v", timeoutErr)
	}
	if failoverErr.Reason != "first_output_timeout" {
		t.Fatalf("unexpected timeout reason: %q", failoverErr.Reason)
	}
}

func TestOpenAIPreOutputSlowFailoverUsesFreshAccountDeadlineAfterRetryBudget(t *testing.T) {
	c, _ := newPreOutputTestContext(t)
	stop := StartOpenAIPreOutput(c, OpenAIPreOutputSettings{
		FirstOutputTimeout: 25 * time.Millisecond,
		TotalBudget:        3 * time.Second,
	})
	defer stop()

	firstCtx, finishFirst, err := OpenAIPreOutputBeginAttempt(c, c.Request.Context())
	if err != nil {
		t.Fatal(err)
	}
	<-firstCtx.Done()
	firstErr := OpenAIPreOutputFailureError(c, firstCtx, firstCtx.Err())
	finishFirst()
	if !IsOpenAIFirstOutputTimeout(firstErr) {
		t.Fatalf("expected first account timeout, got %v", firstErr)
	}

	p := getOpenAIPreOutput(c)
	p.mu.Lock()
	p.deadline = time.Now().Add(-time.Second)
	p.mu.Unlock()
	if OpenAIPreOutputCanFailover(c) {
		t.Fatal("expired retry budget must stop failover before slow mode starts")
	}

	expiredCtx, cancelExpired := OpenAIPreOutputSchedulingContext(c, c.Request.Context())
	if deadline, hasDeadline := expiredCtx.Deadline(); !hasDeadline || time.Until(deadline) > 0 {
		cancelExpired()
		t.Fatal("retry-phase scheduling must retain the expired budget")
	}
	cancelExpired()

	if _, _, err := OpenAIPreOutputBeginAttempt(c, c.Request.Context()); !errors.Is(err, errOpenAIPreOutputBudget) {
		t.Fatalf("expected total budget error, got %v", err)
	}

	OpenAIPreOutputEnterSlowFailover(c)
	if !OpenAIPreOutputCanFailover(c) {
		t.Fatal("normal account failover must remain available after the retry budget")
	}
	schedulingCtx, cancelScheduling := OpenAIPreOutputSchedulingContext(c, c.Request.Context())
	defer cancelScheduling()
	if _, hasDeadline := schedulingCtx.Deadline(); hasDeadline {
		t.Fatal("normal account failover scheduling must not inherit the expired retry budget")
	}

	secondCtx, finishSecond, err := OpenAIPreOutputBeginAttempt(c, c.Request.Context())
	if err != nil {
		t.Fatalf("start normal failover attempt: %v", err)
	}
	defer finishSecond()
	<-secondCtx.Done()
	secondErr := OpenAIPreOutputFailureError(c, secondCtx, secondCtx.Err())
	if !IsOpenAIFirstOutputTimeout(secondErr) {
		t.Fatalf("expected fresh first-output timeout, got %v", secondErr)
	}
}

func TestOpenAIStreamingCleanEOFAfterTimeoutRemainsTyped(t *testing.T) {
	c, _ := newPreOutputTestContext(t)
	stop := StartOpenAIPreOutput(c, OpenAIPreOutputSettings{
		FirstOutputTimeout: 20 * time.Millisecond,
		TotalBudget:        2 * time.Second,
	})
	defer stop()
	attemptCtx, finish, err := OpenAIPreOutputBeginAttempt(c, c.Request.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer finish()

	resp := &http.Response{StatusCode: http.StatusOK, Body: cancelAsEOFReadCloser{ctx: attemptCtx}, Header: http.Header{}}
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}}
	_, err = svc.handleStreamingResponse(attemptCtx, resp, c, &Account{ID: 1}, time.Now(), "model", "model")
	var failoverErr *UpstreamFailoverError
	if !errors.As(err, &failoverErr) || !failoverErr.FirstOutputTimeout {
		t.Fatalf("expected typed first-output timeout after clean EOF, got %v", err)
	}
}

func TestOpenAIPreOutputCommitAfterTimeoutWritesNothing(t *testing.T) {
	c, _ := newPreOutputTestContext(t)
	stop := StartOpenAIPreOutput(c, OpenAIPreOutputSettings{
		FirstOutputTimeout: 20 * time.Millisecond,
		TotalBudget:        2 * time.Second,
	})
	defer stop()
	attemptCtx, finish, err := OpenAIPreOutputBeginAttempt(c, c.Request.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	<-attemptCtx.Done()

	var writes int
	_, _, _, commitErr := OpenAIPreOutputCommitSemantic(c, attemptCtx, func() error {
		writes++
		return nil
	})
	var failoverErr *UpstreamFailoverError
	if writes != 0 || !errors.As(commitErr, &failoverErr) || !failoverErr.FirstOutputTimeout {
		t.Fatalf("writes=%d commitErr=%v, want zero writes and first-output timeout", writes, commitErr)
	}
}

func TestOpenAIPreOutputSemanticReservationBeatsTimerDuringWrite(t *testing.T) {
	c, _ := newPreOutputTestContext(t)
	stop := StartOpenAIPreOutput(c, OpenAIPreOutputSettings{
		FirstOutputTimeout: 35 * time.Millisecond,
		TotalBudget:        2 * time.Second,
	})
	defer stop()
	attemptCtx, finish, err := OpenAIPreOutputBeginAttempt(c, c.Request.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer finish()

	writeStarted := make(chan struct{})
	releaseWrite := make(chan struct{})
	commitDone := make(chan error, 1)
	go func() {
		_, _, _, commitErr := OpenAIPreOutputCommitSemantic(c, attemptCtx, func() error {
			close(writeStarted)
			<-releaseWrite
			return nil
		})
		commitDone <- commitErr
	}()
	select {
	case <-writeStarted:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("semantic write did not reserve commitment")
	}
	time.Sleep(50 * time.Millisecond)
	select {
	case <-attemptCtx.Done():
		t.Fatalf("timeout canceled an attempt during reserved semantic write: %v", context.Cause(attemptCtx))
	default:
	}
	close(releaseWrite)
	if commitErr := <-commitDone; commitErr != nil {
		t.Fatal(commitErr)
	}
	if !OpenAIPreOutputSemanticStarted(c) {
		t.Fatal("semantic reservation did not complete commitment")
	}
}

func TestStopOpenAIPreOutputCommittedObservesInFlightSemanticWrite(t *testing.T) {
	c, _ := newPreOutputTestContext(t)
	stop := StartOpenAIPreOutput(c, OpenAIPreOutputSettings{
		FirstOutputTimeout: time.Second,
		TotalBudget:        2 * time.Second,
	})
	defer stop()
	attemptCtx, finish, err := OpenAIPreOutputBeginAttempt(c, c.Request.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer finish()

	writeStarted := make(chan struct{})
	releaseWrite := make(chan struct{})
	commitDone := make(chan error, 1)
	go func() {
		_, _, _, commitErr := OpenAIPreOutputCommitSemantic(c, attemptCtx, func() error {
			close(writeStarted)
			<-releaseWrite
			_, err := c.Writer.Write([]byte("data: semantic\n\n"))
			return err
		})
		commitDone <- commitErr
	}()
	<-writeStarted

	stopResult := make(chan bool, 1)
	go func() { stopResult <- StopOpenAIPreOutputCommitted(c) }()
	select {
	case <-stopResult:
		t.Fatal("terminal ownership did not wait for the in-flight semantic write")
	case <-time.After(10 * time.Millisecond):
	}
	close(releaseWrite)
	if err := <-commitDone; err != nil {
		t.Fatal(err)
	}
	if committed := <-stopResult; !committed {
		t.Fatal("terminal ownership missed the committed semantic response")
	}
}

func TestOpenAIPreOutputStaleAttemptCannotWriteAfterNextAttemptStarts(t *testing.T) {
	c, _ := newPreOutputTestContext(t)
	stop := StartOpenAIPreOutput(c, OpenAIPreOutputSettings{
		FirstOutputTimeout: time.Second,
		TotalBudget:        5 * time.Second,
	})
	defer stop()
	firstCtx, finishFirst, err := OpenAIPreOutputBeginAttempt(c, c.Request.Context())
	if err != nil {
		t.Fatal(err)
	}
	finishFirst()
	secondCtx, finishSecond, err := OpenAIPreOutputBeginAttempt(c, c.Request.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer finishSecond()

	var staleWrites int
	_, _, _, err = OpenAIPreOutputCommitSemantic(c, firstCtx, func() error {
		staleWrites++
		return nil
	})
	if !errors.Is(err, context.Canceled) || staleWrites != 0 {
		t.Fatalf("stale commit err=%v writes=%d, want context canceled and zero writes", err, staleWrites)
	}
	_, _, transitioned, err := OpenAIPreOutputCommitSemantic(c, secondCtx, func() error { return nil })
	if err != nil || !transitioned {
		t.Fatalf("current attempt commit err=%v transitioned=%v", err, transitioned)
	}
}

func TestOpenAIPreOutputInitialAttemptHonorsRemainingRequestBudget(t *testing.T) {
	c, _ := newPreOutputTestContext(t)
	stop := StartOpenAIPreOutput(c, OpenAIPreOutputSettings{
		FirstOutputTimeout: 3 * time.Second,
		TotalBudget:        5 * time.Second,
	})
	defer stop()

	p := getOpenAIPreOutput(c)
	p.mu.Lock()
	p.deadline = time.Now().Add(2100 * time.Millisecond)
	p.mu.Unlock()

	startedAt := time.Now()
	_, finish, err := OpenAIPreOutputBeginAttempt(c, c.Request.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	p.mu.Lock()
	attemptDeadline := p.attemptDeadline
	attemptTimeout := p.attemptTimeout
	p.mu.Unlock()
	if remaining := attemptDeadline.Sub(startedAt); remaining < 1900*time.Millisecond || remaining > 2200*time.Millisecond {
		t.Fatalf("attempt deadline = %s, want remaining request budget near 2.1s", remaining)
	}
	if !errors.Is(attemptTimeout, errOpenAIPreOutputBudget) {
		t.Fatalf("attempt timeout cause = %v, want pre-output budget exhaustion", attemptTimeout)
	}
}

func TestOpenAIUpstreamContextPreservesAttemptCancellation(t *testing.T) {
	c, _ := newPreOutputTestContext(t)
	stop := StartOpenAIPreOutput(c, OpenAIPreOutputSettings{
		FirstOutputTimeout: time.Second,
		TotalBudget:        2 * time.Second,
	})
	defer stop()

	attemptCtx, finish, err := OpenAIPreOutputBeginAttempt(c, c.Request.Context())
	if err != nil {
		t.Fatal(err)
	}
	upstreamCtx, release := openAIUpstreamContext(attemptCtx)
	defer release()
	finish()
	select {
	case <-upstreamCtx.Done():
	case <-time.After(100 * time.Millisecond):
		t.Fatal("upstream context detached from coordinator attempt")
	}
}

func TestOpenAIPreOutputSchedulingContextCancelsOnDisconnect(t *testing.T) {
	c, _ := newPreOutputTestContext(t)
	stop := StartOpenAIPreOutput(c, OpenAIPreOutputSettings{
		FirstOutputTimeout: time.Second,
		TotalBudget:        2 * time.Second,
	})
	defer stop()

	schedulingCtx, cancel := OpenAIPreOutputSchedulingContext(c, c.Request.Context())
	defer cancel()
	OpenAIPreOutputMarkClientDisconnected(c)
	select {
	case <-schedulingCtx.Done():
	case <-time.After(100 * time.Millisecond):
		t.Fatal("scheduling context did not cancel after coordinator disconnect")
	}
}

func TestOpenAIPreOutputSchedulingContextMarksParentDisconnect(t *testing.T) {
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	c, _ := newPreOutputTestContext(t)
	c.Request = c.Request.WithContext(requestCtx)
	stop := StartOpenAIPreOutput(c, OpenAIPreOutputSettings{
		FirstOutputTimeout: time.Second,
		TotalBudget:        2 * time.Second,
	})
	defer stop()

	schedulingCtx, cancelScheduling := OpenAIPreOutputSchedulingContext(c, requestCtx)
	defer cancelScheduling()
	cancelRequest()
	select {
	case <-schedulingCtx.Done():
	case <-time.After(100 * time.Millisecond):
		t.Fatal("scheduling context did not observe parent disconnect")
	}
	deadline := time.Now().Add(100 * time.Millisecond)
	for OpenAIPreOutputClientConnected(c) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if OpenAIPreOutputClientConnected(c) {
		t.Fatal("parent cancellation did not mark the coordinator disconnected")
	}
}

func TestOpenAIPreOutputSchedulingCancelIsIdempotent(t *testing.T) {
	c, _ := newPreOutputTestContext(t)
	stop := StartOpenAIPreOutput(c, OpenAIPreOutputSettings{
		FirstOutputTimeout: time.Second,
		TotalBudget:        2 * time.Second,
	})
	defer stop()

	_, cancel := OpenAIPreOutputSchedulingContext(c, c.Request.Context())
	cancel()
	cancel()
}

func TestOpenAIPreOutputHeartbeatStopsAfterSemanticOutput(t *testing.T) {
	c, recorder := newPreOutputTestContext(t)
	stop := StartOpenAIPreOutput(c, OpenAIPreOutputSettings{
		FirstOutputTimeout: time.Second,
		TotalBudget:        2 * time.Second,
		HeartbeatInterval:  10 * time.Millisecond,
	})
	defer stop()
	bodyLen := func() int {
		var n int
		_ = OpenAIPreOutputWithWriterLock(c, func() error {
			n = recorder.Body.Len()
			return nil
		})
		return n
	}

	deadline := time.Now().Add(200 * time.Millisecond)
	for bodyLen() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if bodyLen() == 0 {
		t.Fatal("expected initial heartbeat")
	}
	before := bodyLen()
	OpenAIPreOutputMarkSemantic(c)
	time.Sleep(30 * time.Millisecond)
	if after := bodyLen(); after != before {
		t.Fatalf("pre-output heartbeat wrote after semantic handoff: before=%d after=%d", before, after)
	}
}

func TestOpenAIPreOutputBeginAttemptRejectsDisconnectedClient(t *testing.T) {
	c, _ := newPreOutputTestContext(t)
	stop := StartOpenAIPreOutput(c, OpenAIPreOutputSettings{
		FirstOutputTimeout: time.Second,
		TotalBudget:        2 * time.Second,
	})
	defer stop()
	OpenAIPreOutputMarkClientDisconnected(c)
	if _, _, err := OpenAIPreOutputBeginAttempt(c, c.Request.Context()); !errors.Is(err, errOpenAIClientDisconnected) {
		t.Fatalf("expected disconnected attempt error, got %v", err)
	}
}

func TestOpenAIPreOutputDisconnectDrainIsBounded(t *testing.T) {
	requestCtx, cancel := context.WithCancel(context.Background())
	c, _ := newPreOutputTestContext(t)
	c.Request = c.Request.WithContext(requestCtx)
	stop := StartOpenAIPreOutput(c, OpenAIPreOutputSettings{
		FirstOutputTimeout: 2 * time.Second,
		TotalBudget:        3 * time.Second,
		PreDisconnectDrain: 25 * time.Millisecond,
	})
	defer stop()

	attemptCtx, finish, err := OpenAIPreOutputBeginAttempt(c, requestCtx)
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	cancel()
	select {
	case <-attemptCtx.Done():
		t.Fatal("disconnect should enter a bounded drain first")
	case <-time.After(8 * time.Millisecond):
	}
	select {
	case <-attemptCtx.Done():
	case <-time.After(150 * time.Millisecond):
		t.Fatal("disconnect drain did not cancel the upstream attempt")
	}
	if OpenAIPreOutputClientConnected(c) {
		t.Fatal("client should be marked disconnected")
	}
}

func TestOpenAIPreOutputExplicitDisconnectDrainIsBounded(t *testing.T) {
	c, _ := newPreOutputTestContext(t)
	stop := StartOpenAIPreOutput(c, OpenAIPreOutputSettings{
		FirstOutputTimeout:  2 * time.Second,
		TotalBudget:         3 * time.Second,
		PostDisconnectDrain: 25 * time.Millisecond,
	})
	defer stop()

	attemptCtx, finish, err := OpenAIPreOutputBeginAttempt(c, c.Request.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	OpenAIPreOutputMarkSemantic(c)
	OpenAIPreOutputMarkClientDisconnected(c)
	select {
	case <-attemptCtx.Done():
		t.Fatal("explicit disconnect should enter billing drain before canceling the attempt")
	case <-time.After(8 * time.Millisecond):
	}
	select {
	case <-attemptCtx.Done():
	case <-time.After(150 * time.Millisecond):
		t.Fatal("explicit disconnect did not cancel the upstream attempt")
	}
}

func TestOpenAIPreOutputLargePreambleStaysBufferedUntilMeaningfulOutput(t *testing.T) {
	c, recorder := newPreOutputTestContext(t)
	stop := StartOpenAIPreOutput(c, OpenAIPreOutputSettings{
		FirstOutputTimeout: time.Second,
		TotalBudget:        5 * time.Second,
		HeartbeatInterval:  10 * time.Millisecond,
	})
	defer stop()
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}}
	reader, writer := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Body: reader, Header: http.Header{}}
	result := make(chan error, 1)
	go func() {
		_, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, &Account{ID: 1}, time.Now(), "model", "model")
		result <- err
	}()

	largePreamble := `data: {"type":"response.created","padding":"` + strings.Repeat("x", 16*1024) + `"}` + "\n\n"
	if _, err := writer.Write([]byte(largePreamble)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	var beforeMeaningful string
	if err := OpenAIPreOutputWithWriterLock(c, func() error {
		beforeMeaningful = recorder.Body.String()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(beforeMeaningful, "response.created") {
		t.Fatal("large preamble leaked before meaningful output")
	}
	if !strings.Contains(beforeMeaningful, ": keepalive") {
		t.Fatal("expected transport heartbeat while preamble was buffered")
	}

	_, _ = writer.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n"))
	_, _ = writer.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"))
	_ = writer.Close()
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "response.created") || !strings.Contains(body, "response.output_text.delta") {
		t.Fatalf("committed stream missing buffered preamble or meaningful output: %q", body)
	}
}

func TestOpenAIPreOutputLargeFirstEventCannotRaceHeartbeatWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	writer := &concurrentPreOutputWriter{
		ResponseRecorder:  httptest.NewRecorder(),
		firstWriteStarted: make(chan struct{}),
		releaseFirstWrite: make(chan struct{}),
		concurrentWrite:   make(chan struct{}),
	}
	c, _ := gin.CreateTestContext(writer)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	stop := StartOpenAIPreOutput(c, OpenAIPreOutputSettings{
		FirstOutputTimeout:     time.Second,
		TotalBudget:            5 * time.Second,
		HeartbeatInterval:      15 * time.Millisecond,
		DownstreamWriteTimeout: time.Second,
	})
	defer stop()

	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}}
	reader, upstream := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Body: reader, Header: http.Header{}}
	result := make(chan error, 1)
	go func() {
		_, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, &Account{ID: 1}, time.Now(), "model", "model")
		result <- err
	}()

	select {
	case <-writer.firstWriteStarted:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("heartbeat write did not start")
	}
	upstreamWriteDone := make(chan struct{})
	go func() {
		_, _ = upstream.Write([]byte(`data: {"type":"response.created","padding":"` + strings.Repeat("x", 16*1024) + `"}` + "\n\n"))
		_, _ = upstream.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n"))
		close(upstreamWriteDone)
	}()

	select {
	case <-writer.concurrentWrite:
		t.Fatal("semantic output wrote concurrently with the blocked heartbeat")
	case <-time.After(40 * time.Millisecond):
	}
	close(writer.releaseFirstWrite)
	select {
	case <-upstreamWriteDone:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("upstream writer did not resume after heartbeat release")
	}
	_, _ = upstream.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"))
	_ = upstream.Close()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("stream did not finish")
	}
}

func TestOpenAIPreOutputPreambleLimitTriggersFailover(t *testing.T) {
	c, recorder := newPreOutputTestContext(t)
	stop := StartOpenAIPreOutput(c, OpenAIPreOutputSettings{
		FirstOutputTimeout: time.Second,
		TotalBudget:        5 * time.Second,
		HeartbeatInterval:  10 * time.Millisecond,
	})
	defer stop()
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}}
	reader, writer := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Body: reader, Header: http.Header{}}
	result := make(chan error, 1)
	go func() {
		_, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, &Account{ID: 1, Platform: PlatformOpenAI, Name: "acc"}, time.Now(), "model", "model")
		result <- err
	}()

	oversizedPreamble := `data: {"type":"response.created","padding":"` + strings.Repeat("x", openAIPreOutputMaxPreambleBytes) + `"}` + "\n\n"
	if _, err := writer.Write([]byte(oversizedPreamble)); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	err := <-result
	var failoverErr *UpstreamFailoverError
	if !errors.As(err, &failoverErr) {
		t.Fatalf("expected failover error, got %v", err)
	}
	if !strings.Contains(err.Error(), "failover") {
		t.Fatalf("expected failover semantics, got %v", err)
	}
	if strings.Contains(recorder.Body.String(), "response.created") {
		t.Fatal("oversized preamble should not be committed to the client")
	}
}

func TestOpenAIPreOutputBeginAttemptWithoutCoordinatorPreservesParentContext(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx, finish, err := OpenAIPreOutputBeginAttempt(nil, parent)
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	cancel()
	select {
	case <-ctx.Done():
	case <-time.After(50 * time.Millisecond):
		t.Fatal("expected parent cancellation to propagate when coordinator is disabled")
	}
}

func TestOpenAIPreOutputBudgetErrorDoesNotAttributeTimeoutToAccount(t *testing.T) {
	c, _ := newPreOutputTestContext(t)
	stop := StartOpenAIPreOutput(c, OpenAIPreOutputSettings{
		FirstOutputTimeout: time.Second,
		TotalBudget:        2 * time.Second,
	})
	defer stop()
	err := OpenAIPreOutputBudgetError(c)
	if !err.PreOutputBudgetExhausted || err.FirstOutputTimeout {
		t.Fatalf("unexpected budget classification: %+v", err)
	}
}

func TestOpenAIPreOutputStopCancelsAttemptBeforeBlockedHeartbeatReturns(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := httptest.NewRecorder()
	blocking := &blockingPreOutputWriter{
		ResponseRecorder: base,
		writeStarted:     make(chan struct{}),
		releaseWrite:     make(chan struct{}),
	}
	c, _ := gin.CreateTestContext(blocking)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	stop := StartOpenAIPreOutput(c, OpenAIPreOutputSettings{
		FirstOutputTimeout:     time.Second,
		TotalBudget:            2 * time.Second,
		HeartbeatInterval:      time.Millisecond,
		DownstreamWriteTimeout: time.Second,
	})
	attemptCtx, finish, err := OpenAIPreOutputBeginAttempt(c, c.Request.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	select {
	case <-blocking.writeStarted:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("heartbeat write did not start")
	}
	stopDone := make(chan struct{})
	go func() {
		stop()
		close(stopDone)
	}()
	select {
	case <-attemptCtx.Done():
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Stop waited for the blocked heartbeat before canceling upstream")
	}
	select {
	case <-stopDone:
		t.Fatal("Stop should still wait for writer ownership")
	default:
	}
	close(blocking.releaseWrite)
	select {
	case <-stopDone:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Stop did not finish after heartbeat write returned")
	}
}

func TestWriteOpenAITransportAwareJSONErrorAfterPreOutputHeartbeatUsesSSE(t *testing.T) {
	c, recorder := newPreOutputTestContext(t)
	stop := StartOpenAIPreOutput(c, OpenAIPreOutputSettings{
		FirstOutputTimeout: time.Second,
		TotalBudget:        2 * time.Second,
		HeartbeatInterval:  time.Millisecond,
	})
	defer stop()
	deadline := time.Now().Add(100 * time.Millisecond)
	for !OpenAIPreOutputTransportStarted(c) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	writeOpenAICompactAwareJSONError(c, http.StatusForbidden, "forbidden_error", "local reject", nil)
	body := recorder.Body.String()
	if !IsResponseCommitted(c) {
		t.Fatal("terminal SSE error was not marked as committed")
	}
	if recorder.Code != http.StatusOK || !strings.Contains(body, "event: response.failed") {
		t.Fatalf("expected committed SSE failure, code=%d body=%q", recorder.Code, body)
	}
	if count := strings.Count(body, `"type":"response.failed"`); count != 1 {
		t.Fatalf("response.failed count=%d, want 1: %q", count, body)
	}
	if strings.Contains(body, `{"error":{"message"`) {
		t.Fatalf("JSON error was appended to SSE stream: %q", body)
	}
}

func TestWriteOpenAITransportAwareJSONErrorBeforeHeartbeatMarksJSONCommitted(t *testing.T) {
	c, recorder := newPreOutputTestContext(t)
	stop := StartOpenAIPreOutput(c, OpenAIPreOutputSettings{
		FirstOutputTimeout: time.Second,
		TotalBudget:        2 * time.Second,
		HeartbeatInterval:  time.Hour,
	})
	defer stop()

	writeOpenAICompactAwareJSONError(c, http.StatusForbidden, "forbidden_error", "local reject", nil)
	if !IsResponseCommitted(c) {
		t.Fatal("terminal JSON error was not marked as committed")
	}
	body := recorder.Body.String()
	if recorder.Code != http.StatusForbidden || !strings.Contains(body, `"type":"forbidden_error"`) {
		t.Fatalf("expected JSON failure, code=%d body=%q", recorder.Code, body)
	}
	if strings.Contains(body, "response.failed") {
		t.Fatalf("SSE failure was appended to JSON response: %q", body)
	}
}

func TestWriteOpenAIResponsesFallbackErrorAfterPreOutputHeartbeatUsesSSE(t *testing.T) {
	c, recorder := newPreOutputTestContext(t)
	stop := StartOpenAIPreOutput(c, OpenAIPreOutputSettings{
		FirstOutputTimeout: time.Second,
		TotalBudget:        2 * time.Second,
		HeartbeatInterval:  time.Millisecond,
	})
	defer stop()
	deadline := time.Now().Add(100 * time.Millisecond)
	for !OpenAIPreOutputTransportStarted(c) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	writeOpenAIResponsesFallbackError(c, http.StatusBadRequest, "invalid_request_error", "bad input")
	if !IsResponseCommitted(c) {
		t.Fatal("fallback terminal error was not marked as committed")
	}
	if body := recorder.Body.String(); recorder.Code != http.StatusOK || !strings.Contains(body, "event: response.failed") {
		t.Fatalf("expected committed SSE failure, code=%d body=%q", recorder.Code, body)
	}
}
