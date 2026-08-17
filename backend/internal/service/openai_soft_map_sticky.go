package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/google/uuid"
)

const (
	openAISoftMapPhaseSticky    = "sticky"
	openAISoftMapPhaseProbation = "probation"
	openAISoftMapStateTTL       = 7 * 24 * time.Hour
	openAISoftMapStoreTimeout   = 2 * time.Second
	openAISoftMapCASAttempts    = 5
	openAISoftMapKeyPrefix      = "softmap:"
)

type openAISoftMapStickyConfig struct {
	stickyEnabled        bool
	probeEnabled         bool
	defaultProbeInterval time.Duration
	probeIntervalStep    time.Duration
	maxProbeInterval     time.Duration
	probeTimeout         time.Duration
	maxTTFT              time.Duration
	minHealthyRequests   int
	probation            time.Duration
}

func newOpenAISoftMapStickyConfig(cfg config.GatewaySoftModelMappingConfig) openAISoftMapStickyConfig {
	return openAISoftMapStickyConfig{
		stickyEnabled:        cfg.StickyEnabled,
		probeEnabled:         cfg.ProbeEnabled && cfg.StickyEnabled,
		defaultProbeInterval: time.Duration(cfg.DefaultProbeIntervalSeconds) * time.Second,
		probeIntervalStep:    time.Duration(cfg.ProbeIntervalIncrementSeconds) * time.Second,
		maxProbeInterval:     time.Duration(cfg.ProbeIntervalMaxSeconds) * time.Second,
		probeTimeout:         time.Duration(cfg.ProbeTimeoutSeconds) * time.Second,
		maxTTFT:              time.Duration(cfg.MaxTTFTMs) * time.Millisecond,
		minHealthyRequests:   cfg.MinHealthyRequests,
		probation:            time.Duration(cfg.ProbationSeconds) * time.Second,
	}
}

type openAISoftMapStickyState struct {
	Phase              string   `json:"phase"`
	PrimaryModel       string   `json:"primary_model"`
	ActiveModel        string   `json:"active_model"`
	FallbackModels     []string `json:"fallback_models,omitempty"`
	ProbeDueUnixMilli  int64    `json:"probe_due_unix_ms,omitempty"`
	ProbeIntervalSec   int64    `json:"probe_interval_seconds,omitempty"`
	ProbeFailures      int      `json:"probe_failures,omitempty"`
	HealthyRequests    int      `json:"healthy_requests,omitempty"`
	ProbationUntilMS   int64    `json:"probation_until_unix_ms,omitempty"`
	LastProbeTTFTMS    int      `json:"last_probe_ttft_ms,omitempty"`
	LastFailure        string   `json:"last_failure,omitempty"`
	UpdatedAtUnixMilli int64    `json:"updated_at_unix_ms"`
}

type openAISoftMapLocalState struct {
	raw       string
	expiresAt time.Time
}

type openAISoftMapLocalLease struct {
	owner     string
	expiresAt time.Time
}

type openAISoftMapStickyMetrics struct {
	stickyEnter  atomic.Int64
	stickyClear  atomic.Int64
	probeTotal   atomic.Int64
	probeSuccess atomic.Int64
	probeFailure atomic.Int64
	restore      atomic.Int64
	relapse      atomic.Int64
}

type openAISoftMapStickyController struct {
	store OpenAIFailbackStore
	cfg   openAISoftMapStickyConfig
	now   func() time.Time

	probeCtx      context.Context
	probeCancel   context.CancelFunc
	probeExecutor openAIFailbackProbeExecutor
	probeStopOnce sync.Once

	mu              sync.Mutex
	local           map[string]openAISoftMapLocalState
	leases          map[string]openAISoftMapLocalLease
	scheduledProbes map[string]struct{}
	metrics         openAISoftMapStickyMetrics
}

type openAISoftMapAttemptPlan struct {
	phase       string
	primary     string
	active      string
	chain       []string
	shouldProbe bool
}

func newOpenAISoftMapStickyController(store OpenAIFailbackStore, cfg openAISoftMapStickyConfig) *openAISoftMapStickyController {
	return newOpenAISoftMapStickyControllerWithExecutor(store, cfg, newOpenAIFailbackProbeExecutor())
}

