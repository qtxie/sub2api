package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

func testOpenAIFailbackConfig() openAIFailbackConfig {
	return openAIFailbackConfig{
		enabled:            true,
		defaultCooldown:    2 * time.Minute,
		cooldownIncrement:  3 * time.Minute,
		maxCooldown:        26 * time.Minute,
		probation:          5 * time.Minute,
		probeTimeout:       20 * time.Second,
		maxTTFT:            20 * time.Second,
		minHealthyRequests: 3,
	}
}

func TestOpenAIFailbackControllerAdaptiveCooldownAndHealthyReset(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	controller := newOpenAIFailbackController(nil, testOpenAIFailbackConfig())
	controller.now = func() time.Time { return now }

	controller.recordProductionResult(ctx, 10, "gpt-5-mini", false, nil)
	state := requireOpenAIFailbackState(t, controller, 10, "gpt-5-mini")
	require.Equal(t, 0, state.CooldownLevel)
	require.Equal(t, int64(120), state.CooldownSeconds)
	require.Equal(t, openAIFailbackBlock, controller.selectionAction(ctx, 10, "gpt-5-mini"))

	now = now.Add(2 * time.Minute)
	require.Equal(t, openAIFailbackProbe, controller.selectionAction(ctx, 10, "gpt-5-mini"))
	require.True(t, controller.recordProbeSuccess(ctx, 10, "gpt-5-mini", 250))
	require.Equal(t, openAIFailbackAllow, controller.selectionAction(ctx, 10, "gpt-5-mini"))

	now = now.Add(time.Minute)
	controller.recordProductionResult(ctx, 10, "gpt-5-mini", false, nil)
	state = requireOpenAIFailbackState(t, controller, 10, "gpt-5-mini")
	require.Equal(t, 1, state.CooldownLevel)
	require.Equal(t, int64(300), state.CooldownSeconds)

	now = now.Add(5 * time.Minute)
	controller.recordProbeFailure(ctx, 10, "gpt-5-mini", "probe_error")
	state = requireOpenAIFailbackState(t, controller, 10, "gpt-5-mini")
	require.Equal(t, 2, state.CooldownLevel)
	require.Equal(t, int64(480), state.CooldownSeconds)

	now = now.Add(8 * time.Minute)
	require.True(t, controller.recordProbeSuccess(ctx, 10, "gpt-5-mini", 300))
	fastTTFT := 300
	controller.recordProductionResult(ctx, 10, "gpt-5-mini", true, &fastTTFT)
	controller.recordProductionResult(ctx, 10, "gpt-5-mini", true, &fastTTFT)
	now = now.Add(5 * time.Minute)
	controller.recordProductionResult(ctx, 10, "gpt-5-mini", true, &fastTTFT)

	key, ok := openAIFailbackStateKey(10, "gpt-5-mini")
	require.True(t, ok)
	_, found := controller.readState(ctx, key)
	require.False(t, found)

	controller.recordProductionResult(ctx, 10, "gpt-5-mini", false, nil)
	state = requireOpenAIFailbackState(t, controller, 10, "gpt-5-mini")
	require.Equal(t, 0, state.CooldownLevel)
	require.Equal(t, int64(120), state.CooldownSeconds)
}

func TestOpenAIFailbackControllerSlowProbationResultRelapses(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	controller := newOpenAIFailbackController(nil, testOpenAIFailbackConfig())
	controller.now = func() time.Time { return now }

	controller.recordProductionResult(ctx, 20, "gpt-5-mini", false, nil)
	now = now.Add(2 * time.Minute)
	require.True(t, controller.recordProbeSuccess(ctx, 20, "gpt-5-mini", 200))

	slowTTFT := 20_001
	controller.recordProductionResult(ctx, 20, "gpt-5-mini", true, &slowTTFT)
	state := requireOpenAIFailbackState(t, controller, 20, "gpt-5-mini")
	require.Equal(t, 1, state.CooldownLevel)
	require.Equal(t, "production_slow", state.LastFailure)
	require.Equal(t, int64(300), state.CooldownSeconds)
}

