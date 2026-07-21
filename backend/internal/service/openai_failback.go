package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/google/uuid"
)

const (
	openAIFailbackPhaseCooldown  = "cooldown"
	openAIFailbackPhaseProbation = "probation"
	openAIFailbackStateTTL       = 7 * 24 * time.Hour
	openAIFailbackStoreTimeout   = 2 * time.Second
	openAIFailbackCASAttempts    = 5
)

type openAIFailbackAction int

const (
	openAIFailbackAllow openAIFailbackAction = iota
	openAIFailbackBlock
	openAIFailbackProbe
)

type openAIFailbackConfig struct {
	enabled            bool
	defaultCooldown    time.Duration
	cooldownIncrement  time.Duration
	maxCooldown        time.Duration
	probation          time.Duration
	probeTimeout       time.Duration
	maxTTFT            time.Duration
	minHealthyRequests int
}

func newOpenAIFailbackConfig(cfg config.GatewayOpenAISchedulerConfig) openAIFailbackConfig {
	return openAIFailbackConfig{
		enabled:            cfg.FailbackProbeEnabled,
		defaultCooldown:    time.Duration(cfg.FailbackDefaultCooldownSeconds) * time.Second,
		cooldownIncrement:  time.Duration(cfg.FailbackCooldownIncrementSeconds) * time.Second,
		maxCooldown:        time.Duration(cfg.FailbackCooldownMaxSeconds) * time.Second,
		probation:          time.Duration(cfg.FailbackProbationSeconds) * time.Second,
		probeTimeout:       time.Duration(cfg.FailbackProbeTimeoutSeconds) * time.Second,
		maxTTFT:            time.Duration(cfg.FailbackMaxTTFTMs) * time.Millisecond,
		minHealthyRequests: cfg.FailbackMinHealthyRequests,
	}
}

type openAIFailbackState struct {
	Phase                   string `json:"phase"`
	CooldownLevel           int    `json:"cooldown_level"`
	CooldownSeconds         int64  `json:"cooldown_seconds"`
	CooldownUntilUnixMilli  int64  `json:"cooldown_until_unix_ms"`
	ProbationUntilUnixMilli int64  `json:"probation_until_unix_ms,omitempty"`
	HealthyRequests         int    `json:"healthy_requests,omitempty"`
	LastProbeTTFTMS         int    `json:"last_probe_ttft_ms,omitempty"`
	LastFailure             string `json:"last_failure,omitempty"`
	UpdatedAtUnixMilli      int64  `json:"updated_at_unix_ms"`
}

type openAIFailbackLocalState struct {
	raw       string
	expiresAt time.Time
	dirty     bool
}

type openAIFailbackLocalLease struct {
	owner     string
	expiresAt time.Time
}

type openAIFailbackMetrics struct {
	probeTotal   atomic.Int64
	probeSuccess atomic.Int64
	probeFailure atomic.Int64
	blocked      atomic.Int64
	relapse      atomic.Int64
	reset        atomic.Int64
}

type openAIFailbackMetricsSnapshot struct {
	ProbeTotal   int64
	ProbeSuccess int64
	ProbeFailure int64
	Blocked      int64
	Relapse      int64
	Reset        int64
}

type openAIFailbackController struct {
	store OpenAIFailbackStore
	cfg   openAIFailbackConfig
	now   func() time.Time

	mu      sync.Mutex
	local   map[string]openAIFailbackLocalState
	leases  map[string]openAIFailbackLocalLease
	metrics openAIFailbackMetrics
}

func newOpenAIFailbackController(store OpenAIFailbackStore, cfg openAIFailbackConfig) *openAIFailbackController {
	return &openAIFailbackController{
		store:  store,
		cfg:    cfg,
		now:    time.Now,
		local:  make(map[string]openAIFailbackLocalState),
		leases: make(map[string]openAIFailbackLocalLease),
	}
}

func (s *OpenAIGatewayService) getOpenAIFailbackController() *openAIFailbackController {
	if s == nil {
		return nil
	}
	if s.cfg == nil || !s.cfg.Gateway.OpenAIScheduler.FailbackProbeEnabled {
		return s.openaiFailback
	}
	s.openaiFailbackOnce.Do(func() {
		if s.openaiFailback == nil {
			var store OpenAIFailbackStore
			if candidate, ok := s.cache.(OpenAIFailbackStore); ok {
				store = candidate
			}
			s.openaiFailback = newOpenAIFailbackController(
				store,
				newOpenAIFailbackConfig(s.cfg.Gateway.OpenAIScheduler),
			)
		}
	})
	return s.openaiFailback
}