func newOpenAISoftMapStickyControllerWithExecutor(
	store OpenAIFailbackStore,
	cfg openAISoftMapStickyConfig,
	executor openAIFailbackProbeExecutor,
) *openAISoftMapStickyController {
	probeCtx, probeCancel := context.WithCancel(context.Background())
	return &openAISoftMapStickyController{
		store:           store,
		cfg:             cfg,
		now:             time.Now,
		probeCtx:        probeCtx,
		probeCancel:     probeCancel,
		probeExecutor:   executor,
		local:           make(map[string]openAISoftMapLocalState),
		leases:          make(map[string]openAISoftMapLocalLease),
		scheduledProbes: make(map[string]struct{}),
	}
}

func (c *openAISoftMapStickyController) stopBackgroundProbes() {
	if c == nil {
		return
	}
	c.probeStopOnce.Do(func() {
		if c.probeCancel != nil {
			c.probeCancel()
		}
		if c.probeExecutor != nil {
			c.probeExecutor.Stop()
		}
	})
}

func (s *OpenAIGatewayService) getOpenAISoftMapStickyController() *openAISoftMapStickyController {
	if s == nil {
		return nil
	}
	if s.cfg == nil || !s.cfg.Gateway.SoftModelMapping.StickyEnabled {
		return s.openaiSoftMapSticky
	}
	s.openaiSoftMapStickyOnce.Do(func() {
		if s.openaiSoftMapSticky == nil {
			var store OpenAIFailbackStore
			if candidate, ok := s.cache.(OpenAIFailbackStore); ok {
				store = candidate
			}
			s.openaiSoftMapSticky = newOpenAISoftMapStickyController(
				store,
				newOpenAISoftMapStickyConfig(s.cfg.Gateway.SoftModelMapping),
			)
		}
	})
	return s.openaiSoftMapSticky
}

func openAISoftMapStateKey(accountID int64, primaryModel string) (string, bool) {
	primaryModel = strings.TrimSpace(primaryModel)
	if accountID <= 0 || primaryModel == "" {
		return "", false
	}
	sum := sha256.Sum256([]byte(strings.ToLower(primaryModel)))
	return openAISoftMapKeyPrefix + "a" + strconv.FormatInt(accountID, 10) + ":m" + hex.EncodeToString(sum[:16]), true
}