func TestOpenAIFailbackControllerNonStreamingSuccessesCompleteProbation(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	controller := newOpenAIFailbackController(nil, testOpenAIFailbackConfig())
	controller.now = func() time.Time { return now }

	controller.recordProductionResult(ctx, 21, "gpt-5-mini", false, nil)
	now = now.Add(2 * time.Minute)
	require.True(t, controller.recordProbeSuccess(ctx, 21, "gpt-5-mini", 200))

	controller.recordProductionResult(ctx, 21, "gpt-5-mini", true, nil)
	controller.recordProductionResult(ctx, 21, "gpt-5-mini", true, nil)
	state := requireOpenAIFailbackState(t, controller, 21, "gpt-5-mini")
	require.Equal(t, 2, state.HealthyRequests)

	now = now.Add(5 * time.Minute)
	controller.recordProductionResult(ctx, 21, "gpt-5-mini", true, nil)
	key, ok := openAIFailbackStateKey(21, "gpt-5-mini")
	require.True(t, ok)
	_, found := controller.readState(ctx, key)
	require.False(t, found)
}

func TestOpenAIFailbackControllerReconcilesNewerDirtyStateAfterStoreRecovery(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	store := &openAIFailbackStoreStub{}
	controller := newOpenAIFailbackController(store, testOpenAIFailbackConfig())
	controller.now = func() time.Time { return now }

	controller.recordProductionResult(ctx, 22, "gpt-5-mini", false, nil)
	now = now.Add(2 * time.Minute)
	require.True(t, controller.recordProbeSuccess(ctx, 22, "gpt-5-mini", 200))
	remoteState, valid := decodeOpenAIFailbackState(store.value)
	require.True(t, valid)
	require.Equal(t, openAIFailbackPhaseProbation, remoteState.Phase)

	store.unavailable = true
	now = now.Add(time.Minute)
	controller.recordProductionResult(ctx, 22, "gpt-5-mini", false, nil)
	key, ok := openAIFailbackStateKey(22, "gpt-5-mini")
	require.True(t, ok)
	_, localState, dirty := controller.readDirtyLocalState(key)
	require.True(t, dirty)
	require.Equal(t, openAIFailbackPhaseCooldown, localState.Phase)
	require.Equal(t, 1, localState.CooldownLevel)

	store.unavailable = false
	require.Equal(t, openAIFailbackBlock, controller.selectionAction(ctx, 22, "gpt-5-mini"))
	remoteState, valid = decodeOpenAIFailbackState(store.value)
	require.True(t, valid)
	require.Equal(t, openAIFailbackPhaseCooldown, remoteState.Phase)
	require.Equal(t, 1, remoteState.CooldownLevel)
	_, _, dirty = controller.readDirtyLocalState(key)
	require.False(t, dirty)
}

func TestOpenAIFailbackControllerProbeLeaseIsSingleFlight(t *testing.T) {
	ctx := context.Background()
	controller := newOpenAIFailbackController(nil, testOpenAIFailbackConfig())

	owner, acquired := controller.acquireProbe(ctx, 30, "gpt-5-mini")
	require.True(t, acquired)
	require.NotEmpty(t, owner)
	_, acquired = controller.acquireProbe(ctx, 30, "gpt-5-mini")
	require.False(t, acquired)

	controller.releaseProbe(ctx, 30, "gpt-5-mini", owner)
	_, acquired = controller.acquireProbe(ctx, 30, "gpt-5-mini")
	require.True(t, acquired)
}

func requireOpenAIFailbackState(t *testing.T, controller *openAIFailbackController, accountID int64, model string) openAIFailbackState {
	t.Helper()
	key, ok := openAIFailbackStateKey(accountID, model)
	require.True(t, ok)
	state, found := controller.readState(context.Background(), key)
	require.True(t, found)
	return state
}

type openAIFailbackProbeUpstream struct {
	response    *http.Response
	err         error
	request     *http.Request
	proxyURL    string
	accountID   int64
	concurrency int
}

type blockingOpenAIFailbackProbeUpstream struct {
	started     chan struct{}
	unblock     chan struct{}
	startOnce   sync.Once
	unblockOnce sync.Once
	calls       atomic.Int64
}

func newBlockingOpenAIFailbackProbeUpstream() *blockingOpenAIFailbackProbeUpstream {
	return &blockingOpenAIFailbackProbeUpstream{
		started: make(chan struct{}),
		unblock: make(chan struct{}),
	}
}