func shouldApplyOpenAIFailback(
	platform string,
	account *Account,
	mappedModel string,
	requiredCapability OpenAIEndpointCapability,
	requiredImageCapability OpenAIImagesCapability,
) bool {
	if normalizeOpenAICompatiblePlatform(platform) != PlatformOpenAI || account == nil ||
		normalizeOpenAICompatiblePlatform(account.Platform) != PlatformOpenAI ||
		strings.TrimSpace(mappedModel) == "" || requiredImageCapability != "" {
		return false
	}
	switch requiredCapability {
	case "", OpenAIEndpointCapabilityChatCompletions, OpenAIEndpointCapabilityResponses:
		return true
	default:
		return false
	}
}

func (s *OpenAIGatewayService) allowOpenAIFailbackSelection(
	ctx context.Context,
	controller *openAIFailbackController,
	account *Account,
	mappedModel string,
	requiredCapability OpenAIEndpointCapability,
) bool {
	switch controller.selectionAction(ctx, account.ID, mappedModel) {
	case openAIFailbackAllow:
		return true
	case openAIFailbackBlock:
		return false
	}

	owner, acquired := controller.acquireProbe(ctx, account.ID, mappedModel)
	if !acquired {
		return false
	}
	defer func() {
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), openAIFailbackStoreTimeout)
		controller.releaseProbe(releaseCtx, account.ID, mappedModel, owner)
		releaseCancel()
	}()

	probeCtx, probeCancel := context.WithTimeout(ctx, controller.cfg.probeTimeout)
	result, err := s.runOpenAIFailbackProbe(probeCtx, account, mappedModel, requiredCapability)
	probeCancel()
	if err != nil {
		if ctx.Err() == nil {
			reason := "probe_error"
			if errors.Is(err, context.DeadlineExceeded) {
				reason = "probe_timeout"
			}
			stateCtx, stateCancel := context.WithTimeout(context.Background(), openAIFailbackStoreTimeout)
			controller.recordProbeFailure(stateCtx, account.ID, mappedModel, reason)
			stateCancel()
			slog.Warn("openai_failback_probe_request_failed", "account_id", account.ID, "model", mappedModel, "error", err)
		}
		return false
	}
	if time.Duration(result.TTFTMS)*time.Millisecond > controller.cfg.maxTTFT {
		stateCtx, stateCancel := context.WithTimeout(context.Background(), openAIFailbackStoreTimeout)
		controller.recordProbeFailure(stateCtx, account.ID, mappedModel, "probe_slow")
		stateCancel()
		return false
	}
	stateCtx, stateCancel := context.WithTimeout(context.Background(), openAIFailbackStoreTimeout)
	allowed := controller.recordProbeSuccess(stateCtx, account.ID, mappedModel, result.TTFTMS)
	stateCancel()
	return allowed
}

func openAIFailbackStateKey(accountID int64, model string) (string, bool) {
	model = normalizeOpenAIAccountModelTransientModel(model)
	if accountID <= 0 || model == "" {
		return "", false
	}
	sum := sha256.Sum256([]byte(model))
	return "a" + strconv.FormatInt(accountID, 10) + ":m" + hex.EncodeToString(sum[:16]), true
}

func (c *openAIFailbackController) selectionAction(ctx context.Context, accountID int64, model string) openAIFailbackAction {
	if c == nil || !c.cfg.enabled {
		return openAIFailbackAllow
	}
	key, ok := openAIFailbackStateKey(accountID, model)
	if !ok {
		return openAIFailbackAllow
	}
	state, found := c.readState(ctx, key)
	if !found {
		return openAIFailbackAllow
	}
	switch state.Phase {
	case openAIFailbackPhaseCooldown:
		if c.now().UnixMilli() < state.CooldownUntilUnixMilli {
			c.metrics.blocked.Add(1)
			return openAIFailbackBlock
		}
		return openAIFailbackProbe
	case openAIFailbackPhaseProbation:
		return openAIFailbackAllow
	default:
		return openAIFailbackAllow
	}
}