func isOpenAISoftMapStickyTriggerError(err error) bool {
	var failoverErr *UpstreamFailoverError
	if !errors.As(err, &failoverErr) || failoverErr == nil {
		return false
	}
	if IsModelUnavailableFailover(err) {
		return true
	}
	if failoverErr.IsOpenAIPreOutputTimeout() {
		return true
	}
	switch failoverErr.StatusCode {
	case http.StatusTooManyRequests,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func shouldDeferOpenAISameAccountModelUnavailableRecording(account *Account, requestedModel string) bool {
	return account != nil &&
		NormalizeOpenAICompatiblePlatform(account.Platform) == PlatformOpenAI &&
		account.HasSoftModelFallbacks(requestedModel)
}

func (c *openAISoftMapStickyController) resolveAttemptPlan(
	ctx context.Context,
	account *Account,
	requestedModel string,
	baseChain []string,
) openAISoftMapAttemptPlan {
	requestedModel = strings.TrimSpace(requestedModel)
	plan := openAISoftMapAttemptPlan{
		primary: requestedModel,
		chain:   append([]string(nil), baseChain...),
	}
	if c == nil || !c.cfg.stickyEnabled || account == nil ||
		NormalizeOpenAICompatiblePlatform(account.Platform) != PlatformOpenAI ||
		requestedModel == "" {
		return plan
	}
	softTargets := account.GetSoftModelFallbacks(requestedModel)
	key, ok := openAISoftMapStateKey(account.ID, requestedModel)
	if len(softTargets) == 0 {
		// Mapping removed while sticky state remains: drop stale sticky winners.
		if ok {
			if _, found := c.readState(ctx, key); found {
				c.clearSticky(ctx, account.ID, requestedModel, "soft_targets_removed")
			}
		}
		return plan
	}
	if len(plan.chain) == 0 {
		plan.chain = []string{requestedModel}
	}
	if !ok {
		return plan
	}
	state, found := c.readState(ctx, key)
	if !found {
		return plan
	}
	active := strings.TrimSpace(state.ActiveModel)
	if active == "" {
		return plan
	}

	switch state.Phase {
	case openAISoftMapPhaseSticky:
		plan.phase = openAISoftMapPhaseSticky
		plan.active = active
		plan.chain = buildOpenAISoftMapStickyChain(active, softTargets, baseChain)
		plan.shouldProbe = c.cfg.probeEnabled && c.now().UnixMilli() >= state.ProbeDueUnixMilli
	case openAISoftMapPhaseProbation:
		plan.phase = openAISoftMapPhaseProbation
		plan.active = active
		plan.chain = buildOpenAISoftMapProbationChain(requestedModel, active, softTargets, baseChain)
	}
	return plan
}

func buildOpenAISoftMapStickyChain(active string, softTargets, baseChain []string) []string {
	active = strings.TrimSpace(active)
	if active == "" {
		return append([]string(nil), baseChain...)
	}
	chain := []string{active}
	seen := map[string]struct{}{active: {}}
	for _, model := range softTargets {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		chain = append(chain, model)
	}
	// Keep any global fallbacks that were already in baseChain after soft targets.
	if len(baseChain) > 1 {
		primary := strings.TrimSpace(baseChain[0])
		for _, model := range baseChain[1:] {
			model = strings.TrimSpace(model)
			if model == "" || model == primary {
				continue
			}
			if _, ok := seen[model]; ok {
				continue
			}
			// Skip re-adding primary while sticky.
			seen[model] = struct{}{}
			chain = append(chain, model)
		}
	}
	return chain
}

func buildOpenAISoftMapProbationChain(primary, active string, softTargets, baseChain []string) []string {
	primary = strings.TrimSpace(primary)
	active = strings.TrimSpace(active)
	chain := make([]string, 0, len(baseChain)+2)
	seen := make(map[string]struct{}, len(baseChain)+2)
	appendModel := func(model string) {
		model = strings.TrimSpace(model)
		if model == "" {
			return
		}
		if _, ok := seen[model]; ok {
			return
		}
		seen[model] = struct{}{}
		chain = append(chain, model)
	}
	appendModel(primary)
	appendModel(active)
	for _, model := range softTargets {
		appendModel(model)
	}
	for _, model := range baseChain {
		appendModel(model)
	}
	return chain
}

func (c *openAISoftMapStickyController) enterSticky(
	ctx context.Context,
	accountID int64,
	primaryModel, activeModel string,
	softTargets []string,
	reason string,
) {
	if c == nil || !c.cfg.stickyEnabled {
		return
	}
	primaryModel = strings.TrimSpace(primaryModel)
	activeModel = strings.TrimSpace(activeModel)
	key, ok := openAISoftMapStateKey(accountID, primaryModel)
	if !ok || activeModel == "" || activeModel == primaryModel {
		return
	}
	now := c.now()
	fallbacks := filterSoftModelFallbacks(primaryModel, softTargets)
	interval := c.cfg.defaultProbeInterval
	if interval <= 0 {
		interval = 2 * time.Minute
	}
	_, _ = c.mutateState(ctx, key, func(current openAISoftMapStickyState, exists bool) (openAISoftMapStickyState, bool) {
		return openAISoftMapStickyState{
			Phase:              openAISoftMapPhaseSticky,
			PrimaryModel:       primaryModel,
			ActiveModel:        activeModel,
			FallbackModels:     fallbacks,
			ProbeDueUnixMilli:  now.Add(interval).UnixMilli(),
			ProbeIntervalSec:   int64(interval / time.Second),
			ProbeFailures:      0,
			LastFailure:        strings.TrimSpace(reason),
			UpdatedAtUnixMilli: now.UnixMilli(),
		}, true
	})
	c.metrics.stickyEnter.Add(1)
	slog.Info("openai_soft_map_sticky_enter",
		"account_id", accountID,
		"primary_model", primaryModel,
		"active_model", activeModel,
		"reason", strings.TrimSpace(reason),
	)
}

func (c *openAISoftMapStickyController) clearSticky(ctx context.Context, accountID int64, primaryModel, reason string) {
	if c == nil || !c.cfg.stickyEnabled {
		return
	}
	key, ok := openAISoftMapStateKey(accountID, primaryModel)
	if !ok {
		return
	}
	if _, exists := c.readState(ctx, key); !exists {
		return
	}
	_, _ = c.mutateState(ctx, key, func(current openAISoftMapStickyState, exists bool) (openAISoftMapStickyState, bool) {
		return openAISoftMapStickyState{}, false
	})
	c.metrics.stickyClear.Add(1)
	slog.Info("openai_soft_map_sticky_clear",
		"account_id", accountID,
		"primary_model", strings.TrimSpace(primaryModel),
		"reason", strings.TrimSpace(reason),
	)
}

func (c *openAISoftMapStickyController) recordPrimarySuccess(
	ctx context.Context,
	accountID int64,
	primaryModel string,
	firstTokenMS *int,
) {
	if c == nil || !c.cfg.stickyEnabled {
		return
	}
	key, ok := openAISoftMapStateKey(accountID, primaryModel)
	if !ok {
		return
	}
	now := c.now()
	// Missing TTFT must not count as healthy production evidence.
	if firstTokenMS == nil {
		return
	}
	slow := time.Duration(*firstTokenMS)*time.Millisecond > c.cfg.maxTTFT
	state, found := c.mutateState(ctx, key, func(current openAISoftMapStickyState, exists bool) (openAISoftMapStickyState, bool) {
		if !exists || current.Phase != openAISoftMapPhaseProbation {
			return current, exists
		}
		if slow {
			// Slow primary production traffic: stay sticky on mapped model.
			interval := c.nextProbeInterval(current)
			current.Phase = openAISoftMapPhaseSticky
			current.HealthyRequests = 0
			current.ProbationUntilMS = 0
			current.ProbeFailures++
			current.ProbeIntervalSec = int64(interval / time.Second)
			current.ProbeDueUnixMilli = now.Add(interval).UnixMilli()
			current.LastFailure = "production_slow"
			current.UpdatedAtUnixMilli = now.UnixMilli()
			return current, true
		}
		current.HealthyRequests++
		current.UpdatedAtUnixMilli = now.UnixMilli()
		if current.HealthyRequests >= c.cfg.minHealthyRequests &&
			(current.ProbationUntilMS == 0 || now.UnixMilli() >= current.ProbationUntilMS) {
			return openAISoftMapStickyState{}, false
		}
		return current, true
	})
	if !found {
		c.metrics.restore.Add(1)
		slog.Info("openai_soft_map_primary_restored",
			"account_id", accountID,
			"primary_model", strings.TrimSpace(primaryModel),
		)
		return
	}
	if state.Phase == openAISoftMapPhaseSticky && state.LastFailure == "production_slow" {
		c.metrics.relapse.Add(1)
	}
}

func (c *openAISoftMapStickyController) recordPrimaryTriggerFailure(
	ctx context.Context,
	accountID int64,
	primaryModel, activeModel string,
	softTargets []string,
	reason string,
) {
	// Relapse from probation back to sticky when primary fails again with a soft trigger.
	if strings.TrimSpace(activeModel) == "" {
		soft := filterSoftModelFallbacks(primaryModel, softTargets)
		if len(soft) > 0 {
			activeModel = soft[0]
		}
	}
	if strings.TrimSpace(activeModel) == "" {
		c.clearSticky(ctx, accountID, primaryModel, reason)
		return
	}
	c.enterSticky(ctx, accountID, primaryModel, activeModel, softTargets, reason)
	c.metrics.relapse.Add(1)
}

func (c *openAISoftMapStickyController) nextProbeInterval(state openAISoftMapStickyState) time.Duration {
	interval := c.cfg.defaultProbeInterval
	if state.ProbeIntervalSec > 0 {
		interval = time.Duration(state.ProbeIntervalSec) * time.Second
	}
	if state.ProbeFailures > 0 && c.cfg.probeIntervalStep > 0 {
		interval += time.Duration(state.ProbeFailures) * c.cfg.probeIntervalStep
	}
	if interval < c.cfg.defaultProbeInterval {
		interval = c.cfg.defaultProbeInterval
	}
	if c.cfg.maxProbeInterval > 0 && interval > c.cfg.maxProbeInterval {
		interval = c.cfg.maxProbeInterval
	}
	if interval <= 0 {
		interval = 2 * time.Minute
	}
	return interval
}

func (c *openAISoftMapStickyController) recordProbeFailure(ctx context.Context, accountID int64, primaryModel, reason string) {
	if c == nil || !c.cfg.probeEnabled {
		return
	}
	key, ok := openAISoftMapStateKey(accountID, primaryModel)
	if !ok {
		return
	}
	now := c.now()
	_, _ = c.mutateState(ctx, key, func(current openAISoftMapStickyState, exists bool) (openAISoftMapStickyState, bool) {
		if !exists || current.Phase != openAISoftMapPhaseSticky {
			return current, exists
		}
		current.ProbeFailures++
		interval := c.nextProbeInterval(current)
		current.ProbeIntervalSec = int64(interval / time.Second)
		current.ProbeDueUnixMilli = now.Add(interval).UnixMilli()
		current.LastFailure = strings.TrimSpace(reason)
		current.UpdatedAtUnixMilli = now.UnixMilli()
		return current, true
	})
	c.metrics.probeFailure.Add(1)
}

func (c *openAISoftMapStickyController) recordProbeSuccess(ctx context.Context, accountID int64, primaryModel string, ttftMS int) bool {
	if c == nil || !c.cfg.probeEnabled {
		return false
	}
	key, ok := openAISoftMapStateKey(accountID, primaryModel)
	if !ok {
		return false
	}
	now := c.now()
	state, found := c.mutateState(ctx, key, func(current openAISoftMapStickyState, exists bool) (openAISoftMapStickyState, bool) {
		if !exists || current.Phase != openAISoftMapPhaseSticky {
			return current, exists
		}
		current.Phase = openAISoftMapPhaseProbation
		current.ProbeFailures = 0
		current.HealthyRequests = 0
		current.LastProbeTTFTMS = ttftMS
		current.LastFailure = ""
		current.ProbationUntilMS = now.Add(c.cfg.probation).UnixMilli()
		current.ProbeDueUnixMilli = 0
		current.UpdatedAtUnixMilli = now.UnixMilli()
		return current, true
	})
	if found && state.Phase == openAISoftMapPhaseProbation {
		c.metrics.probeSuccess.Add(1)
		slog.Info("openai_soft_map_probation_started",
			"account_id", accountID,
			"primary_model", strings.TrimSpace(primaryModel),
			"probe_ttft_ms", ttftMS,
			"probation_seconds", int64(c.cfg.probation/time.Second),
		)
		return true
	}
	return false
}

func (c *openAISoftMapStickyController) reserveBackgroundProbe(accountID int64, primaryModel string) (string, bool) {
	key, ok := openAISoftMapStateKey(accountID, primaryModel)
	if c == nil || !ok {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.scheduledProbes[key]; exists {
		return key, false
	}
	c.scheduledProbes[key] = struct{}{}
	return key, true
}

func (c *openAISoftMapStickyController) releaseBackgroundProbeReservation(key string) {
	if c == nil || key == "" {
		return
	}
	c.mu.Lock()
	delete(c.scheduledProbes, key)
	c.mu.Unlock()
}

func (c *openAISoftMapStickyController) acquireProbe(ctx context.Context, accountID int64, primaryModel string) (string, bool) {
	if c == nil || !c.cfg.probeEnabled {
		return "", false
	}
	key, ok := openAISoftMapStateKey(accountID, primaryModel)
	if !ok {
		return "", false
	}
	owner := uuid.NewString()
	ttl := c.cfg.probeTimeout + 5*time.Second
	if c.store != nil {
		acquired, err := c.store.AcquireOpenAIFailbackProbe(ctx, key, owner, ttl)
		if err == nil {
			if acquired {
				c.metrics.probeTotal.Add(1)
			}
			return owner, acquired
		}
		slog.Warn("openai_soft_map_probe_lease_failed", "account_id", accountID, "model", primaryModel, "error", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if lease, exists := c.leases[key]; exists && c.now().Before(lease.expiresAt) {
		return "", false
	}
	c.leases[key] = openAISoftMapLocalLease{owner: owner, expiresAt: c.now().Add(ttl)}
	c.metrics.probeTotal.Add(1)
	return owner, true
}

func (c *openAISoftMapStickyController) releaseProbe(ctx context.Context, accountID int64, primaryModel, owner string) {
	key, ok := openAISoftMapStateKey(accountID, primaryModel)
	if c == nil || !ok || owner == "" {
		return
	}
	if c.store != nil {
		if err := c.store.ReleaseOpenAIFailbackProbe(ctx, key, owner); err == nil {
			return
		}
	}
	c.mu.Lock()
	if lease, exists := c.leases[key]; exists && lease.owner == owner {
		delete(c.leases, key)
	}
	c.mu.Unlock()
}

func (c *openAISoftMapStickyController) readState(ctx context.Context, key string) (openAISoftMapStickyState, bool) {
	if c == nil {
		return openAISoftMapStickyState{}, false
	}
	if c.store != nil {
		raw, found, err := c.store.GetOpenAIFailbackState(ctx, key)
		if err == nil && found {
			if state, ok := decodeOpenAISoftMapStickyState(raw); ok {
				c.storeLocalState(key, raw)
				return state, true
			}
		}
	}
	return c.readLocalState(key)
}

func (c *openAISoftMapStickyController) mutateState(
	ctx context.Context,
	key string,
	mutate func(openAISoftMapStickyState, bool) (openAISoftMapStickyState, bool),
) (openAISoftMapStickyState, bool) {
	if c == nil || mutate == nil {
		return openAISoftMapStickyState{}, false
	}
	for attempt := 0; attempt < openAISoftMapCASAttempts; attempt++ {
		var expected string
		current, found := c.readState(ctx, key)
		if found {
			if raw, err := json.Marshal(current); err == nil {
				expected = string(raw)
			}
		}
		next, keep := mutate(current, found)
		if !keep {
			if c.store != nil {
				_, _ = c.store.CompareAndSwapOpenAIFailbackState(ctx, key, expected, "", openAISoftMapStateTTL)
			}
			c.deleteLocalState(key)
			return openAISoftMapStickyState{}, false
		}
		raw, err := json.Marshal(next)
		if err != nil {
			return current, found
		}
		nextRaw := string(raw)
		if c.store != nil {
			swapped, swapErr := c.store.CompareAndSwapOpenAIFailbackState(ctx, key, expected, nextRaw, openAISoftMapStateTTL)
			if swapErr == nil && swapped {
				c.storeLocalState(key, nextRaw)
				return next, true
			}
			if swapErr == nil && !swapped {
				continue
			}
		}
		c.storeLocalState(key, nextRaw)
		return next, true
	}
	return c.readState(ctx, key)
}

func (c *openAISoftMapStickyController) readLocalState(key string) (openAISoftMapStickyState, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.local[key]
	if !ok || c.now().After(entry.expiresAt) {
		delete(c.local, key)
		return openAISoftMapStickyState{}, false
	}
	return decodeOpenAISoftMapStickyState(entry.raw)
}

func (c *openAISoftMapStickyController) storeLocalState(key, raw string) {
	c.mu.Lock()
	c.local[key] = openAISoftMapLocalState{raw: raw, expiresAt: c.now().Add(openAISoftMapStateTTL)}
	c.mu.Unlock()
}

func (c *openAISoftMapStickyController) deleteLocalState(key string) {
	c.mu.Lock()
	delete(c.local, key)
	c.mu.Unlock()
}

func decodeOpenAISoftMapStickyState(raw string) (openAISoftMapStickyState, bool) {
	if strings.TrimSpace(raw) == "" {
		return openAISoftMapStickyState{}, false
	}
	var state openAISoftMapStickyState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return openAISoftMapStickyState{}, false
	}
	if state.Phase != openAISoftMapPhaseSticky && state.Phase != openAISoftMapPhaseProbation {
		return openAISoftMapStickyState{}, false
	}
	if strings.TrimSpace(state.ActiveModel) == "" || strings.TrimSpace(state.PrimaryModel) == "" {
		return openAISoftMapStickyState{}, false
	}
	return state, true
}

// acquireOpenAISoftMapProbeSlot waits briefly for an account concurrency slot so
// background primary probes do not oversubscribe accounts that are already
// serving sticky traffic. Returns (nil, true) when concurrency is unlimited or
// unavailable to enforce.
func (s *OpenAIGatewayService) acquireOpenAISoftMapProbeSlot(
	ctx context.Context,
	account *Account,
) (func(), bool) {
	if s == nil || account == nil {
		return nil, false
	}
	if s.concurrencyService == nil || account.Concurrency <= 0 {
		return func() {}, true
	}
	if ctx == nil {
		ctx = context.Background()
	}
	deadline := time.Now().Add(2 * time.Second)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	for {
		if ctx.Err() != nil {
			return nil, false
		}
		result, err := s.tryAcquireAccountSlot(ctx, account.ID, account.Concurrency)
		if err == nil && result != nil && result.Acquired {
			return result.ReleaseFunc, true
		}
		if !time.Now().Before(deadline) {
			return nil, false
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, false
		case <-timer.C:
		}
	}
}

func (s *OpenAIGatewayService) scheduleOpenAISoftMapPrimaryProbe(
	controller *openAISoftMapStickyController,
	account *Account,
	primaryModel string,
) {
	if s == nil || controller == nil || account == nil || !controller.cfg.probeEnabled ||
		controller.probeExecutor == nil || controller.probeCtx == nil || controller.probeCtx.Err() != nil {
		return
	}
	primaryModel = strings.TrimSpace(primaryModel)
	probeKey, reserved := controller.reserveBackgroundProbe(account.ID, primaryModel)
	if !reserved {
		return
	}
	accountID := account.ID
	// Shallow copy account pointer is fine; probe resolves credentials at runtime.
	accountRef := account
	submitted := controller.probeExecutor.Submit(func() {
		defer controller.releaseBackgroundProbeReservation(probeKey)
		if controller.probeCtx == nil || controller.probeCtx.Err() != nil {
			return
		}
		stateCtx, stateCancel := context.WithTimeout(controller.probeCtx, openAISoftMapStoreTimeout)
		plan := controller.resolveAttemptPlan(stateCtx, accountRef, primaryModel, []string{primaryModel})
		stateCancel()
		if plan.phase != openAISoftMapPhaseSticky || !plan.shouldProbe {
			return
		}
		// Wait for an account concurrency slot so probes do not oversubscribe
		// Concurrency=1 accounts that are already serving sticky traffic.
		releaseSlot, slotAcquired := s.acquireOpenAISoftMapProbeSlot(controller.probeCtx, accountRef)
		if !slotAcquired {
			return
		}
		if releaseSlot != nil {
			defer releaseSlot()
		}
		leaseCtx, leaseCancel := context.WithTimeout(controller.probeCtx, openAISoftMapStoreTimeout)
		owner, acquired := controller.acquireProbe(leaseCtx, accountID, primaryModel)
		leaseCancel()
		if !acquired {
			return
		}
		defer func() {
			releaseCtx, releaseCancel := context.WithTimeout(context.Background(), openAISoftMapStoreTimeout)
			controller.releaseProbe(releaseCtx, accountID, primaryModel, owner)
			releaseCancel()
		}()

		probeCtx, probeCancel := context.WithTimeout(controller.probeCtx, controller.cfg.probeTimeout)
		result, err := s.runOpenAIFailbackProbe(probeCtx, accountRef, primaryModel, "")
		probeCancel()
		stateCtx, stateCancel = context.WithTimeout(context.Background(), openAISoftMapStoreTimeout)
		defer stateCancel()
		if err != nil {
			reason := "probe_error"
			if errors.Is(err, context.DeadlineExceeded) {
				reason = "probe_timeout"
			}
			controller.recordProbeFailure(stateCtx, accountID, primaryModel, reason)
			slog.Warn("openai_soft_map_primary_probe_failed",
				"account_id", accountID,
				"primary_model", primaryModel,
				"error", err,
			)
			return
		}
		if time.Duration(result.TTFTMS)*time.Millisecond > controller.cfg.maxTTFT {
			controller.recordProbeFailure(stateCtx, accountID, primaryModel, "probe_slow")
			return
		}
		controller.recordProbeSuccess(stateCtx, accountID, primaryModel, result.TTFTMS)
	})
	if !submitted {
		controller.releaseBackgroundProbeReservation(probeKey)
	}
}