func (u *blockingOpenAIFailbackProbeUpstream) Do(
	req *http.Request,
	_ string,
	_ int64,
	_ int,
) (*http.Response, error) {
	u.calls.Add(1)
	u.startOnce.Do(func() { close(u.started) })
	select {
	case <-u.unblock:
		return failbackProbeResponse(
			http.StatusOK,
			"data: {\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\n",
		), nil
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
}

func (u *blockingOpenAIFailbackProbeUpstream) DoWithTLS(
	req *http.Request,
	proxyURL string,
	accountID int64,
	concurrency int,
	_ *tlsfingerprint.Profile,
) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

func (u *blockingOpenAIFailbackProbeUpstream) release() {
	u.unblockOnce.Do(func() { close(u.unblock) })
}

type rejectingOpenAIFailbackProbeExecutor struct{}

func (rejectingOpenAIFailbackProbeExecutor) Submit(func()) bool { return false }
func (rejectingOpenAIFailbackProbeExecutor) Stop()              {}

type openAIFailbackStoreStub struct {
	value       string
	found       bool
	unavailable bool
}

func (s *openAIFailbackStoreStub) GetOpenAIFailbackState(context.Context, string) (string, bool, error) {
	if s.unavailable {
		return "", false, errors.New("failback store unavailable")
	}
	return s.value, s.found, nil
}

func (s *openAIFailbackStoreStub) CompareAndSwapOpenAIFailbackState(
	_ context.Context,
	_ string,
	expected string,
	next string,
	_ time.Duration,
) (bool, error) {
	if s.unavailable {
		return false, errors.New("failback store unavailable")
	}
	if expected == "" {
		if s.found {
			return false, nil
		}
	} else if !s.found || s.value != expected {
		return false, nil
	}
	s.value = next
	s.found = next != ""
	return true, nil
}

func (s *openAIFailbackStoreStub) AcquireOpenAIFailbackProbe(context.Context, string, string, time.Duration) (bool, error) {
	if s.unavailable {
		return false, errors.New("failback store unavailable")
	}
	return true, nil
}

func (s *openAIFailbackStoreStub) ReleaseOpenAIFailbackProbe(context.Context, string, string) error {
	if s.unavailable {
		return errors.New("failback store unavailable")
	}
	return nil
}

func (u *openAIFailbackProbeUpstream) Do(req *http.Request, proxyURL string, accountID int64, concurrency int) (*http.Response, error) {
	u.request = req
	u.proxyURL = proxyURL
	u.accountID = accountID
	u.concurrency = concurrency
	return u.response, u.err
}

func (u *openAIFailbackProbeUpstream) DoWithTLS(
	req *http.Request,
	proxyURL string,
	accountID int64,
	concurrency int,
	_ *tlsfingerprint.Profile,
) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

func TestOpenAIFailbackProbeUsesSameMappedModelAndCheapOutput(t *testing.T) {
	upstream := &openAIFailbackProbeUpstream{response: failbackProbeResponse(
		http.StatusOK,
		"data: {\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\n",
	)}
	account := &Account{
		ID:          40,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 3,
		Credentials: map[string]any{"api_key": "test-key"},
	}
	svc := &OpenAIGatewayService{httpUpstream: upstream, cfg: &config.Config{}}

	result, err := svc.runOpenAIFailbackProbe(
		context.Background(), account, "gpt-5-mini", OpenAIEndpointCapabilityChatCompletions,
	)
	require.NoError(t, err)
	require.Greater(t, result.TTFTMS, 0)
	require.Equal(t, int64(40), upstream.accountID)
	require.Equal(t, 3, upstream.concurrency)
	require.Equal(t, "/v1/chat/completions", upstream.request.URL.Path)
	require.Equal(t, "Bearer test-key", upstream.request.Header.Get("Authorization"))
	require.Equal(t, HTTPUpstreamProfileOpenAI, HTTPUpstreamProfileFromContext(upstream.request.Context()))

	body, err := io.ReadAll(upstream.request.Body)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(body, &payload))
	require.Equal(t, "gpt-5-mini", payload["model"])
	require.EqualValues(t, 128, payload["max_tokens"])
	require.Equal(t, true, payload["stream"])
}