func (c *openAIFailbackController) recordProductionResult(
	ctx context.Context,
	accountID int64,
	model string,
	success bool,
	firstTokenMS *int,
) {
	if c == nil || !c.cfg.enabled {
		return
	}
	key, ok := openAIFailbackStateKey(accountID, model)
	if !ok {
		return
	}
	now := c.now()
	before, beforeFound := c.readState(ctx, key)
	if success && !beforeFound {
		return
	}
	quickRelapse := beforeFound && before.Phase == openAIFailbackPhaseProbation && now.UnixMilli() < before.ProbationUntilUnixMilli
	slowResult := success && firstTokenMS != nil && time.Duration(*firstTokenMS)*time.Millisecond > c.cfg.maxTTFT
	state, found := c.mutateState(ctx, key, func(current openAIFailbackState, exists bool) (openAIFailbackState, bool) {
		if !success {
			level := 0
			currentQuickRelapse := exists && current.Phase == openAIFailbackPhaseProbation && now.UnixMilli() < current.ProbationUntilUnixMilli
			if currentQuickRelapse {
				level = current.CooldownLevel + 1
			} else if exists && current.Phase == openAIFailbackPhaseCooldown {
				return current, true
			}
			return c.cooldownState(level, now, "production_error"), true
		}

		if !exists || current.Phase != openAIFailbackPhaseProbation {
			return current, exists
		}
		if firstTokenMS != nil && time.Duration(*firstTokenMS)*time.Millisecond > c.cfg.maxTTFT {
			level := 0
			if now.UnixMilli() < current.ProbationUntilUnixMilli {
				level = current.CooldownLevel + 1
			}
			return c.cooldownState(level, now, "production_slow"), true
		}
		if firstTokenMS != nil {
			current.HealthyRequests++
		}
		current.UpdatedAtUnixMilli = now.UnixMilli()
		if now.UnixMilli() >= current.ProbationUntilUnixMilli && current.HealthyRequests >= c.cfg.minHealthyRequests {
			return openAIFailbackState{}, false
		}
		return current, true
	})

	if !success && state.Phase == openAIFailbackPhaseCooldown && (!beforeFound || before.Phase != openAIFailbackPhaseCooldown) {
		slog.Warn("openai_failback_cooldown_started",
			"account_id", accountID,
			"model", model,
			"cooldown_level", state.CooldownLevel,
			"cooldown_seconds", state.CooldownSeconds,
			"reason", state.LastFailure,
		)
	}
	if quickRelapse && (!success || slowResult) && state.Phase == openAIFailbackPhaseCooldown {
		c.metrics.relapse.Add(1)
		slog.Warn("openai_failback_probation_relapse",
			"account_id", accountID,
			"model", model,
			"success", success,
			"ttft_ms", nullableOpenAIFailbackTTFT(firstTokenMS),
			"cooldown_seconds", state.CooldownSeconds,
		)
	}
	if success && beforeFound && before.Phase == openAIFailbackPhaseProbation && !found {
		c.metrics.reset.Add(1)
		slog.Info("openai_failback_cooldown_reset", "account_id", accountID, "model", model)
	}
}

func nullableOpenAIFailbackTTFT(firstTokenMS *int) int {
	if firstTokenMS == nil {
		return 0
	}
	return *firstTokenMS
}

func (c *openAIFailbackController) recordProbeFailure(ctx context.Context, accountID int64, model, reason string) {
	if c == nil || !c.cfg.enabled {
		return
	}
	key, ok := openAIFailbackStateKey(accountID, model)
	if !ok {
		return
	}
	now := c.now()
	state, _ := c.mutateState(ctx, key, func(current openAIFailbackState, exists bool) (openAIFailbackState, bool) {
		level := 0
		if exists {
			level = current.CooldownLevel + 1
		}
		return c.cooldownState(level, now, reason), true
	})
	c.metrics.probeFailure.Add(1)
	slog.Warn("openai_failback_probe_failed",
		"account_id", accountID,
		"model", model,
		"reason", reason,
		"cooldown_level", state.CooldownLevel,
		"cooldown_seconds", state.CooldownSeconds,
	)
}