func TestOpenAIFailbackSelectionSuccessfulProbeUsesFallbackWithoutWaiting(t *testing.T) {
	now := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	controller := newOpenAIFailbackController(nil, testOpenAIFailbackConfig())
	t.Cleanup(controller.stopBackgroundProbes)
	controller.now = func() time.Time { return now }
	controller.recordProductionResult(context.Background(), 51, "gpt-5-mini", false, nil)
	now = now.Add(2 * time.Minute)

	upstream := newBlockingOpenAIFailbackProbeUpstream()
	t.Cleanup(upstream.release)
	svc := newOpenAIFailbackSelectionTestService(controller, upstream)
	type selectionResult struct {
		selection *AccountSelectionResult
		err       error
	}
	resultCh := make(chan selectionResult, 1)
	go func() {
		selection, _, err := svc.SelectAccountWithSchedulerForCapability(
			context.Background(), nil, "", "", "gpt-5", nil,
			OpenAIUpstreamTransportAny,
			OpenAIEndpointCapabilityChatCompletions,
			false, false, true,
		)
		resultCh <- selectionResult{selection: selection, err: err}
	}()

	var result selectionResult
	select {
	case result = <-resultCh:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("selection waited for the background failback probe")
	}
	require.NoError(t, result.err)
	require.NotNil(t, result.selection)
	require.Equal(t, int64(52), result.selection.Account.ID)
	select {
	case <-upstream.started:
	case <-time.After(time.Second):
		t.Fatal("background failback probe did not start")
	}

	upstream.release()
	require.Eventually(t, func() bool {
		state := requireOpenAIFailbackState(t, controller, 51, "gpt-5-mini")
		return state.Phase == openAIFailbackPhaseProbation
	}, time.Second, 10*time.Millisecond)

	next, _, err := svc.SelectAccountWithSchedulerForCapability(
		context.Background(), nil, "", "", "gpt-5", nil,
		OpenAIUpstreamTransportAny,
		OpenAIEndpointCapabilityChatCompletions,
		false, false, true,
	)
	require.NoError(t, err)
	require.NotNil(t, next)
	require.Equal(t, int64(51), next.Account.ID)
	if next.ReleaseFunc != nil {
		next.ReleaseFunc()
	}
	selection := result.selection
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIFailbackSelectionReportsEarliestTemporaryCapacityRetry(t *testing.T) {
	now := time.Date(2026, 7, 22, 13, 6, 0, 0, time.UTC)
	controller := newOpenAIFailbackController(nil, testOpenAIFailbackConfig())
	t.Cleanup(controller.stopBackgroundProbes)
	controller.now = func() time.Time { return now }
	controller.recordProductionResult(context.Background(), 51, "gpt-5-mini", false, nil)
	controller.recordProductionResult(context.Background(), 52, "gpt-5-mini", false, nil)

	svc := newOpenAIFailbackSelectionTestService(controller, &openAIFailbackProbeUpstream{})
	selection, _, err := svc.SelectAccountWithSchedulerForCapability(
		context.Background(), nil, "", "", "gpt-5", nil,
		OpenAIUpstreamTransportAny,
		OpenAIEndpointCapabilityChatCompletions,
		false, false, true,
	)

	require.Nil(t, selection)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	var temporary *OpenAITemporaryCapacityError
	require.ErrorAs(t, err, &temporary)
	require.True(t, now.Add(2*time.Minute).Equal(temporary.RetryAt))
}

func TestOpenAIFailbackSelectionFailedProbeFallsBackAndEscalates(t *testing.T) {
	now := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	controller := newOpenAIFailbackController(nil, testOpenAIFailbackConfig())
	t.Cleanup(controller.stopBackgroundProbes)
	controller.now = func() time.Time { return now }
	controller.recordProductionResult(context.Background(), 51, "gpt-5-mini", false, nil)
	now = now.Add(2 * time.Minute)

	upstream := &openAIFailbackProbeUpstream{response: failbackProbeResponse(http.StatusServiceUnavailable, `{"error":{"message":"busy"}}`)}
	svc := newOpenAIFailbackSelectionTestService(controller, upstream)
	selection, _, err := svc.SelectAccountWithSchedulerForCapability(
		context.Background(), nil, "", "", "gpt-5", nil,
		OpenAIUpstreamTransportAny,
		OpenAIEndpointCapabilityChatCompletions,
		false, false, true,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, int64(52), selection.Account.ID)
	var state openAIFailbackState
	require.Eventually(t, func() bool {
		state = requireOpenAIFailbackState(t, controller, 51, "gpt-5-mini")
		return state.CooldownLevel == 1
	}, time.Second, 10*time.Millisecond)
	require.Equal(t, 1, state.CooldownLevel)
	require.Equal(t, int64(300), state.CooldownSeconds)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIFailbackConcurrentSelectionsRunOneProbeAndUseFallback(t *testing.T) {
	now := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	controller := newOpenAIFailbackController(nil, testOpenAIFailbackConfig())
	t.Cleanup(controller.stopBackgroundProbes)
	controller.now = func() time.Time { return now }
	controller.recordProductionResult(context.Background(), 51, "gpt-5-mini", false, nil)
	now = now.Add(2 * time.Minute)

	upstream := newBlockingOpenAIFailbackProbeUpstream()
	t.Cleanup(upstream.release)
	svc := newOpenAIFailbackSelectionTestService(controller, upstream)

	const requestCount = 20
	type result struct {
		selection *AccountSelectionResult
		err       error
	}
	start := make(chan struct{})
	results := make(chan result, requestCount)
	var wg sync.WaitGroup
	for i := 0; i < requestCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			selection, _, err := svc.SelectAccountWithSchedulerForCapability(
				context.Background(), nil, "", "", "gpt-5", nil,
				OpenAIUpstreamTransportAny,
				OpenAIEndpointCapabilityChatCompletions,
				false, false, true,
			)
			results <- result{selection: selection, err: err}
		}()
	}
	close(start)

	for i := 0; i < requestCount; i++ {
		select {
		case current := <-results:
			require.NoError(t, current.err)
			require.NotNil(t, current.selection)
			require.Equal(t, int64(52), current.selection.Account.ID)
			if current.selection.ReleaseFunc != nil {
				current.selection.ReleaseFunc()
			}
		case <-time.After(time.Second):
			t.Fatal("a selection waited for the background failback probe")
		}
	}
	wg.Wait()
	select {
	case <-upstream.started:
	case <-time.After(time.Second):
		t.Fatal("background failback probe did not start")
	}
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, int64(1), upstream.calls.Load())

	upstream.release()
	require.Eventually(t, func() bool {
		state := requireOpenAIFailbackState(t, controller, 51, "gpt-5-mini")
		return state.Phase == openAIFailbackPhaseProbation
	}, time.Second, 10*time.Millisecond)
	controller.stopBackgroundProbes()
	require.Equal(t, int64(1), upstream.calls.Load())
}