func (c *openAIFailbackController) recordProbeSuccess(ctx context.Context, accountID int64, model string, ttftMS int) bool {
	if c == nil || !c.cfg.enabled {
		return true
	}
	key, ok := openAIFailbackStateKey(accountID, model)
	if !ok {
		return true
	}
	now := c.now()
	state, found := c.mutateState(ctx, key, func(current openAIFailbackState, exists bool) (openAIFailbackState, bool) {
		if !exists || current.Phase != openAIFailbackPhaseCooldown || now.UnixMilli() < current.CooldownUntilUnixMilli {
			return current, exists
		}
		current.Phase = openAIFailbackPhaseProbation
		current.CooldownUntilUnixMilli = 0
		current.ProbationUntilUnixMilli = now.Add(c.cfg.probation).UnixMilli()
		current.HealthyRequests = 0
		current.LastProbeTTFTMS = ttftMS
		current.LastFailure = ""
		current.UpdatedAtUnixMilli = now.UnixMilli()
		return current, true
	})
	if found && state.Phase == openAIFailbackPhaseProbation {
		c.metrics.probeSuccess.Add(1)
		slog.Info("openai_failback_probation_started",
			"account_id", accountID,
			"model", model,
			"probe_ttft_ms", ttftMS,
			"probation_seconds", int64(c.cfg.probation/time.Second),
		)
		return true
	}
	return false
}

func (c *openAIFailbackController) cooldownState(level int, now time.Time, reason string) openAIFailbackState {
	if level < 0 {
		level = 0
	}
	duration := c.cfg.defaultCooldown
	if c.cfg.cooldownIncrement > 0 && level > 0 {
		maxLevel := int64(c.cfg.maxCooldown / c.cfg.cooldownIncrement)
		if int64(level) > maxLevel+1 {
			duration = c.cfg.maxCooldown
		} else {
			duration += time.Duration(level) * c.cfg.cooldownIncrement
		}
	}
	if duration > c.cfg.maxCooldown {
		duration = c.cfg.maxCooldown
	}
	return openAIFailbackState{
		Phase:                  openAIFailbackPhaseCooldown,
		CooldownLevel:          level,
		CooldownSeconds:        int64(duration / time.Second),
		CooldownUntilUnixMilli: now.Add(duration).UnixMilli(),
		LastFailure:            strings.TrimSpace(reason),
		UpdatedAtUnixMilli:     now.UnixMilli(),
	}
}

func (c *openAIFailbackController) acquireProbe(ctx context.Context, accountID int64, model string) (string, bool) {
	if c == nil || !c.cfg.enabled {
		return "", false
	}
	key, ok := openAIFailbackStateKey(accountID, model)
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
		slog.Warn("openai_failback_probe_lease_failed", "account_id", accountID, "model", model, "error", err)
	}
	if c.acquireLocalProbe(key, owner, ttl) {
		c.metrics.probeTotal.Add(1)
		return owner, true
	}
	return "", false
}

func (c *openAIFailbackController) releaseProbe(ctx context.Context, accountID int64, model, owner string) {
	key, ok := openAIFailbackStateKey(accountID, model)
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

func (c *openAIFailbackController) acquireLocalProbe(key, owner string, ttl time.Duration) bool {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if lease, exists := c.leases[key]; exists && now.Before(lease.expiresAt) {
		return false
	}
	c.leases[key] = openAIFailbackLocalLease{owner: owner, expiresAt: now.Add(ttl)}
	return true
}

func (c *openAIFailbackController) readState(ctx context.Context, key string) (openAIFailbackState, bool) {
	if c.store != nil {
		raw, found, err := c.store.GetOpenAIFailbackState(ctx, key)
		if err == nil {
			if !found {
				c.mu.Lock()
				local, localFound := c.local[key]
				if !localFound || !local.dirty || !c.now().Before(local.expiresAt) {
					delete(c.local, key)
					c.mu.Unlock()
					return openAIFailbackState{}, false
				}
				c.mu.Unlock()
				return decodeOpenAIFailbackState(local.raw)
			}
			state, valid := decodeOpenAIFailbackState(raw)
			if valid {
				c.storeLocalState(key, raw, false)
			}
			return state, valid
		}
	}
	return c.readLocalState(key)
}

func (c *openAIFailbackController) mutateState(
	ctx context.Context,
	key string,
	mutate func(openAIFailbackState, bool) (openAIFailbackState, bool),
) (openAIFailbackState, bool) {
	if c.store != nil {
		for range openAIFailbackCASAttempts {
			raw, found, err := c.store.GetOpenAIFailbackState(ctx, key)
			if err != nil {
				break
			}
			current, valid := decodeOpenAIFailbackState(raw)
			if found && !valid {
				current = openAIFailbackState{}
			}
			currentFound := found && valid
			if !found {
				if local, localFound := c.readDirtyLocalState(key); localFound {
					current = local
					currentFound = true
				}
			}
			next, keep := mutate(current, currentFound)
			nextRaw := ""
			if keep {
				encoded, encodeErr := json.Marshal(next)
				if encodeErr != nil {
					return current, found && valid
				}
				nextRaw = string(encoded)
			}
			expected := ""
			if found {
				expected = raw
			}
			swapped, swapErr := c.store.CompareAndSwapOpenAIFailbackState(ctx, key, expected, nextRaw, openAIFailbackStateTTL)
			if swapErr != nil {
				break
			}
			if !swapped {
				continue
			}
			if keep {
				c.storeLocalState(key, nextRaw, false)
			} else {
				c.deleteLocalState(key)
			}
			return next, keep
		}
	}
	return c.mutateLocalState(key, mutate)
}

func (c *openAIFailbackController) readLocalState(key string) (openAIFailbackState, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, found := c.local[key]
	if !found || !c.now().Before(entry.expiresAt) {
		delete(c.local, key)
		return openAIFailbackState{}, false
	}
	return decodeOpenAIFailbackState(entry.raw)
}

func (c *openAIFailbackController) readDirtyLocalState(key string) (openAIFailbackState, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, found := c.local[key]
	if !found || !entry.dirty || !c.now().Before(entry.expiresAt) {
		return openAIFailbackState{}, false
	}
	return decodeOpenAIFailbackState(entry.raw)
}

func (c *openAIFailbackController) mutateLocalState(
	key string,
	mutate func(openAIFailbackState, bool) (openAIFailbackState, bool),
) (openAIFailbackState, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, found := c.local[key]
	if found && !c.now().Before(entry.expiresAt) {
		delete(c.local, key)
		found = false
	}
	current, valid := decodeOpenAIFailbackState(entry.raw)
	next, keep := mutate(current, found && valid)
	if !keep {
		delete(c.local, key)
		return next, false
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return current, found && valid
	}
	c.local[key] = openAIFailbackLocalState{raw: string(raw), expiresAt: c.now().Add(openAIFailbackStateTTL), dirty: true}
	return next, true
}

func (c *openAIFailbackController) storeLocalState(key, raw string, dirty bool) {
	c.mu.Lock()
	c.local[key] = openAIFailbackLocalState{raw: raw, expiresAt: c.now().Add(openAIFailbackStateTTL), dirty: dirty}
	c.mu.Unlock()
}

func (c *openAIFailbackController) deleteLocalState(key string) {
	c.mu.Lock()
	delete(c.local, key)
	c.mu.Unlock()
}

func decodeOpenAIFailbackState(raw string) (openAIFailbackState, bool) {
	if strings.TrimSpace(raw) == "" {
		return openAIFailbackState{}, false
	}
	var state openAIFailbackState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return openAIFailbackState{}, false
	}
	if state.Phase != openAIFailbackPhaseCooldown && state.Phase != openAIFailbackPhaseProbation {
		return openAIFailbackState{}, false
	}
	return state, true
}

func (c *openAIFailbackController) snapshotMetrics() openAIFailbackMetricsSnapshot {
	if c == nil {
		return openAIFailbackMetricsSnapshot{}
	}
	return openAIFailbackMetricsSnapshot{
		ProbeTotal:   c.metrics.probeTotal.Load(),
		ProbeSuccess: c.metrics.probeSuccess.Load(),
		ProbeFailure: c.metrics.probeFailure.Load(),
		Blocked:      c.metrics.blocked.Load(),
		Relapse:      c.metrics.relapse.Load(),
		Reset:        c.metrics.reset.Load(),
	}
}