func TestOpenAIFailbackSelectionWithoutFallbackProbesSynchronously(t *testing.T) {
	now := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	controller := newOpenAIFailbackController(nil, testOpenAIFailbackConfig())
	t.Cleanup(controller.stopBackgroundProbes)
	controller.now = func() time.Time { return now }
	controller.recordProductionResult(context.Background(), 51, "gpt-5-mini", false, nil)
	now = now.Add(2 * time.Minute)

	upstream := &openAIFailbackProbeUpstream{response: failbackProbeResponse(
		http.StatusOK,
		"data: {\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\n",
	)}
	svc := newOpenAIFailbackSelectionTestService(controller, upstream)
	svc.accountRepo = schedulerTestOpenAIAccountRepo{accounts: openAIFailbackSelectionTestAccounts()[:1]}

	selection, _, err := svc.SelectAccountWithSchedulerForCapability(
		context.Background(), nil, "", "", "gpt-5", nil,
		OpenAIUpstreamTransportAny,
		OpenAIEndpointCapabilityChatCompletions,
		false, false, true,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, int64(51), selection.Account.ID)
	require.Equal(t, openAIFailbackPhaseProbation, requireOpenAIFailbackState(t, controller, 51, "gpt-5-mini").Phase)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIFailbackRejectedBackgroundProbeReleasesSlot(t *testing.T) {
	now := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	controller := newOpenAIFailbackControllerWithExecutor(
		nil,
		testOpenAIFailbackConfig(),
		rejectingOpenAIFailbackProbeExecutor{},
	)
	t.Cleanup(controller.stopBackgroundProbes)
	controller.now = func() time.Time { return now }
	controller.recordProductionResult(context.Background(), 51, "gpt-5-mini", false, nil)
	now = now.Add(2 * time.Minute)

	releasedIDs := make([]int64, 0, 2)
	svc := newOpenAIFailbackSelectionTestService(controller, &openAIFailbackProbeUpstream{})
	svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{releasedIDs: &releasedIDs})
	selection, _, err := svc.SelectAccountWithSchedulerForCapability(
		context.Background(), nil, "", "", "gpt-5", nil,
		OpenAIUpstreamTransportAny,
		OpenAIEndpointCapabilityChatCompletions,
		false, false, true,
	)
	require.NoError(t, err)
	require.Equal(t, int64(52), selection.Account.ID)
	require.Contains(t, releasedIDs, int64(51))
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIFailbackWaitPlanDoesNotProbeWithoutSlot(t *testing.T) {
	now := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	controller := newOpenAIFailbackController(nil, testOpenAIFailbackConfig())
	t.Cleanup(controller.stopBackgroundProbes)
	controller.now = func() time.Time { return now }
	controller.recordProductionResult(context.Background(), 51, "gpt-5-mini", false, nil)
	now = now.Add(2 * time.Minute)

	upstream := newBlockingOpenAIFailbackProbeUpstream()
	t.Cleanup(upstream.release)
	svc := newOpenAIFailbackSelectionTestService(controller, upstream)
	svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{
		acquireResults: map[int64]bool{51: false, 52: false},
	})
	selection, _, err := svc.SelectAccountWithSchedulerForCapability(
		context.Background(), nil, "", "", "gpt-5", nil,
		OpenAIUpstreamTransportAny,
		OpenAIEndpointCapabilityChatCompletions,
		false, false, true,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, int64(52), selection.Account.ID)
	require.NotNil(t, selection.WaitPlan)
	require.Equal(t, int64(0), upstream.calls.Load())
}

func TestOpenAIFailbackShutdownCancelsProbeAndReleasesSlot(t *testing.T) {
	now := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	controller := newOpenAIFailbackController(nil, testOpenAIFailbackConfig())
	controller.now = func() time.Time { return now }
	controller.recordProductionResult(context.Background(), 51, "gpt-5-mini", false, nil)
	now = now.Add(2 * time.Minute)

	upstream := newBlockingOpenAIFailbackProbeUpstream()
	t.Cleanup(upstream.release)
	releasedIDs := make([]int64, 0, 2)
	svc := newOpenAIFailbackSelectionTestService(controller, upstream)
	svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{releasedIDs: &releasedIDs})
	selection, _, err := svc.SelectAccountWithSchedulerForCapability(
		context.Background(), nil, "", "", "gpt-5", nil,
		OpenAIUpstreamTransportAny,
		OpenAIEndpointCapabilityChatCompletions,
		false, false, true,
	)
	require.NoError(t, err)
	require.Equal(t, int64(52), selection.Account.ID)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
	select {
	case <-upstream.started:
	case <-time.After(time.Second):
		t.Fatal("background failback probe did not start")
	}

	svc.CloseOpenAIWSPool()
	require.Contains(t, releasedIDs, int64(51))
	require.Contains(t, releasedIDs, int64(52))
	state := requireOpenAIFailbackState(t, controller, 51, "gpt-5-mini")
	require.Equal(t, 0, state.CooldownLevel)
	require.Equal(t, openAIFailbackPhaseCooldown, state.Phase)
}

func TestOpenAIFailbackShutdownStopsLazilyInitializedController(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIScheduler.FailbackProbeEnabled = true
	svc := &OpenAIGatewayService{cfg: cfg}

	require.Nil(t, svc.openaiFailback)
	svc.CloseOpenAIWSPool()

	controller := svc.getOpenAIFailbackController()
	require.NotNil(t, controller)
	require.ErrorIs(t, controller.probeCtx.Err(), context.Canceled)
	require.False(t, controller.probeExecutor.Submit(func() {}))
}

func newOpenAIFailbackSelectionTestService(
	controller *openAIFailbackController,
	upstream HTTPUpstream,
) *OpenAIGatewayService {
	accounts := openAIFailbackSelectionTestAccounts()
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	return &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
		httpUpstream:       upstream,
		openaiFailback:     controller,
	}
}

func openAIFailbackSelectionTestAccounts() []Account {
	return []Account{
		{
			ID: 51, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
			Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0,
			Credentials: map[string]any{
				"api_key":       "high-key",
				"model_mapping": map[string]any{"gpt-5": "gpt-5-mini"},
			},
		},
		{
			ID: 52, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
			Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 10,
			Credentials: map[string]any{
				"api_key":       "lower-key",
				"model_mapping": map[string]any{"gpt-5": "gpt-5-mini"},
			},
		},
	}
}

func failbackProbeResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
