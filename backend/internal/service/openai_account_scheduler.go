package service

import (
	"container/heap"
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	openAIAccountScheduleLayerPreviousResponse                     = "previous_response_id"
	openAIAccountScheduleLayerSessionSticky                        = "session_hash"
	openAIAccountScheduleLayerLoadBalance                          = "load_balance"
	openAIAdvancedSchedulerSettingKey                              = "openai_advanced_scheduler_enabled"
	openAIStickyPreferHigherPrioritySettingKey                     = "openai_sticky_prefer_higher_priority_enabled"
	openAIStickyPreferHigherPriorityMinIntervalSettingKey          = "openai_sticky_prefer_higher_priority_min_interval_seconds"
	openAIStickyFailbackFailureCooldownSettingKey                  = "openai_sticky_failback_failure_cooldown_seconds"
	openAIPreviousResponseRebindSettingKey                         = "openai_previous_response_rebind_enabled"
	openAIPreviousResponseRebindOnlyWhenCurrentUnhealthySettingKey = "openai_previous_response_rebind_only_when_current_unhealthy"
)

const (
	openAIAdvancedSchedulerSettingCacheTTL  = 5 * time.Second
	openAIAdvancedSchedulerSettingDBTimeout = 2 * time.Second
)

const (
	openAIQuotaHeadroomNeutralFactor      = 0.5
	openAIQuotaHeadroomSecondaryLowRemain = 0.10
	openAIQuotaHeadroomSnapshotStaleAfter = 8 * time.Hour
)

type cachedOpenAIAdvancedSchedulerSetting struct {
	enabled   bool
	expiresAt int64
}

type cachedOpenAIStickyPreferHigherPrioritySetting struct {
	cfg       openAIStickyPreferHigherPriorityConfig
	expiresAt int64
}

var openAIAdvancedSchedulerSettingCache atomic.Value // *cachedOpenAIAdvancedSchedulerSetting
var openAIAdvancedSchedulerSettingSF singleflight.Group
var openAIStickyPreferHigherPrioritySettingCache atomic.Value // *cachedOpenAIStickyPreferHigherPrioritySetting
var openAIStickyPreferHigherPrioritySettingSF singleflight.Group

type OpenAIAccountScheduleRequest struct {
	GroupID                    *int64
	Platform                   string
	SessionHash                string
	StickyAccountID            int64
	PreserveStickyBinding      bool
	HasFunctionCallOutput      bool
	PreviousResponseReplayable bool
	PreviousResponseID         string
	RequestedModel             string
	RequiredTransport          OpenAIUpstreamTransport
	RequiredCapability         OpenAIEndpointCapability
	RequiredImageCapability    OpenAIImagesCapability
	RequireCompact             bool
	ExcludedIDs                map[int64]struct{}
}

type OpenAIAccountScheduleDecision struct {
	Layer               string
	StickyPreviousHit   bool
	StickySessionHit    bool
	StickySessionRebind bool
	PreviousRebind      bool
	DropPreviousID      bool
	PreviousAccountID   int64
	RebindReason        string
	CandidateCount      int
	TopK                int
	LatencyMs           int64
	LoadSkew            float64
	SelectedAccountID   int64
	SelectedAccountType string
}

type OpenAIAccountScheduleOptions struct {
	HasFunctionCallOutput      bool
	PreviousResponseReplayable bool
}

type OpenAIAccountSchedulerMetricsSnapshot struct {
	SelectTotal              int64
	StickyPreviousHitTotal   int64
	StickySessionHitTotal    int64
	LoadBalanceSelectTotal   int64
	AccountSwitchTotal       int64
	SchedulerLatencyMsTotal  int64
	SchedulerLatencyMsAvg    float64
	StickyHitRatio           float64
	AccountSwitchRate        float64
	LoadSkewAvg              float64
	RuntimeStatsAccountCount int
}

type OpenAIAccountScheduler interface {
	Select(ctx context.Context, req OpenAIAccountScheduleRequest) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error)
	ReportResult(accountID int64, success bool, firstTokenMs *int)
	ReportSwitch()
	SnapshotMetrics() OpenAIAccountSchedulerMetricsSnapshot
}

type openAIAccountSchedulerMetrics struct {
	selectTotal            atomic.Int64
	stickyPreviousHitTotal atomic.Int64
	stickySessionHitTotal  atomic.Int64
	loadBalanceSelectTotal atomic.Int64
	accountSwitchTotal     atomic.Int64
	latencyMsTotal         atomic.Int64
	loadSkewMilliTotal     atomic.Int64
}

type openAIAccountLoadPlan struct {
	allCandidates             []openAIAccountCandidateScore
	candidates                []openAIAccountCandidateScore
	staleSnapshotCompactRetry []openAIAccountCandidateScore
	selectionOrder            []openAIAccountCandidateScore
	candidateCount            int
	topK                      int
	loadSkew                  float64
}

func (m *openAIAccountSchedulerMetrics) recordSelect(decision OpenAIAccountScheduleDecision) {
	if m == nil {
		return
	}
	m.selectTotal.Add(1)
	m.latencyMsTotal.Add(decision.LatencyMs)
	m.loadSkewMilliTotal.Add(int64(math.Round(decision.LoadSkew * 1000)))
	if decision.StickyPreviousHit {
		m.stickyPreviousHitTotal.Add(1)
	}
	if decision.StickySessionHit {
		m.stickySessionHitTotal.Add(1)
	}
	if decision.Layer == openAIAccountScheduleLayerLoadBalance {
		m.loadBalanceSelectTotal.Add(1)
	}
}

func (m *openAIAccountSchedulerMetrics) recordSwitch() {
	if m == nil {
		return
	}
	m.accountSwitchTotal.Add(1)
}

type openAIAccountRuntimeStats struct {
	accounts     sync.Map
	accountCount atomic.Int64
}

type openAIAccountRuntimeStat struct {
	errorRateEWMABits atomic.Uint64
	ttftEWMABits      atomic.Uint64
	slowStreak        atomic.Int64
	fastStreak        atomic.Int64
	slowScore         atomic.Int64
	sampleCount       atomic.Int64
	slowUntilUnixNano atomic.Int64
	lastTTFTSampleAt  atomic.Int64
	lastScoreUpdateAt atomic.Int64
}

type openAISlowAccountConfig struct {
	enabled           bool
	thresholdMs       int
	softThresholdMs   int
	recoveryTTFTMs    int
	consecutiveCount  int
	minSamples        int
	cooldown          time.Duration
	recoveryFastCount int
	penaltyWeight     float64
	markScore         int
	skipScore         int
	maxScore          int
	decayInterval     time.Duration
}

type openAIAccountRuntimeReport struct {
	errorRate     float64
	ttft          float64
	hasTTFT       bool
	firstTokenMs  int
	sampleCount   int64
	slowStreak    int64
	fastStreak    int64
	slowScore     int64
	slowUntil     time.Time
	markedSlow    bool
	recoveredSlow bool
}

type openAIAccountSlowSnapshot struct {
	marked       bool
	slowUntil    time.Time
	sampleCount  int64
	slowStreak   int64
	fastStreak   int64
	slowScore    int64
	lastSampleAt time.Time
}

func newOpenAIAccountRuntimeStats() *openAIAccountRuntimeStats {
	return &openAIAccountRuntimeStats{}
}

func (s *openAIAccountRuntimeStats) loadOrCreate(accountID int64) *openAIAccountRuntimeStat {
	if value, ok := s.accounts.Load(accountID); ok {
		stat, _ := value.(*openAIAccountRuntimeStat)
		if stat != nil {
			return stat
		}
	}

	stat := &openAIAccountRuntimeStat{}
	stat.ttftEWMABits.Store(math.Float64bits(math.NaN()))
	actual, loaded := s.accounts.LoadOrStore(accountID, stat)
	if !loaded {
		s.accountCount.Add(1)
		return stat
	}
	existing, _ := actual.(*openAIAccountRuntimeStat)
	if existing != nil {
		return existing
	}
	return stat
}

func updateEWMAAtomic(target *atomic.Uint64, sample float64, alpha float64) {
	for {
		oldBits := target.Load()
		oldValue := math.Float64frombits(oldBits)
		newValue := alpha*sample + (1-alpha)*oldValue
		if target.CompareAndSwap(oldBits, math.Float64bits(newValue)) {
			return
		}
	}
}

func (s *openAIAccountRuntimeStats) report(accountID int64, success bool, firstTokenMs *int, slowCfgs ...openAISlowAccountConfig) openAIAccountRuntimeReport {
	if s == nil || accountID <= 0 {
		return openAIAccountRuntimeReport{}
	}
	const alpha = 0.2
	stat := s.loadOrCreate(accountID)

	errorSample := 1.0
	if success {
		errorSample = 0.0
	}
	updateEWMAAtomic(&stat.errorRateEWMABits, errorSample, alpha)

	report := openAIAccountRuntimeReport{}
	if firstTokenMs != nil && *firstTokenMs > 0 {
		ttft := float64(*firstTokenMs)
		report.firstTokenMs = *firstTokenMs
		ttftBits := math.Float64bits(ttft)
		for {
			oldBits := stat.ttftEWMABits.Load()
			oldValue := math.Float64frombits(oldBits)
			if math.IsNaN(oldValue) {
				if stat.ttftEWMABits.CompareAndSwap(oldBits, ttftBits) {
					break
				}
				continue
			}
			newValue := alpha*ttft + (1-alpha)*oldValue
			if stat.ttftEWMABits.CompareAndSwap(oldBits, math.Float64bits(newValue)) {
				break
			}
		}
		if len(slowCfgs) > 0 {
			report = s.updateSlowAccountState(stat, *firstTokenMs, slowCfgs[0])
		}
	}
	report.errorRate, report.ttft, report.hasTTFT = s.snapshot(accountID)
	if firstTokenMs != nil && *firstTokenMs > 0 {
		report.firstTokenMs = *firstTokenMs
	}
	if report.sampleCount == 0 {
		report.sampleCount = stat.sampleCount.Load()
		report.slowStreak = stat.slowStreak.Load()
		report.fastStreak = stat.fastStreak.Load()
		if until := stat.slowUntilUnixNano.Load(); until > 0 {
			report.slowUntil = time.Unix(0, until)
		}
	}
	return report
}

func (s *openAIAccountRuntimeStats) updateSlowAccountState(stat *openAIAccountRuntimeStat, firstTokenMs int, cfg openAISlowAccountConfig) openAIAccountRuntimeReport {
	if stat == nil || !cfg.enabled || firstTokenMs <= 0 {
		return openAIAccountRuntimeReport{}
	}
	cfg = normalizeOpenAISlowAccountConfig(cfg)
	now := time.Now()
	nowNano := now.UnixNano()
	sampleCount := stat.sampleCount.Add(1)
	stat.lastTTFTSampleAt.Store(nowNano)
	score := decayOpenAIAccountSlowScore(stat, now, cfg)

	report := openAIAccountRuntimeReport{
		firstTokenMs: firstTokenMs,
		sampleCount:  sampleCount,
	}
	switch {
	case firstTokenMs > cfg.thresholdMs:
		report.slowStreak = stat.slowStreak.Add(1)
		stat.fastStreak.Store(0)
		report.fastStreak = 0
		score = addOpenAIAccountSlowScore(stat, 3, cfg)
	case firstTokenMs > cfg.softThresholdMs:
		stat.slowStreak.Store(0)
		stat.fastStreak.Store(0)
		report.slowStreak = 0
		report.fastStreak = 0
		score = addOpenAIAccountSlowScore(stat, 1, cfg)
	case firstTokenMs <= cfg.recoveryTTFTMs:
		report.fastStreak = stat.fastStreak.Add(1)
		stat.slowStreak.Store(0)
		report.slowStreak = 0
		score = addOpenAIAccountSlowScore(stat, -1, cfg)
	default:
		stat.slowStreak.Store(0)
		stat.fastStreak.Store(0)
		report.slowStreak = 0
		report.fastStreak = 0
	}

	report.slowScore = score
	if sampleCount >= int64(cfg.minSamples) && score >= int64(cfg.markScore) {
		until := now.Add(cfg.cooldown)
		oldUntilNano := stat.slowUntilUnixNano.Swap(until.UnixNano())
		report.slowUntil = until
		report.markedSlow = oldUntilNano <= nowNano
	}
	if score < int64(cfg.markScore) && report.fastStreak >= int64(cfg.recoveryFastCount) {
		oldUntilNano := stat.slowUntilUnixNano.Load()
		if oldUntilNano > 0 {
			if oldUntilNano <= nowNano || stat.slowUntilUnixNano.CompareAndSwap(oldUntilNano, 0) {
				report.recoveredSlow = true
			}
		}
	}
	if untilNano := stat.slowUntilUnixNano.Load(); untilNano > 0 {
		report.slowUntil = time.Unix(0, untilNano)
	}
	if report.slowStreak == 0 {
		report.slowStreak = stat.slowStreak.Load()
	}
	if report.fastStreak == 0 {
		report.fastStreak = stat.fastStreak.Load()
	}
	if report.slowScore == 0 {
		report.slowScore = stat.slowScore.Load()
	}
	return report
}

func decayOpenAIAccountSlowScore(stat *openAIAccountRuntimeStat, now time.Time, cfg openAISlowAccountConfig) int64 {
	if stat == nil || cfg.decayInterval <= 0 {
		return 0
	}
	nowNano := now.UnixNano()
	for {
		lastNano := stat.lastScoreUpdateAt.Load()
		if lastNano <= 0 {
			if stat.lastScoreUpdateAt.CompareAndSwap(lastNano, nowNano) {
				return stat.slowScore.Load()
			}
			continue
		}
		elapsed := time.Duration(nowNano - lastNano)
		if elapsed < cfg.decayInterval {
			return stat.slowScore.Load()
		}
		decay := int64(elapsed / cfg.decayInterval)
		if decay <= 0 {
			return stat.slowScore.Load()
		}
		newLast := lastNano + int64(time.Duration(decay)*cfg.decayInterval)
		if !stat.lastScoreUpdateAt.CompareAndSwap(lastNano, newLast) {
			continue
		}
		return addOpenAIAccountSlowScore(stat, -decay, cfg)
	}
}

func addOpenAIAccountSlowScore(stat *openAIAccountRuntimeStat, delta int64, cfg openAISlowAccountConfig) int64 {
	if stat == nil {
		return 0
	}
	maxScore := int64(cfg.maxScore)
	if maxScore <= 0 {
		maxScore = 10
	}
	for {
		oldScore := stat.slowScore.Load()
		newScore := oldScore + delta
		if newScore < 0 {
			newScore = 0
		}
		if newScore > maxScore {
			newScore = maxScore
		}
		if stat.slowScore.CompareAndSwap(oldScore, newScore) {
			return newScore
		}
	}
}

func (s *openAIAccountRuntimeStats) snapshot(accountID int64) (errorRate float64, ttft float64, hasTTFT bool) {
	if s == nil || accountID <= 0 {
		return 0, 0, false
	}
	value, ok := s.accounts.Load(accountID)
	if !ok {
		return 0, 0, false
	}
	stat, _ := value.(*openAIAccountRuntimeStat)
	if stat == nil {
		return 0, 0, false
	}
	errorRate = clamp01(math.Float64frombits(stat.errorRateEWMABits.Load()))
	ttftValue := math.Float64frombits(stat.ttftEWMABits.Load())
	if math.IsNaN(ttftValue) {
		return errorRate, 0, false
	}
	return errorRate, ttftValue, true
}

func (s *openAIAccountRuntimeStats) slowSnapshot(accountID int64, now time.Time) openAIAccountSlowSnapshot {
	if s == nil || accountID <= 0 {
		return openAIAccountSlowSnapshot{}
	}
	value, ok := s.accounts.Load(accountID)
	if !ok {
		return openAIAccountSlowSnapshot{}
	}
	stat, _ := value.(*openAIAccountRuntimeStat)
	if stat == nil {
		return openAIAccountSlowSnapshot{}
	}
	snapshot := openAIAccountSlowSnapshot{
		sampleCount: stat.sampleCount.Load(),
		slowStreak:  stat.slowStreak.Load(),
		fastStreak:  stat.fastStreak.Load(),
		slowScore:   stat.slowScore.Load(),
	}
	if last := stat.lastTTFTSampleAt.Load(); last > 0 {
		snapshot.lastSampleAt = time.Unix(0, last)
	}
	if until := stat.slowUntilUnixNano.Load(); until > 0 {
		snapshot.slowUntil = time.Unix(0, until)
		snapshot.marked = now.Before(snapshot.slowUntil)
	}
	return snapshot
}

func normalizeOpenAISlowAccountConfig(cfg openAISlowAccountConfig) openAISlowAccountConfig {
	if cfg.thresholdMs <= 0 {
		cfg.thresholdMs = 30000
	}
	if cfg.softThresholdMs <= 0 || cfg.softThresholdMs >= cfg.thresholdMs {
		cfg.softThresholdMs = 15000
	}
	if cfg.recoveryTTFTMs <= 0 {
		cfg.recoveryTTFTMs = 10000
	}
	if cfg.recoveryTTFTMs >= cfg.softThresholdMs {
		cfg.recoveryTTFTMs = cfg.softThresholdMs / 2
		if cfg.recoveryTTFTMs <= 0 {
			cfg.recoveryTTFTMs = 10000
		}
	}
	if cfg.consecutiveCount <= 0 {
		cfg.consecutiveCount = 2
	}
	if cfg.minSamples <= 0 {
		cfg.minSamples = 3
	}
	if cfg.cooldown < 0 {
		cfg.cooldown = 0
	}
	if cfg.recoveryFastCount <= 0 {
		cfg.recoveryFastCount = 2
	}
	if cfg.penaltyWeight < 0 {
		cfg.penaltyWeight = 0
	}
	if cfg.markScore <= 0 {
		cfg.markScore = 5
	}
	if cfg.skipScore < cfg.markScore {
		cfg.skipScore = 8
	}
	if cfg.maxScore < cfg.skipScore {
		cfg.maxScore = 10
	}
	if cfg.decayInterval <= 0 {
		cfg.decayInterval = time.Minute
	}
	return cfg
}

func (s *openAIAccountRuntimeStats) size() int {
	if s == nil {
		return 0
	}
	return int(s.accountCount.Load())
}

type defaultOpenAIAccountScheduler struct {
	service *OpenAIGatewayService
	metrics openAIAccountSchedulerMetrics
	stats   *openAIAccountRuntimeStats
}

type openAIStickyEscapeConfig struct {
	enabled   bool
	ttftMs    float64
	errorRate float64
}

type openAIStickyPreferHigherPriorityConfig struct {
	stickyEnabled                     bool
	previousResponseEnabled           bool
	previousResponseOnlyWhenUnhealthy bool
	minInterval                       time.Duration
	failureCooldown                   time.Duration
	probeEnabled                      bool
	probeTimeout                      time.Duration
	probeSuccessTTL                   time.Duration
	probeFailureTTL                   time.Duration
}

func newDefaultOpenAIAccountScheduler(service *OpenAIGatewayService, stats *openAIAccountRuntimeStats) OpenAIAccountScheduler {
	if stats == nil {
		stats = newOpenAIAccountRuntimeStats()
	}
	return &defaultOpenAIAccountScheduler{
		service: service,
		stats:   stats,
	}
}

func (s *defaultOpenAIAccountScheduler) Select(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	decision := OpenAIAccountScheduleDecision{}
	start := time.Now()
	slowPreviousAccountID := int64(0)
	slowPreviousReason := ""
	defer func() {
		decision.LatencyMs = time.Since(start).Milliseconds()
		s.metrics.recordSelect(decision)
	}()

	previousResponseID := strings.TrimSpace(req.PreviousResponseID)
	if previousResponseID != "" && normalizeOpenAICompatiblePlatform(req.Platform) == PlatformOpenAI {
		selection, err := s.service.selectAccountByPreviousResponseIDForCapability(
			ctx,
			req.GroupID,
			previousResponseID,
			req.RequestedModel,
			req.ExcludedIDs,
			req.RequiredCapability,
			req.RequireCompact,
		)
		if err != nil {
			return nil, decision, err
		}
		if selection != nil && selection.Account != nil {
			if !s.isAccountTransportCompatible(selection.Account, req.RequiredTransport) {
				if selection.ReleaseFunc != nil {
					selection.ReleaseFunc()
				}
				selection = nil
			}
		}
		if selection != nil && selection.Account != nil {
			failbackCfg := s.service.openAIStickyPreferHigherPriorityConfig(ctx)
			if snapshot, marked := s.service.isOpenAIAccountMarkedSlow(selection.Account.ID, time.Now()); marked {
				if failbackCfg.previousResponseEnabled && !req.HasFunctionCallOutput && req.PreviousResponseReplayable {
					errorRate, ttft, _ := s.stats.snapshot(selection.Account.ID)
					slog.Info("openai.sticky_escape_triggered",
						"account_id", selection.Account.ID,
						"reason", "slow_ttft",
						"binding", "previous_response_id",
						"error_rate", errorRate,
						"ttft", ttft,
						"slow_until", snapshot.slowUntil,
					)
					slowPreviousAccountID = selection.Account.ID
					slowPreviousReason = "slow_ttft"
					if selection.ReleaseFunc != nil {
						selection.ReleaseFunc()
					}
				} else {
					s.service.logOpenAIStickyFailbackSkipped(req, "previous_response_id", selection.Account.ID, 0, openAIStickyRebindSkipReason(req))
					decision.Layer = openAIAccountScheduleLayerPreviousResponse
					decision.StickyPreviousHit = true
					decision.SelectedAccountID = selection.Account.ID
					decision.SelectedAccountType = selection.Account.Type
					if req.SessionHash != "" {
						_ = s.service.BindStickySession(ctx, req.GroupID, req.SessionHash, selection.Account.ID)
					}
					return selection, decision, nil
				}
			}
		}
		if selection != nil && selection.Account != nil && slowPreviousAccountID == 0 {
			failbackCfg := s.service.openAIStickyPreferHigherPriorityConfig(ctx)
			if failbackCfg.previousResponseEnabled &&
				!failbackCfg.previousResponseOnlyWhenUnhealthy &&
				!req.HasFunctionCallOutput &&
				req.PreviousResponseReplayable &&
				s.service.allowOpenAIStickyFailbackAttempt("previous_response_id", req.GroupID, previousResponseID, failbackCfg.minInterval) {
				failbackSelection, reason, failbackErr := s.service.tryAcquireHigherPriorityOpenAIAccount(ctx, req, selection.Account)
				if failbackErr != nil {
					if selection.ReleaseFunc != nil {
						selection.ReleaseFunc()
					}
					return nil, decision, failbackErr
				}
				if failbackSelection != nil && failbackSelection.Account != nil {
					if selection.ReleaseFunc != nil {
						selection.ReleaseFunc()
					}
					decision.Layer = openAIAccountScheduleLayerLoadBalance
					decision.PreviousRebind = true
					decision.DropPreviousID = true
					decision.PreviousAccountID = selection.Account.ID
					decision.RebindReason = reason
					decision.SelectedAccountID = failbackSelection.Account.ID
					decision.SelectedAccountType = failbackSelection.Account.Type
					return failbackSelection, decision, nil
				}
			} else if failbackCfg.previousResponseEnabled && !failbackCfg.previousResponseOnlyWhenUnhealthy {
				reason := openAIStickyRebindSkipReason(req)
				if reason == "current_account_healthy" {
					reason = "attempt_interval"
				}
				s.service.logOpenAIStickyFailbackSkipped(req, "previous_response_id", selection.Account.ID, 0, reason)
			}
			decision.Layer = openAIAccountScheduleLayerPreviousResponse
			decision.StickyPreviousHit = true
			decision.SelectedAccountID = selection.Account.ID
			decision.SelectedAccountType = selection.Account.Type
			if req.SessionHash != "" {
				_ = s.service.BindStickySession(ctx, req.GroupID, req.SessionHash, selection.Account.ID)
			}
			return selection, decision, nil
		}
	}

	selection, escapedSticky, stickyRebind, previousAccountID, rebindReason, err := s.selectBySessionHash(ctx, req)
	if err != nil {
		return nil, decision, err
	}
	if selection != nil && selection.Account != nil {
		decision.Layer = openAIAccountScheduleLayerSessionSticky
		decision.StickySessionHit = true
		decision.StickySessionRebind = stickyRebind
		decision.PreviousAccountID = previousAccountID
		decision.RebindReason = rebindReason
		decision.SelectedAccountID = selection.Account.ID
		decision.SelectedAccountType = selection.Account.Type
		if slowPreviousAccountID > 0 && selection.Account.ID != slowPreviousAccountID {
			decision.PreviousRebind = true
			decision.DropPreviousID = true
			decision.PreviousAccountID = slowPreviousAccountID
			decision.RebindReason = slowPreviousReason
		}
		return selection, decision, nil
	}
	if escapedSticky {
		req.PreserveStickyBinding = true
	}

	selection, candidateCount, topK, loadSkew, err := s.selectByLoadBalance(ctx, req)
	decision.Layer = openAIAccountScheduleLayerLoadBalance
	decision.CandidateCount = candidateCount
	decision.TopK = topK
	decision.LoadSkew = loadSkew
	if err != nil {
		return nil, decision, err
	}
	if selection != nil && selection.Account != nil {
		failbackCfg := s.service.openAIStickyPreferHigherPriorityConfig(ctx)
		if slowPreviousAccountID > 0 && selection.Account.ID != slowPreviousAccountID {
			decision.PreviousRebind = true
			decision.DropPreviousID = true
			decision.PreviousAccountID = slowPreviousAccountID
			decision.RebindReason = slowPreviousReason
		} else if previousResponseID != "" &&
			failbackCfg.previousResponseEnabled &&
			failbackCfg.previousResponseOnlyWhenUnhealthy &&
			req.PreviousResponseReplayable {
			decision.PreviousRebind = true
			decision.DropPreviousID = true
			decision.PreviousAccountID = 0
			decision.RebindReason = "previous_response_account_unavailable"
		} else if previousResponseID != "" && failbackCfg.previousResponseEnabled && failbackCfg.previousResponseOnlyWhenUnhealthy {
			s.service.logOpenAIStickyFailbackSkipped(req, "previous_response_id", 0, 0, openAIStickyRebindSkipReason(req))
		}
		decision.SelectedAccountID = selection.Account.ID
		decision.SelectedAccountType = selection.Account.Type
	}
	return selection, decision, nil
}

func (s *defaultOpenAIAccountScheduler) selectBySessionHash(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
) (*AccountSelectionResult, bool, bool, int64, string, error) {
	sessionHash := strings.TrimSpace(req.SessionHash)
	if sessionHash == "" || s == nil || s.service == nil || s.service.cache == nil {
		return nil, false, false, 0, "", nil
	}

	accountID := req.StickyAccountID
	if accountID <= 0 {
		var err error
		accountID, err = s.service.getStickySessionAccountID(ctx, req.GroupID, sessionHash)
		if err != nil || accountID <= 0 {
			return nil, false, false, 0, "", nil
		}
	}
	if accountID <= 0 {
		return nil, false, false, 0, "", nil
	}
	if req.ExcludedIDs != nil {
		if _, excluded := req.ExcludedIDs[accountID]; excluded {
			return nil, false, false, 0, "", nil
		}
	}

	account, err := s.service.getSchedulableAccount(ctx, accountID)
	if err != nil || account == nil {
		_ = s.service.deleteStickySessionAccountID(ctx, req.GroupID, sessionHash)
		return nil, false, false, 0, "", nil
	}
	if shouldClearStickySession(account, req.RequestedModel) || account.Platform != normalizeOpenAICompatiblePlatform(req.Platform) || !account.IsOpenAICompatible() || !account.IsSchedulable() {
		_ = s.service.deleteStickySessionAccountID(ctx, req.GroupID, sessionHash)
		return nil, false, false, 0, "", nil
	}
	if !s.isAccountRequestCompatible(ctx, account, req) {
		return nil, false, false, 0, "", nil
	}
	if !s.isAccountTransportCompatible(account, req.RequiredTransport) {
		_ = s.service.deleteStickySessionAccountID(ctx, req.GroupID, sessionHash)
		return nil, false, false, 0, "", nil
	}
	account = s.service.recheckSelectedOpenAIAccountFromDB(ctx, account, req.Platform, req.RequestedModel, req.RequireCompact, req.RequiredCapability)
	if account == nil || !openAIStickyAccountMatchesGroup(account, req.GroupID) || !s.isAccountTransportCompatible(account, req.RequiredTransport) {
		_ = s.service.deleteStickySessionAccountID(ctx, req.GroupID, sessionHash)
		return nil, false, false, 0, "", nil
	}
	failbackCfg := s.service.openAIStickyPreferHigherPriorityConfig(ctx)
	if failbackCfg.stickyEnabled &&
		s.service.allowOpenAIStickyFailbackAttempt("session_hash", req.GroupID, sessionHash, failbackCfg.minInterval) {
		failbackSelection, reason, failbackErr := s.service.tryAcquireHigherPriorityOpenAIAccount(ctx, req, account)
		if failbackErr != nil {
			return nil, false, false, 0, "", failbackErr
		}
		if failbackSelection != nil && failbackSelection.Account != nil {
			slog.Info("openai.sticky_session_failback",
				"previous_account_id", account.ID,
				"account_id", failbackSelection.Account.ID,
				"reason", reason,
			)
			return failbackSelection, false, true, account.ID, reason, nil
		}
	} else if failbackCfg.stickyEnabled {
		s.service.logOpenAIStickyFailbackSkipped(req, "session_hash", account.ID, 0, "attempt_interval")
	}
	escapeCfg := s.service.openAIStickyEscapeConfig()
	if reason, errorRate, ttft, shouldEscape := s.shouldEscapeStickyAccount(accountID, escapeCfg); shouldEscape {
		attrs := []any{
			"account_id", accountID,
			"reason", reason,
			"error_rate", errorRate,
			"ttft", ttft,
		}
		if snapshot, marked := s.service.isOpenAIAccountMarkedSlow(accountID, time.Now()); marked {
			attrs = append(attrs, "slow_until", snapshot.slowUntil)
		}
		slog.Info("openai.sticky_escape_triggered", attrs...)
		return nil, true, false, 0, "", nil
	}
	result, acquireErr := s.service.tryAcquireAccountSlot(ctx, accountID, account.Concurrency)
	if acquireErr == nil && result != nil && result.Acquired {
		_ = s.service.refreshStickySessionTTL(ctx, req.GroupID, sessionHash, s.service.openAIWSSessionStickyTTL())
		return &AccountSelectionResult{
			Account:     account,
			Acquired:    true,
			ReleaseFunc: result.ReleaseFunc,
		}, false, false, 0, "", nil
	}

	cfg := s.service.schedulingConfig()
	// WaitPlan.MaxConcurrency 使用 Concurrency（非 EffectiveLoadFactor），因为 WaitPlan 控制的是 Redis 实际并发槽位等待。
	if s.service.concurrencyService != nil {
		if escapeCfg.enabled && acquireErr == nil && result != nil && !result.Acquired {
			errorRate, ttft, _ := s.stats.snapshot(accountID)
			slog.Info("openai.sticky_escape_triggered",
				"account_id", accountID,
				"reason", "concurrency_full",
				"error_rate", errorRate,
				"ttft", ttft,
			)
			return nil, true, false, 0, "", nil
		}
		return &AccountSelectionResult{
			Account: account,
			WaitPlan: &AccountWaitPlan{
				AccountID:      accountID,
				MaxConcurrency: account.Concurrency,
				Timeout:        cfg.StickySessionWaitTimeout,
				MaxWaiting:     cfg.StickySessionMaxWaiting,
			},
		}, false, false, 0, "", nil
	}
	return nil, false, false, 0, "", nil
}

func openAIStickyAccountMatchesGroup(account *Account, groupID *int64) bool {
	if account == nil {
		return false
	}
	if groupID == nil {
		return len(account.AccountGroups) == 0 && len(account.GroupIDs) == 0
	}
	for _, accountGroupID := range account.GroupIDs {
		if accountGroupID == *groupID {
			return true
		}
	}
	for _, accountGroup := range account.AccountGroups {
		if accountGroup.GroupID == *groupID {
			return true
		}
	}
	return false
}

func (s *defaultOpenAIAccountScheduler) shouldEscapeStickyAccount(accountID int64, cfg openAIStickyEscapeConfig) (reason string, errorRate float64, ttft float64, shouldEscape bool) {
	if !cfg.enabled || s == nil || s.stats == nil || accountID <= 0 {
		return "", 0, 0, false
	}
	errorRate, ttft, hasTTFT := s.stats.snapshot(accountID)
	if s.service != nil {
		slowCfg := s.service.openAISlowAccountConfig()
		if slowCfg.enabled && s.stats.slowSnapshot(accountID, time.Now()).marked {
			return "slow_ttft", errorRate, ttft, true
		}
	}
	if hasTTFT && ttft > cfg.ttftMs {
		return "ttft", errorRate, ttft, true
	}
	if errorRate > cfg.errorRate {
		return "error_rate", errorRate, ttft, true
	}
	return "", errorRate, ttft, false
}

func (s *OpenAIGatewayService) isOpenAIAccountMarkedSlow(accountID int64, now time.Time) (openAIAccountSlowSnapshot, bool) {
	if s == nil || accountID <= 0 || s.openaiAccountStats == nil {
		return openAIAccountSlowSnapshot{}, false
	}
	cfg := s.openAISlowAccountConfig()
	if !cfg.enabled {
		return openAIAccountSlowSnapshot{}, false
	}
	snapshot := s.openaiAccountStats.slowSnapshot(accountID, now)
	return snapshot, snapshot.marked && snapshot.slowScore >= int64(cfg.markScore)
}

func openAIAccountSlowSkip(snapshot openAIAccountSlowSnapshot, cfg openAISlowAccountConfig) bool {
	return cfg.enabled && snapshot.marked && snapshot.slowScore >= int64(cfg.skipScore)
}

func openAIAccountSlowPenalty(snapshot openAIAccountSlowSnapshot, cfg openAISlowAccountConfig) float64 {
	if !cfg.enabled || cfg.penaltyWeight <= 0 || snapshot.slowScore <= 0 {
		return 0
	}
	if snapshot.slowScore >= int64(cfg.markScore) && !snapshot.marked {
		return 0
	}
	if snapshot.slowScore < int64(cfg.markScore) {
		return cfg.penaltyWeight * 0.25 * float64(snapshot.slowScore) / float64(cfg.markScore)
	}
	return cfg.penaltyWeight * float64(snapshot.slowScore) / float64(cfg.maxScore)
}

func (s *OpenAIGatewayService) logOpenAISlowAccountSkipped(req OpenAIAccountScheduleRequest, accountID int64, selectedAlternativeID int64, snapshot openAIAccountSlowSnapshot) {
	if s == nil || accountID <= 0 {
		return
	}
	slog.Info("openai.slow_account_skipped",
		"account_id", accountID,
		"selected_alternative_account_id", selectedAlternativeID,
		"group_id", derefGroupID(req.GroupID),
		"model", req.RequestedModel,
		"slow_until", snapshot.slowUntil,
		"slow_streak", snapshot.slowStreak,
		"slow_score", snapshot.slowScore,
		"sample_count", snapshot.sampleCount,
	)
}

func (s *defaultOpenAIAccountScheduler) filterCircuitOpenOpenAIAccountCandidatesIfAlternativesExist(
	req OpenAIAccountScheduleRequest,
	candidates []openAIAccountCandidateScore,
	now time.Time,
) []openAIAccountCandidateScore {
	if s == nil || s.service == nil || len(candidates) <= 1 {
		return candidates
	}
	cfg := s.service.openAISlowAccountConfig()
	if !cfg.enabled || s.stats == nil {
		return candidates
	}
	nonSlow := make([]openAIAccountCandidateScore, 0, len(candidates))
	type slowCandidate struct {
		candidate openAIAccountCandidateScore
		snapshot  openAIAccountSlowSnapshot
	}
	slow := make([]slowCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.account == nil {
			continue
		}
		snapshot := s.stats.slowSnapshot(candidate.account.ID, now)
		if openAIAccountSlowSkip(snapshot, cfg) {
			candidate.slow = true
			candidate.slowUntil = snapshot.slowUntil
			candidate.slowScore = snapshot.slowScore
			slow = append(slow, slowCandidate{candidate: candidate, snapshot: snapshot})
			continue
		}
		nonSlow = append(nonSlow, candidate)
	}
	if len(nonSlow) == 0 || len(slow) == 0 {
		return candidates
	}
	selectedAlternativeID := int64(0)
	if nonSlow[0].account != nil {
		selectedAlternativeID = nonSlow[0].account.ID
	}
	for _, item := range slow {
		s.service.logOpenAISlowAccountSkipped(req, item.candidate.account.ID, selectedAlternativeID, item.snapshot)
	}
	return nonSlow
}

func (s *OpenAIGatewayService) filterCircuitOpenOpenAIAccountsIfAlternativesExist(
	req OpenAIAccountScheduleRequest,
	accounts []*Account,
	now time.Time,
) []*Account {
	if s == nil || len(accounts) <= 1 {
		return accounts
	}
	cfg := s.openAISlowAccountConfig()
	if !cfg.enabled || s.openaiAccountStats == nil {
		return accounts
	}
	nonSlow := make([]*Account, 0, len(accounts))
	type slowAccount struct {
		account  *Account
		snapshot openAIAccountSlowSnapshot
	}
	slow := make([]slowAccount, 0, len(accounts))
	for _, account := range accounts {
		if account == nil {
			continue
		}
		snapshot := s.openaiAccountStats.slowSnapshot(account.ID, now)
		if openAIAccountSlowSkip(snapshot, cfg) {
			slow = append(slow, slowAccount{account: account, snapshot: snapshot})
			continue
		}
		nonSlow = append(nonSlow, account)
	}
	if len(nonSlow) == 0 || len(slow) == 0 {
		return accounts
	}
	selectedAlternativeID := int64(0)
	if nonSlow[0] != nil {
		selectedAlternativeID = nonSlow[0].ID
	}
	for _, item := range slow {
		s.logOpenAISlowAccountSkipped(req, item.account.ID, selectedAlternativeID, item.snapshot)
	}
	return nonSlow
}

type openAIAccountCandidateScore struct {
	account   *Account
	loadInfo  *AccountLoadInfo
	score     float64
	errorRate float64
	ttft      float64
	hasTTFT   bool
	slow      bool
	slowUntil time.Time
	slowScore int64
}

type openAIAccountCandidateHeap []openAIAccountCandidateScore

func (h openAIAccountCandidateHeap) Len() int {
	return len(h)
}

func (h openAIAccountCandidateHeap) Less(i, j int) bool {
	// 最小堆根节点保存“最差”候选，便于 O(log k) 维护 topK。
	return isOpenAIAccountCandidateBetter(h[j], h[i])
}

func (h openAIAccountCandidateHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
}

func (h *openAIAccountCandidateHeap) Push(x any) {
	candidate, ok := x.(openAIAccountCandidateScore)
	if !ok {
		panic("openAIAccountCandidateHeap: invalid element type")
	}
	*h = append(*h, candidate)
}

func (h *openAIAccountCandidateHeap) Pop() any {
	old := *h
	n := len(old)
	last := old[n-1]
	*h = old[:n-1]
	return last
}

func isOpenAIAccountCandidateBetter(left openAIAccountCandidateScore, right openAIAccountCandidateScore) bool {
	if left.score != right.score {
		return left.score > right.score
	}
	if left.account.Priority != right.account.Priority {
		return left.account.Priority < right.account.Priority
	}
	if left.loadInfo.LoadRate != right.loadInfo.LoadRate {
		return left.loadInfo.LoadRate < right.loadInfo.LoadRate
	}
	if left.loadInfo.WaitingCount != right.loadInfo.WaitingCount {
		return left.loadInfo.WaitingCount < right.loadInfo.WaitingCount
	}
	return left.account.ID < right.account.ID
}

func selectTopKOpenAICandidates(candidates []openAIAccountCandidateScore, topK int) []openAIAccountCandidateScore {
	if len(candidates) == 0 {
		return nil
	}
	if topK <= 0 {
		topK = 1
	}
	if topK >= len(candidates) {
		ranked := append([]openAIAccountCandidateScore(nil), candidates...)
		sort.Slice(ranked, func(i, j int) bool {
			return isOpenAIAccountCandidateBetter(ranked[i], ranked[j])
		})
		return ranked
	}

	best := make(openAIAccountCandidateHeap, 0, topK)
	for _, candidate := range candidates {
		if len(best) < topK {
			heap.Push(&best, candidate)
			continue
		}
		if isOpenAIAccountCandidateBetter(candidate, best[0]) {
			best[0] = candidate
			heap.Fix(&best, 0)
		}
	}

	ranked := make([]openAIAccountCandidateScore, len(best))
	copy(ranked, best)
	sort.Slice(ranked, func(i, j int) bool {
		return isOpenAIAccountCandidateBetter(ranked[i], ranked[j])
	})
	return ranked
}

type openAISelectionRNG struct {
	state uint64
}

func newOpenAISelectionRNG(seed uint64) openAISelectionRNG {
	if seed == 0 {
		seed = 0x9e3779b97f4a7c15
	}
	return openAISelectionRNG{state: seed}
}

func (r *openAISelectionRNG) nextUint64() uint64 {
	// xorshift64*
	x := r.state
	x ^= x >> 12
	x ^= x << 25
	x ^= x >> 27
	r.state = x
	return x * 2685821657736338717
}

func (r *openAISelectionRNG) nextFloat64() float64 {
	// [0,1)
	return float64(r.nextUint64()>>11) / (1 << 53)
}

func deriveOpenAISelectionSeed(req OpenAIAccountScheduleRequest) uint64 {
	hasher := fnv.New64a()
	writeValue := func(value string) {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return
		}
		_, _ = hasher.Write([]byte(trimmed))
		_, _ = hasher.Write([]byte{0})
	}

	writeValue(req.SessionHash)
	writeValue(req.PreviousResponseID)
	writeValue(req.RequestedModel)
	if req.GroupID != nil {
		_, _ = hasher.Write([]byte(strconv.FormatInt(*req.GroupID, 10)))
	}

	seed := hasher.Sum64()
	// 对“无会话锚点”的纯负载均衡请求引入时间熵，避免固定命中同一账号。
	if strings.TrimSpace(req.SessionHash) == "" && strings.TrimSpace(req.PreviousResponseID) == "" {
		seed ^= uint64(time.Now().UnixNano())
	}
	if seed == 0 {
		seed = uint64(time.Now().UnixNano()) ^ 0x9e3779b97f4a7c15
	}
	return seed
}

func buildOpenAIWeightedSelectionOrder(
	candidates []openAIAccountCandidateScore,
	req OpenAIAccountScheduleRequest,
) []openAIAccountCandidateScore {
	if len(candidates) <= 1 {
		return append([]openAIAccountCandidateScore(nil), candidates...)
	}

	pool := append([]openAIAccountCandidateScore(nil), candidates...)
	weights := make([]float64, len(pool))
	minScore := pool[0].score
	for i := 1; i < len(pool); i++ {
		if pool[i].score < minScore {
			minScore = pool[i].score
		}
	}
	for i := range pool {
		// 将 top-K 分值平移到正区间，避免“单一最高分账号”长期垄断。
		weight := (pool[i].score - minScore) + 1.0
		if math.IsNaN(weight) || math.IsInf(weight, 0) || weight <= 0 {
			weight = 1.0
		}
		weights[i] = weight
	}

	order := make([]openAIAccountCandidateScore, 0, len(pool))
	rng := newOpenAISelectionRNG(deriveOpenAISelectionSeed(req))
	for len(pool) > 0 {
		total := 0.0
		for _, w := range weights {
			total += w
		}

		selectedIdx := 0
		if total > 0 {
			r := rng.nextFloat64() * total
			acc := 0.0
			for i, w := range weights {
				acc += w
				if r <= acc {
					selectedIdx = i
					break
				}
			}
		} else {
			selectedIdx = int(rng.nextUint64() % uint64(len(pool)))
		}

		order = append(order, pool[selectedIdx])
		pool = append(pool[:selectedIdx], pool[selectedIdx+1:]...)
		weights = append(weights[:selectedIdx], weights[selectedIdx+1:]...)
	}
	return order
}

func (s *defaultOpenAIAccountScheduler) buildOpenAIAccountLoadPlan(
	req OpenAIAccountScheduleRequest,
	filtered []*Account,
	loadMap map[int64]*AccountLoadInfo,
) openAIAccountLoadPlan {
	allCandidates := make([]openAIAccountCandidateScore, 0, len(filtered))
	now := time.Now()
	for _, account := range filtered {
		loadInfo := loadMap[account.ID]
		if loadInfo == nil {
			loadInfo = &AccountLoadInfo{AccountID: account.ID}
		}
		errorRate, ttft, hasTTFT := 0.0, 0.0, false
		slowSnapshot := openAIAccountSlowSnapshot{}
		if s.stats != nil {
			errorRate, ttft, hasTTFT = s.stats.snapshot(account.ID)
			slowSnapshot = s.stats.slowSnapshot(account.ID, now)
		}
		allCandidates = append(allCandidates, openAIAccountCandidateScore{
			account:   account,
			loadInfo:  loadInfo,
			errorRate: errorRate,
			ttft:      ttft,
			hasTTFT:   hasTTFT,
			slow:      slowSnapshot.marked,
			slowUntil: slowSnapshot.slowUntil,
			slowScore: slowSnapshot.slowScore,
		})
	}

	candidates := allCandidates
	staleSnapshotCompactRetry := make([]openAIAccountCandidateScore, 0, len(allCandidates))
	if req.RequireCompact {
		candidates = make([]openAIAccountCandidateScore, 0, len(allCandidates))
		for _, candidate := range allCandidates {
			if openAICompactSupportTier(candidate.account) == 0 {
				staleSnapshotCompactRetry = append(staleSnapshotCompactRetry, candidate)
				continue
			}
			candidates = append(candidates, candidate)
		}
	}
	candidates = s.filterCircuitOpenOpenAIAccountCandidatesIfAlternativesExist(req, candidates, now)

	plan := openAIAccountLoadPlan{
		allCandidates:             allCandidates,
		candidates:                candidates,
		staleSnapshotCompactRetry: staleSnapshotCompactRetry,
		candidateCount:            len(candidates),
	}
	if len(candidates) == 0 {
		plan.selectionOrder = s.buildOpenAISelectionOrder(req, plan)
		return plan
	}

	minPriority, maxPriority := candidates[0].account.Priority, candidates[0].account.Priority
	maxWaiting := 1
	loadRateSum := 0.0
	loadRateSumSquares := 0.0
	minTTFT, maxTTFT := 0.0, 0.0
	hasTTFTSample := false
	for _, candidate := range candidates {
		if candidate.account.Priority < minPriority {
			minPriority = candidate.account.Priority
		}
		if candidate.account.Priority > maxPriority {
			maxPriority = candidate.account.Priority
		}
		if candidate.loadInfo.WaitingCount > maxWaiting {
			maxWaiting = candidate.loadInfo.WaitingCount
		}
		if candidate.hasTTFT && candidate.ttft > 0 {
			if !hasTTFTSample {
				minTTFT, maxTTFT = candidate.ttft, candidate.ttft
				hasTTFTSample = true
			} else {
				if candidate.ttft < minTTFT {
					minTTFT = candidate.ttft
				}
				if candidate.ttft > maxTTFT {
					maxTTFT = candidate.ttft
				}
			}
		}
		loadRate := float64(candidate.loadInfo.LoadRate)
		loadRateSum += loadRate
		loadRateSumSquares += loadRate * loadRate
	}
	plan.loadSkew = calcLoadSkewByMoments(loadRateSum, loadRateSumSquares, len(candidates))

	weights := s.service.openAIWSSchedulerWeights()

	// Reset 因子（use-it-or-lose-it）：在拥有「未来会话窗口结束时间」的账号中，
	// 剩余时间越短 → 因子越接近 1（越早重置越优先用尽）。无活跃窗口的账号因子为 0。
	// 仅在 weights.Reset > 0 时计算，默认关闭不影响原有行为。
	minResetRemaining, maxResetRemaining := 0.0, 0.0
	hasResetSample := false
	if weights.Reset > 0 {
		now := time.Now()
		for _, candidate := range candidates {
			end := candidate.account.SessionWindowEnd
			if end == nil || !now.Before(*end) {
				continue
			}
			remaining := end.Sub(now).Seconds()
			if !hasResetSample {
				minResetRemaining, maxResetRemaining = remaining, remaining
				hasResetSample = true
				continue
			}
			if remaining < minResetRemaining {
				minResetRemaining = remaining
			}
			if remaining > maxResetRemaining {
				maxResetRemaining = remaining
			}
		}
	}

	now = time.Now()
	for i := range candidates {
		item := &candidates[i]
		priorityFactor := 1.0
		if maxPriority > minPriority {
			priorityFactor = 1 - float64(item.account.Priority-minPriority)/float64(maxPriority-minPriority)
		}
		loadFactor := 1 - clamp01(float64(item.loadInfo.LoadRate)/100.0)
		queueFactor := 1 - clamp01(float64(item.loadInfo.WaitingCount)/float64(maxWaiting))
		errorFactor := 1 - clamp01(item.errorRate)
		ttftFactor := 0.5
		if item.hasTTFT && hasTTFTSample && maxTTFT > minTTFT {
			ttftFactor = 1 - clamp01((item.ttft-minTTFT)/(maxTTFT-minTTFT))
		}
		resetFactor := 0.0
		if weights.Reset > 0 && hasResetSample {
			if end := item.account.SessionWindowEnd; end != nil && now.Before(*end) {
				if maxResetRemaining > minResetRemaining {
					resetFactor = 1 - clamp01((end.Sub(now).Seconds()-minResetRemaining)/(maxResetRemaining-minResetRemaining))
				} else {
					// 所有有窗口的账号剩余时间相同：一律给满分，让其优于无窗口账号。
					resetFactor = 1
				}
			}
		}
		quotaHeadroomFactor := 0.0
		if weights.QuotaHeadroom > 0 {
			quotaHeadroomFactor = openAIQuotaHeadroomFactor(item.account, now)
		}
		slowPenalty := 0.0
		if s.stats != nil {
			snapshot := s.stats.slowSnapshot(item.account.ID, now)
			slowPenalty = openAIAccountSlowPenalty(snapshot, s.service.openAISlowAccountConfig())
			item.slowScore = snapshot.slowScore
			item.slow = snapshot.marked
			item.slowUntil = snapshot.slowUntil
		}

		item.score = weights.Priority*priorityFactor +
			weights.Load*loadFactor +
			weights.Queue*queueFactor +
			weights.ErrorRate*errorFactor +
			weights.TTFT*ttftFactor +
			weights.Reset*resetFactor +
			weights.QuotaHeadroom*quotaHeadroomFactor -
			slowPenalty
	}
	plan.candidates = candidates

	plan.topK = s.service.openAIWSLBTopK()
	if plan.topK > len(candidates) {
		plan.topK = len(candidates)
	}
	if plan.topK <= 0 {
		plan.topK = 1
	}

	plan.selectionOrder = s.buildOpenAISelectionOrder(req, plan)
	return plan
}

func (s *defaultOpenAIAccountScheduler) buildOpenAISelectionOrder(
	req OpenAIAccountScheduleRequest,
	plan openAIAccountLoadPlan,
) []openAIAccountCandidateScore {
	buildSelectionOrder := func(pool []openAIAccountCandidateScore) []openAIAccountCandidateScore {
		if len(pool) == 0 || plan.topK <= 0 {
			return nil
		}
		groupTopK := plan.topK
		if groupTopK > len(pool) {
			groupTopK = len(pool)
		}
		ranked := selectTopKOpenAICandidates(pool, groupTopK)
		return buildOpenAIWeightedSelectionOrder(ranked, req)
	}

	if req.RequireCompact {
		supported := make([]openAIAccountCandidateScore, 0, len(plan.candidates))
		unknown := make([]openAIAccountCandidateScore, 0, len(plan.candidates))
		for _, candidate := range plan.candidates {
			switch openAICompactSupportTier(candidate.account) {
			case 2:
				supported = append(supported, candidate)
			case 1:
				unknown = append(unknown, candidate)
			}
		}
		selectionOrder := make([]openAIAccountCandidateScore, 0, len(plan.allCandidates))
		selectionOrder = append(selectionOrder, buildSelectionOrder(supported)...)
		selectionOrder = append(selectionOrder, buildSelectionOrder(unknown)...)
		if len(plan.staleSnapshotCompactRetry) > 0 && s.service.schedulerSnapshot != nil {
			selectionOrder = append(selectionOrder, sortOpenAICompactRetryCandidates(plan.staleSnapshotCompactRetry)...)
		}
		return selectionOrder
	}

	return buildSelectionOrder(plan.candidates)
}

func sortOpenAICompactRetryCandidates(pool []openAIAccountCandidateScore) []openAIAccountCandidateScore {
	if len(pool) == 0 {
		return nil
	}
	ordered := append([]openAIAccountCandidateScore(nil), pool...)
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if a.account.Priority != b.account.Priority {
			return a.account.Priority < b.account.Priority
		}
		if a.loadInfo.LoadRate != b.loadInfo.LoadRate {
			return a.loadInfo.LoadRate < b.loadInfo.LoadRate
		}
		if a.loadInfo.WaitingCount != b.loadInfo.WaitingCount {
			return a.loadInfo.WaitingCount < b.loadInfo.WaitingCount
		}
		switch {
		case a.account.LastUsedAt == nil && b.account.LastUsedAt != nil:
			return true
		case a.account.LastUsedAt != nil && b.account.LastUsedAt == nil:
			return false
		case a.account.LastUsedAt == nil && b.account.LastUsedAt == nil:
			return false
		default:
			return a.account.LastUsedAt.Before(*b.account.LastUsedAt)
		}
	})
	return ordered
}

func (s *defaultOpenAIAccountScheduler) tryAcquireOpenAISelectionOrder(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
	selectionOrder []openAIAccountCandidateScore,
) (*AccountSelectionResult, bool, error) {
	compactBlocked := false
	for i := 0; i < len(selectionOrder); i++ {
		candidate := selectionOrder[i]
		fresh := s.service.resolveFreshSchedulableOpenAIAccount(ctx, candidate.account, req.Platform, req.RequestedModel, false, req.RequiredCapability)
		if fresh == nil || !s.isAccountTransportCompatible(fresh, req.RequiredTransport) || !s.isAccountRequestCompatible(ctx, fresh, req) {
			continue
		}
		fresh = s.service.recheckSelectedOpenAIAccountFromDB(ctx, fresh, req.Platform, req.RequestedModel, false, req.RequiredCapability)
		if fresh == nil || !s.isAccountTransportCompatible(fresh, req.RequiredTransport) || !s.isAccountRequestCompatible(ctx, fresh, req) {
			continue
		}
		if req.RequireCompact && openAICompactSupportTier(fresh) == 0 {
			compactBlocked = true
			continue
		}
		result, acquireErr := s.service.tryAcquireAccountSlot(ctx, fresh.ID, fresh.Concurrency)
		if acquireErr != nil {
			return nil, compactBlocked, acquireErr
		}
		if result != nil && result.Acquired {
			if req.SessionHash != "" && !req.PreserveStickyBinding {
				_ = s.service.BindStickySession(ctx, req.GroupID, req.SessionHash, fresh.ID)
			}
			return &AccountSelectionResult{
				Account:     fresh,
				Acquired:    true,
				ReleaseFunc: result.ReleaseFunc,
			}, compactBlocked, nil
		}
	}
	return nil, compactBlocked, nil
}

func (s *defaultOpenAIAccountScheduler) selectByLoadBalance(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
) (*AccountSelectionResult, int, int, float64, error) {
	accounts, err := s.service.listSchedulableAccounts(ctx, req.GroupID, req.Platform)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	if len(accounts) == 0 {
		return nil, 0, 0, 0, noAvailableOpenAISelectionError(req.RequestedModel, false)
	}

	// require_privacy_set: 获取分组信息
	var schedGroup *Group
	if req.GroupID != nil && s.service.schedulerSnapshot != nil {
		schedGroup, _ = s.service.schedulerSnapshot.GetGroupByID(ctx, *req.GroupID)
	}

	filtered := make([]*Account, 0, len(accounts))
	loadReq := make([]AccountWithConcurrency, 0, len(accounts))
	for i := range accounts {
		account := &accounts[i]
		if req.ExcludedIDs != nil {
			if _, excluded := req.ExcludedIDs[account.ID]; excluded {
				continue
			}
		}
		if !account.IsSchedulable() || account.Platform != normalizeOpenAICompatiblePlatform(req.Platform) || !account.IsOpenAICompatible() {
			continue
		}
		if s.service.isOpenAIAccountRuntimeBlocked(account) {
			continue
		}
		// require_privacy_set: 跳过 privacy 未设置的账号并标记异常
		if schedGroup != nil && schedGroup.RequirePrivacySet && !account.IsPrivacySet() {
			s.service.BlockAccountScheduling(account, time.Time{}, "privacy_not_set")
			_ = s.service.accountRepo.SetError(ctx, account.ID,
				fmt.Sprintf("Privacy not set, required by group [%s]", schedGroup.Name))
			continue
		}
		if !s.isAccountRequestCompatible(ctx, account, req) {
			continue
		}
		if !s.isAccountTransportCompatible(account, req.RequiredTransport) {
			continue
		}
		filtered = append(filtered, account)
		loadReq = append(loadReq, AccountWithConcurrency{
			ID:             account.ID,
			MaxConcurrency: account.EffectiveLoadFactor(),
		})
	}
	if len(filtered) == 0 {
		return nil, 0, 0, 0, noAvailableOpenAISelectionError(req.RequestedModel, false)
	}

	loadMap := map[int64]*AccountLoadInfo{}
	if s.service.concurrencyService != nil {
		if batchLoad, loadErr := s.service.concurrencyService.GetAccountsLoadBatch(ctx, loadReq); loadErr == nil {
			loadMap = batchLoad
		}
	}

	plan := s.buildOpenAIAccountLoadPlan(req, filtered, loadMap)
	candidateCount := plan.candidateCount
	topK := plan.topK
	loadSkew := plan.loadSkew
	selectionOrder := plan.selectionOrder
	if req.RequireCompact && len(plan.candidates) == 0 && len(plan.staleSnapshotCompactRetry) == 0 {
		return nil, 0, 0, 0, ErrNoAvailableCompactAccounts
	}
	if req.RequireCompact && len(selectionOrder) == 0 && s.service.schedulerSnapshot == nil {
		return nil, candidateCount, topK, loadSkew, ErrNoAvailableCompactAccounts
	}
	if len(selectionOrder) == 0 {
		return nil, candidateCount, topK, loadSkew, noAvailableOpenAISelectionError(req.RequestedModel, req.RequireCompact && len(plan.allCandidates) > 0)
	}

	result, compactBlocked, acquireErr := s.tryAcquireOpenAISelectionOrder(ctx, req, selectionOrder)
	if acquireErr != nil {
		return nil, candidateCount, topK, loadSkew, acquireErr
	}
	if result != nil {
		return result, candidateCount, topK, loadSkew, nil
	}

	if s.service.concurrencyService != nil {
		if freshLoadMap, loadErr := s.service.concurrencyService.GetAccountsLoadBatchFresh(ctx, loadReq); loadErr == nil {
			freshPlan := s.buildOpenAIAccountLoadPlan(req, filtered, freshLoadMap)
			if len(freshPlan.selectionOrder) > 0 {
				freshResult, freshCompactBlocked, freshAcquireErr := s.tryAcquireOpenAISelectionOrder(ctx, req, freshPlan.selectionOrder)
				if freshAcquireErr != nil {
					return nil, candidateCount, topK, loadSkew, freshAcquireErr
				}
				if freshResult != nil {
					return freshResult, freshPlan.candidateCount, freshPlan.topK, freshPlan.loadSkew, nil
				}
				compactBlocked = compactBlocked || freshCompactBlocked
				selectionOrder = freshPlan.selectionOrder
				candidateCount = freshPlan.candidateCount
				topK = freshPlan.topK
				loadSkew = freshPlan.loadSkew
			}
		}
	}

	cfg := s.service.schedulingConfig()
	// WaitPlan.MaxConcurrency 使用 Concurrency（非 EffectiveLoadFactor），因为 WaitPlan 控制的是 Redis 实际并发槽位等待。
	for _, candidate := range selectionOrder {
		fresh := s.service.resolveFreshSchedulableOpenAIAccount(ctx, candidate.account, req.Platform, req.RequestedModel, false, req.RequiredCapability)
		if fresh == nil || !s.isAccountTransportCompatible(fresh, req.RequiredTransport) || !s.isAccountRequestCompatible(ctx, fresh, req) {
			continue
		}
		fresh = s.service.recheckSelectedOpenAIAccountFromDB(ctx, fresh, req.Platform, req.RequestedModel, false, req.RequiredCapability)
		if fresh == nil || !s.isAccountTransportCompatible(fresh, req.RequiredTransport) || !s.isAccountRequestCompatible(ctx, fresh, req) {
			continue
		}
		if req.RequireCompact && openAICompactSupportTier(fresh) == 0 {
			compactBlocked = true
			continue
		}
		return &AccountSelectionResult{
			Account: fresh,
			WaitPlan: &AccountWaitPlan{
				AccountID:      fresh.ID,
				MaxConcurrency: fresh.Concurrency,
				Timeout:        cfg.FallbackWaitTimeout,
				MaxWaiting:     cfg.FallbackMaxWaiting,
			},
		}, candidateCount, topK, loadSkew, nil
	}

	return nil, candidateCount, topK, loadSkew, noAvailableOpenAISelectionError(req.RequestedModel, compactBlocked)
}

func (s *OpenAIGatewayService) tryAcquireHigherPriorityOpenAIAccount(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
	current *Account,
) (*AccountSelectionResult, string, error) {
	if s == nil || current == nil {
		return nil, "", nil
	}
	accounts, err := s.listSchedulableAccounts(ctx, req.GroupID, req.Platform)
	if err != nil {
		return nil, "", err
	}
	if len(accounts) == 0 {
		return nil, "", nil
	}

	failbackCfg := s.openAIStickyPreferHigherPriorityConfig(ctx)
	platform := normalizeOpenAICompatiblePlatform(req.Platform)
	needsUpstreamCheck := s.needsUpstreamChannelRestrictionCheck(ctx, req.GroupID)
	candidates := make([]accountWithLoad, 0, len(accounts))
	loadReq := make([]AccountWithConcurrency, 0, len(accounts))
	parentCache := make(map[int64]*Account)
	parentLookup := func(id int64) *Account {
		if account, ok := parentCache[id]; ok {
			return account
		}
		if s.accountRepo == nil {
			return nil
		}
		account, _ := s.accountRepo.GetByID(ctx, id)
		parentCache[id] = account
		return account
	}

	for i := range accounts {
		account := &accounts[i]
		if account.ID == current.ID || account.Priority >= current.Priority {
			continue
		}
		if req.ExcludedIDs != nil {
			if _, excluded := req.ExcludedIDs[account.ID]; excluded {
				continue
			}
		}
		if !isOpenAICompatibleAccountEligibleForRequest(ctx, account, platform, req.RequestedModel, req.RequireCompact, req.RequiredCapability) {
			continue
		}
		if !accountSupportsOpenAICapabilities(account, req.RequiredCapability, req.RequiredImageCapability) {
			continue
		}
		if !s.isOpenAIAccountTransportCompatible(account, req.RequiredTransport) {
			continue
		}
		if !parentHealthyForShadow(account, parentLookup) {
			continue
		}
		if s.isOpenAIAccountRuntimeBlocked(account) {
			continue
		}
		if snapshot, marked := s.isOpenAIAccountMarkedSlow(account.ID, time.Now()); marked {
			s.logOpenAIStickyFailbackSkipped(req, "higher_priority", current.ID, account.ID, "slow_ttft",
				slog.Time("slow_until", snapshot.slowUntil),
			)
			continue
		}
		if needsUpstreamCheck && req.GroupID != nil &&
			s.isUpstreamModelRestrictedByChannel(ctx, *req.GroupID, account, req.RequestedModel, req.RequireCompact) {
			continue
		}
		candidates = append(candidates, accountWithLoad{
			account:  account,
			loadInfo: &AccountLoadInfo{AccountID: account.ID},
		})
		loadReq = append(loadReq, AccountWithConcurrency{
			ID:             account.ID,
			MaxConcurrency: account.EffectiveLoadFactor(),
		})
	}
	if len(candidates) == 0 {
		return nil, "", nil
	}

	if s.concurrencyService != nil {
		if loadMap, loadErr := s.concurrencyService.GetAccountsLoadBatch(ctx, loadReq); loadErr == nil {
			for i := range candidates {
				if loadInfo := loadMap[candidates[i].account.ID]; loadInfo != nil {
					candidates[i].loadInfo = loadInfo
				}
			}
		}
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.account.Priority != b.account.Priority {
			return a.account.Priority < b.account.Priority
		}
		if a.loadInfo.LoadRate != b.loadInfo.LoadRate {
			return a.loadInfo.LoadRate < b.loadInfo.LoadRate
		}
		if a.loadInfo.WaitingCount != b.loadInfo.WaitingCount {
			return a.loadInfo.WaitingCount < b.loadInfo.WaitingCount
		}
		switch {
		case a.account.LastUsedAt == nil && b.account.LastUsedAt != nil:
			return true
		case a.account.LastUsedAt != nil && b.account.LastUsedAt == nil:
			return false
		case a.account.LastUsedAt == nil && b.account.LastUsedAt == nil:
			return a.account.ID < b.account.ID
		default:
			if a.account.LastUsedAt.Equal(*b.account.LastUsedAt) {
				return a.account.ID < b.account.ID
			}
			return a.account.LastUsedAt.Before(*b.account.LastUsedAt)
		}
	})

	for _, candidate := range candidates {
		if until, cooling := s.openAIStickyFailbackAccountCooldown(candidate.account.ID); cooling {
			s.logOpenAIStickyFailbackSkipped(req, "higher_priority", current.ID, candidate.account.ID, "failback_cooldown",
				slog.Time("cooldown_until", until),
			)
			continue
		}
		fresh := s.resolveFreshSchedulableOpenAIAccount(ctx, candidate.account, platform, req.RequestedModel, req.RequireCompact, req.RequiredCapability)
		if fresh == nil {
			continue
		}
		fresh = s.recheckSelectedOpenAIAccountFromDB(ctx, fresh, platform, req.RequestedModel, req.RequireCompact, req.RequiredCapability)
		if fresh == nil {
			continue
		}
		if !accountSupportsOpenAICapabilities(fresh, req.RequiredCapability, req.RequiredImageCapability) {
			continue
		}
		if !s.isOpenAIAccountTransportCompatible(fresh, req.RequiredTransport) {
			continue
		}
		if needsUpstreamCheck && req.GroupID != nil &&
			s.isUpstreamModelRestrictedByChannel(ctx, *req.GroupID, fresh, req.RequestedModel, req.RequireCompact) {
			continue
		}
		if probe := s.probeOpenAIStickyFailbackCandidate(ctx, req, fresh, failbackCfg); !probe.Healthy {
			args := []slog.Attr{
				slog.String("probe_reason", probe.Reason),
			}
			if probe.StatusCode > 0 {
				args = append(args, slog.Int("probe_status", probe.StatusCode))
			}
			if probe.Err != nil {
				args = append(args, slog.String("probe_error", probe.Err.Error()))
			}
			s.logOpenAIStickyFailbackSkipped(req, "higher_priority", current.ID, fresh.ID, "failback_probe_unhealthy", args...)
			continue
		}
		result, acquireErr := s.tryAcquireAccountSlot(ctx, fresh.ID, fresh.Concurrency)
		if acquireErr != nil {
			return nil, "", acquireErr
		}
		if result == nil || !result.Acquired {
			s.logOpenAIStickyFailbackSkipped(req, "higher_priority", current.ID, fresh.ID, "higher_priority_busy")
			continue
		}
		selection, selectErr := s.newAcquiredSelectionResult(ctx, fresh, result.ReleaseFunc)
		if selectErr != nil {
			if result.ReleaseFunc != nil {
				result.ReleaseFunc()
			}
			return nil, "", selectErr
		}
		if req.SessionHash != "" && !req.PreserveStickyBinding {
			_ = s.BindStickySession(ctx, req.GroupID, req.SessionHash, fresh.ID)
		}
		return selection, "higher_priority_available", nil
	}

	s.logOpenAIStickyFailbackSkipped(req, "higher_priority", current.ID, 0, "no_eligible_higher_priority_account")
	return nil, "", nil
}

func (s *defaultOpenAIAccountScheduler) isAccountTransportCompatible(account *Account, requiredTransport OpenAIUpstreamTransport) bool {
	if requiredTransport == OpenAIUpstreamTransportAny || requiredTransport == OpenAIUpstreamTransportHTTPSSE {
		return true
	}
	if s == nil || s.service == nil {
		return false
	}
	return s.service.isOpenAIAccountTransportCompatible(account, requiredTransport)
}

func (s *defaultOpenAIAccountScheduler) lookupShadowParentAccount(ctx context.Context, id int64) *Account {
	if s == nil || s.service == nil {
		return nil
	}
	if s.service.schedulerSnapshot != nil {
		if account, err := s.service.schedulerSnapshot.GetAccount(ctx, id); err == nil && account != nil {
			return account
		}
	}
	if s.service.accountRepo == nil {
		return nil
	}
	account, _ := s.service.accountRepo.GetByID(ctx, id)
	return account
}

func (s *defaultOpenAIAccountScheduler) isAccountRequestCompatible(ctx context.Context, account *Account, req OpenAIAccountScheduleRequest) bool {
	if account == nil {
		return false
	}
	if s != nil && s.service != nil && s.service.isOpenAIAccountRuntimeBlocked(account) {
		return false
	}
	// Quota auto-pause must be evaluated during the initial filter too. Without it the
	// TopK candidate pool can be filled with paused accounts and the later fresh/DB
	// rechecks won't reach healthy accounts that fell outside TopK — manifesting as
	// "no available accounts" even though healthy ones exist.
	if paused, _ := shouldAutoPauseOpenAIAccountByQuota(ctx, account); paused {
		return false
	}
	// 母账号健康联动：影子账号的凭据来自母账号，母账号不可调度时影子也不应被选中。
	// Parent-health gate: shadow borrows the parent's credentials; an unschedulable
	// parent must block the shadow across all scheduler paths.
	if !parentHealthyForShadow(account, func(id int64) *Account {
		return s.lookupShadowParentAccount(ctx, id)
	}) {
		return false
	}
	if req.RequestedModel != "" && !account.IsModelSupported(req.RequestedModel) {
		return false
	}
	if req.GroupID != nil && s != nil && s.service != nil &&
		s.service.needsUpstreamChannelRestrictionCheck(ctx, req.GroupID) &&
		s.service.isUpstreamModelRestrictedByChannel(ctx, *req.GroupID, account, req.RequestedModel, req.RequireCompact) {
		return false
	}
	return accountSupportsOpenAICapabilities(account, req.RequiredCapability, req.RequiredImageCapability)
}

func (s *defaultOpenAIAccountScheduler) ReportResult(accountID int64, success bool, firstTokenMs *int) {
	if s == nil || s.stats == nil {
		return
	}
	cfg := openAISlowAccountConfig{}
	if s.service != nil {
		cfg = s.service.openAISlowAccountConfig()
	}
	report := s.stats.report(accountID, success, firstTokenMs, cfg)
	if s.service != nil {
		s.service.logOpenAIAccountSlowStateChange(accountID, report, cfg)
	}
}

func (s *defaultOpenAIAccountScheduler) ReportSwitch() {
	if s == nil {
		return
	}
	s.metrics.recordSwitch()
}

func (s *defaultOpenAIAccountScheduler) SnapshotMetrics() OpenAIAccountSchedulerMetricsSnapshot {
	if s == nil {
		return OpenAIAccountSchedulerMetricsSnapshot{}
	}

	selectTotal := s.metrics.selectTotal.Load()
	prevHit := s.metrics.stickyPreviousHitTotal.Load()
	sessionHit := s.metrics.stickySessionHitTotal.Load()
	switchTotal := s.metrics.accountSwitchTotal.Load()
	latencyTotal := s.metrics.latencyMsTotal.Load()
	loadSkewTotal := s.metrics.loadSkewMilliTotal.Load()

	snapshot := OpenAIAccountSchedulerMetricsSnapshot{
		SelectTotal:              selectTotal,
		StickyPreviousHitTotal:   prevHit,
		StickySessionHitTotal:    sessionHit,
		LoadBalanceSelectTotal:   s.metrics.loadBalanceSelectTotal.Load(),
		AccountSwitchTotal:       switchTotal,
		SchedulerLatencyMsTotal:  latencyTotal,
		RuntimeStatsAccountCount: s.stats.size(),
	}
	if selectTotal > 0 {
		snapshot.SchedulerLatencyMsAvg = float64(latencyTotal) / float64(selectTotal)
		snapshot.StickyHitRatio = float64(prevHit+sessionHit) / float64(selectTotal)
		snapshot.AccountSwitchRate = float64(switchTotal) / float64(selectTotal)
		snapshot.LoadSkewAvg = float64(loadSkewTotal) / 1000 / float64(selectTotal)
	}
	return snapshot
}

func (s *OpenAIGatewayService) openAIAdvancedSchedulerSettingRepo() SettingRepository {
	if s == nil || s.rateLimitService == nil || s.rateLimitService.settingService == nil {
		return nil
	}
	return s.rateLimitService.settingService.settingRepo
}

func (s *OpenAIGatewayService) isOpenAIAdvancedSchedulerEnabled(ctx context.Context) bool {
	if cached, ok := openAIAdvancedSchedulerSettingCache.Load().(*cachedOpenAIAdvancedSchedulerSetting); ok && cached != nil {
		if time.Now().UnixNano() < cached.expiresAt {
			return cached.enabled
		}
	}

	result, _, _ := openAIAdvancedSchedulerSettingSF.Do(openAIAdvancedSchedulerSettingKey, func() (any, error) {
		if cached, ok := openAIAdvancedSchedulerSettingCache.Load().(*cachedOpenAIAdvancedSchedulerSetting); ok && cached != nil {
			if time.Now().UnixNano() < cached.expiresAt {
				return cached.enabled, nil
			}
		}

		enabled := false
		if repo := s.openAIAdvancedSchedulerSettingRepo(); repo != nil {
			dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), openAIAdvancedSchedulerSettingDBTimeout)
			defer cancel()

			value, err := repo.GetValue(dbCtx, openAIAdvancedSchedulerSettingKey)
			if err == nil {
				enabled = strings.EqualFold(strings.TrimSpace(value), "true")
			}
		}

		openAIAdvancedSchedulerSettingCache.Store(&cachedOpenAIAdvancedSchedulerSetting{
			enabled:   enabled,
			expiresAt: time.Now().Add(openAIAdvancedSchedulerSettingCacheTTL).UnixNano(),
		})
		return enabled, nil
	})

	enabled, _ := result.(bool)
	return enabled
}

func (s *OpenAIGatewayService) getOpenAIAccountScheduler(ctx context.Context) OpenAIAccountScheduler {
	if s == nil {
		return nil
	}
	if !s.isOpenAIAdvancedSchedulerEnabled(ctx) {
		return nil
	}
	s.openaiSchedulerOnce.Do(func() {
		stats := s.openAIAccountRuntimeStats()
		if s.openaiScheduler == nil {
			s.openaiScheduler = newDefaultOpenAIAccountScheduler(s, stats)
		}
	})
	return s.openaiScheduler
}

func (s *OpenAIGatewayService) openAIAccountRuntimeStats() *openAIAccountRuntimeStats {
	if s == nil {
		return nil
	}
	s.openaiAccountStatsOnce.Do(func() {
		if s.openaiAccountStats == nil {
			s.openaiAccountStats = newOpenAIAccountRuntimeStats()
		}
	})
	return s.openaiAccountStats
}

func resetOpenAIAdvancedSchedulerSettingCacheForTest() {
	openAIAdvancedSchedulerSettingCache = atomic.Value{}
	openAIAdvancedSchedulerSettingSF = singleflight.Group{}
	openAIStickyPreferHigherPrioritySettingCache = atomic.Value{}
	openAIStickyPreferHigherPrioritySettingSF = singleflight.Group{}
}

func (s *OpenAIGatewayService) SelectAccountWithScheduler(
	ctx context.Context,
	groupID *int64,
	previousResponseID string,
	sessionHash string,
	requestedModel string,
	excludedIDs map[int64]struct{},
	requiredTransport OpenAIUpstreamTransport,
	requireCompact bool,
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	return s.selectAccountWithScheduler(ctx, groupID, previousResponseID, sessionHash, requestedModel, excludedIDs, requiredTransport, "", "", requireCompact, PlatformOpenAI, OpenAIAccountScheduleOptions{})
}

func (s *OpenAIGatewayService) SelectAccountWithSchedulerForCapability(
	ctx context.Context,
	groupID *int64,
	previousResponseID string,
	sessionHash string,
	requestedModel string,
	excludedIDs map[int64]struct{},
	requiredTransport OpenAIUpstreamTransport,
	requiredCapability OpenAIEndpointCapability,
	requireCompact bool,
	platformOverride ...string,
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	return s.SelectAccountWithSchedulerForCapabilityWithOptions(
		ctx,
		groupID,
		previousResponseID,
		sessionHash,
		requestedModel,
		excludedIDs,
		requiredTransport,
		requiredCapability,
		requireCompact,
		OpenAIAccountScheduleOptions{},
		platformOverride...,
	)
}

func (s *OpenAIGatewayService) SelectAccountWithSchedulerForCapabilityWithOptions(
	ctx context.Context,
	groupID *int64,
	previousResponseID string,
	sessionHash string,
	requestedModel string,
	excludedIDs map[int64]struct{},
	requiredTransport OpenAIUpstreamTransport,
	requiredCapability OpenAIEndpointCapability,
	requireCompact bool,
	options OpenAIAccountScheduleOptions,
	platformOverride ...string,
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	platform := PlatformOpenAI
	if len(platformOverride) > 0 {
		platform = platformOverride[0]
	}
	return s.selectAccountWithScheduler(ctx, groupID, previousResponseID, sessionHash, requestedModel, excludedIDs, requiredTransport, requiredCapability, "", requireCompact, platform, options)
}

func (s *OpenAIGatewayService) SelectAccountWithSchedulerForImages(
	ctx context.Context,
	groupID *int64,
	sessionHash string,
	requestedModel string,
	excludedIDs map[int64]struct{},
	requiredCapability OpenAIImagesCapability,
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	selection, decision, err := s.selectAccountWithScheduler(ctx, groupID, "", sessionHash, requestedModel, excludedIDs, OpenAIUpstreamTransportHTTPSSE, "", requiredCapability, false, PlatformOpenAI, OpenAIAccountScheduleOptions{})
	if err == nil && selection != nil && selection.Account != nil {
		return selection, decision, nil
	}
	// 如果要求 native 能力（如指定了模型）但没有可用的 APIKey 账号，回退到 basic（OAuth 账号）
	if requiredCapability == OpenAIImagesCapabilityNative {
		return s.selectAccountWithScheduler(ctx, groupID, "", sessionHash, requestedModel, excludedIDs, OpenAIUpstreamTransportHTTPSSE, "", OpenAIImagesCapabilityBasic, false, PlatformOpenAI, OpenAIAccountScheduleOptions{})
	}
	return selection, decision, err
}

func (s *OpenAIGatewayService) selectAccountWithScheduler(
	ctx context.Context,
	groupID *int64,
	previousResponseID string,
	sessionHash string,
	requestedModel string,
	excludedIDs map[int64]struct{},
	requiredTransport OpenAIUpstreamTransport,
	requiredCapability OpenAIEndpointCapability,
	requiredImageCapability OpenAIImagesCapability,
	requireCompact bool,
	platform string,
	options OpenAIAccountScheduleOptions,
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	ctx = s.withOpenAIQuotaAutoPauseContext(ctx)
	platform = normalizeOpenAICompatiblePlatform(platform)
	decision := OpenAIAccountScheduleDecision{}
	scheduler := s.getOpenAIAccountScheduler(ctx)
	if scheduler == nil {
		decision.Layer = openAIAccountScheduleLayerLoadBalance
		if requiredTransport == OpenAIUpstreamTransportAny || requiredTransport == OpenAIUpstreamTransportHTTPSSE {
			effectiveExcludedIDs := cloneExcludedAccountIDs(excludedIDs)
			for {
				selection, err := s.selectAccountWithLoadAwareness(ctx, groupID, platform, sessionHash, requestedModel, effectiveExcludedIDs, requireCompact, requiredCapability, requiredImageCapability, requiredTransport)
				if err != nil {
					return nil, decision, err
				}
				if selection == nil || selection.Account == nil {
					return selection, decision, nil
				}
				if accountSupportsOpenAICapabilities(selection.Account, requiredCapability, requiredImageCapability) {
					return selection, decision, nil
				}
				if selection.ReleaseFunc != nil {
					selection.ReleaseFunc()
				}
				if effectiveExcludedIDs == nil {
					effectiveExcludedIDs = make(map[int64]struct{})
				}
				if _, exists := effectiveExcludedIDs[selection.Account.ID]; exists {
					return nil, decision, ErrNoAvailableAccounts
				}
				effectiveExcludedIDs[selection.Account.ID] = struct{}{}
			}
		}

		effectiveExcludedIDs := cloneExcludedAccountIDs(excludedIDs)
		for {
			selection, err := s.selectAccountWithLoadAwareness(ctx, groupID, platform, sessionHash, requestedModel, effectiveExcludedIDs, requireCompact, requiredCapability, requiredImageCapability, requiredTransport)
			if err != nil {
				return nil, decision, err
			}
			if selection == nil || selection.Account == nil {
				return selection, decision, nil
			}
			if s.isOpenAIAccountTransportCompatible(selection.Account, requiredTransport) &&
				accountSupportsOpenAICapabilities(selection.Account, requiredCapability, requiredImageCapability) {
				return selection, decision, nil
			}
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
			if effectiveExcludedIDs == nil {
				effectiveExcludedIDs = make(map[int64]struct{})
			}
			if _, exists := effectiveExcludedIDs[selection.Account.ID]; exists {
				return nil, decision, ErrNoAvailableAccounts
			}
			effectiveExcludedIDs[selection.Account.ID] = struct{}{}
		}
	}

	if s.checkChannelPricingRestriction(ctx, groupID, requestedModel) {
		slog.Warn("channel pricing restriction blocked request",
			"group_id", derefGroupID(groupID),
			"model", requestedModel)
		return nil, decision, fmt.Errorf("%w supporting model: %s (channel pricing restriction)", ErrNoAvailableAccounts, requestedModel)
	}

	var stickyAccountID int64
	if sessionHash != "" && s.cache != nil {
		if accountID, err := s.getStickySessionAccountID(ctx, groupID, sessionHash); err == nil && accountID > 0 {
			stickyAccountID = accountID
		}
	}

	return scheduler.Select(ctx, OpenAIAccountScheduleRequest{
		GroupID:                    groupID,
		Platform:                   platform,
		SessionHash:                sessionHash,
		StickyAccountID:            stickyAccountID,
		PreviousResponseID:         previousResponseID,
		HasFunctionCallOutput:      options.HasFunctionCallOutput,
		PreviousResponseReplayable: options.PreviousResponseReplayable,
		RequestedModel:             requestedModel,
		RequiredTransport:          requiredTransport,
		RequiredCapability:         requiredCapability,
		RequiredImageCapability:    requiredImageCapability,
		RequireCompact:             requireCompact,
		ExcludedIDs:                excludedIDs,
	})
}

func accountSupportsOpenAICapabilities(account *Account, requiredCapability OpenAIEndpointCapability, requiredImageCapability OpenAIImagesCapability) bool {
	if account == nil {
		return false
	}
	return account.SupportsOpenAIEndpointCapability(requiredCapability) &&
		account.SupportsOpenAIImageCapability(requiredImageCapability)
}

func cloneExcludedAccountIDs(excludedIDs map[int64]struct{}) map[int64]struct{} {
	if len(excludedIDs) == 0 {
		return nil
	}
	cloned := make(map[int64]struct{}, len(excludedIDs))
	for id := range excludedIDs {
		cloned[id] = struct{}{}
	}
	return cloned
}

func (s *OpenAIGatewayService) isOpenAIAccountTransportCompatible(account *Account, requiredTransport OpenAIUpstreamTransport) bool {
	if requiredTransport == OpenAIUpstreamTransportAny || requiredTransport == OpenAIUpstreamTransportHTTPSSE {
		return true
	}
	if s == nil || account == nil {
		return false
	}
	if requiredTransport == OpenAIUpstreamTransportResponsesWebsocketV2Ingress {
		if s.cfg == nil || !s.cfg.Gateway.OpenAIWS.ModeRouterV2Enabled {
			return s.getOpenAIWSProtocolResolver().Resolve(account).Transport == OpenAIUpstreamTransportResponsesWebsocketV2
		}
		mode := account.ResolveOpenAIResponsesWebSocketV2Mode(s.cfg.Gateway.OpenAIWS.IngressModeDefault)
		switch mode {
		case OpenAIWSIngressModeCtxPool, OpenAIWSIngressModePassthrough, OpenAIWSIngressModeHTTPBridge, OpenAIWSIngressModeShared, OpenAIWSIngressModeDedicated:
			return true
		default:
			return false
		}
	}
	return s.getOpenAIWSProtocolResolver().Resolve(account).Transport == requiredTransport
}

func (s *OpenAIGatewayService) ReportOpenAIAccountScheduleResult(accountID int64, success bool, firstTokenMs *int) {
	scheduler := s.getOpenAIAccountScheduler(context.Background())
	if scheduler != nil {
		scheduler.ReportResult(accountID, success, firstTokenMs)
		return
	}
	stats := s.openAIAccountRuntimeStats()
	if stats == nil {
		return
	}
	report := stats.report(accountID, success, firstTokenMs, s.openAISlowAccountConfig())
	s.logOpenAIAccountSlowStateChange(accountID, report, s.openAISlowAccountConfig())
}

func (s *OpenAIGatewayService) RecordOpenAIAccountSwitch() {
	scheduler := s.getOpenAIAccountScheduler(context.Background())
	if scheduler == nil {
		return
	}
	scheduler.ReportSwitch()
}

func (s *OpenAIGatewayService) SnapshotOpenAIAccountSchedulerMetrics() OpenAIAccountSchedulerMetricsSnapshot {
	scheduler := s.getOpenAIAccountScheduler(context.Background())
	if scheduler == nil {
		return OpenAIAccountSchedulerMetricsSnapshot{}
	}
	return scheduler.SnapshotMetrics()
}

func (s *OpenAIGatewayService) openAIWSSessionStickyTTL() time.Duration {
	if s != nil && s.cfg != nil && s.cfg.Gateway.OpenAIWS.StickySessionTTLSeconds > 0 {
		return time.Duration(s.cfg.Gateway.OpenAIWS.StickySessionTTLSeconds) * time.Second
	}
	return openaiStickySessionTTL
}

func (s *OpenAIGatewayService) openAIWSLBTopK() int {
	if s != nil && s.cfg != nil && s.cfg.Gateway.OpenAIWS.LBTopK > 0 {
		return s.cfg.Gateway.OpenAIWS.LBTopK
	}
	return 7
}

func (s *OpenAIGatewayService) openAIStickyEscapeConfig() openAIStickyEscapeConfig {
	if s != nil && s.cfg != nil {
		cfg := s.cfg.Gateway.OpenAIScheduler
		enabled := cfg.StickyEscapeEnabled
		if !enabled && cfg.StickyEscapeTTFTMs == 0 && cfg.StickyEscapeErrorRate == 0 {
			enabled = true
		}
		ttftMs := float64(cfg.StickyEscapeTTFTMs)
		if ttftMs <= 0 {
			ttftMs = 15000
		}
		errorRate := cfg.StickyEscapeErrorRate
		if errorRate < 0 || errorRate > 1 {
			errorRate = 0.5
		}
		if errorRate == 0 && cfg.StickyEscapeTTFTMs == 0 && cfg.StickyEscapeErrorRate == 0 {
			errorRate = 0.5
		}
		return openAIStickyEscapeConfig{
			enabled:   enabled,
			ttftMs:    ttftMs,
			errorRate: errorRate,
		}
	}
	return openAIStickyEscapeConfig{
		enabled:   true,
		ttftMs:    15000,
		errorRate: 0.5,
	}
}

func (s *OpenAIGatewayService) openAISlowAccountConfig() openAISlowAccountConfig {
	cfg := openAISlowAccountConfig{
		enabled:           true,
		thresholdMs:       30000,
		softThresholdMs:   15000,
		recoveryTTFTMs:    10000,
		consecutiveCount:  2,
		minSamples:        3,
		cooldown:          5 * time.Minute,
		recoveryFastCount: 2,
		penaltyWeight:     100,
		markScore:         5,
		skipScore:         8,
		maxScore:          10,
		decayInterval:     time.Minute,
	}
	if s == nil || s.cfg == nil {
		return cfg
	}
	raw := s.cfg.Gateway.OpenAIScheduler
	if !raw.SlowAccountEscapeEnabled &&
		raw.SlowTTFTThresholdMs == 0 &&
		raw.SlowSoftTTFTThresholdMs == 0 &&
		raw.SlowRecoveryTTFTMs == 0 &&
		raw.SlowTTFTConsecutiveCount == 0 &&
		raw.SlowMinSamples == 0 &&
		raw.SlowCooldownSeconds == 0 &&
		raw.SlowRecoveryFastCount == 0 &&
		raw.SlowPenaltyWeight == 0 &&
		raw.SlowScoreMarkThreshold == 0 &&
		raw.SlowScoreSkipThreshold == 0 &&
		raw.SlowScoreMax == 0 &&
		raw.SlowScoreDecayIntervalSeconds == 0 {
		return cfg
	}
	cfg.enabled = raw.SlowAccountEscapeEnabled
	if raw.SlowTTFTThresholdMs > 0 {
		cfg.thresholdMs = raw.SlowTTFTThresholdMs
	}
	if raw.SlowSoftTTFTThresholdMs > 0 {
		cfg.softThresholdMs = raw.SlowSoftTTFTThresholdMs
	}
	if raw.SlowRecoveryTTFTMs > 0 {
		cfg.recoveryTTFTMs = raw.SlowRecoveryTTFTMs
	}
	if raw.SlowTTFTConsecutiveCount > 0 {
		cfg.consecutiveCount = raw.SlowTTFTConsecutiveCount
	}
	if raw.SlowMinSamples > 0 {
		cfg.minSamples = raw.SlowMinSamples
	}
	if raw.SlowCooldownSeconds >= 0 {
		cfg.cooldown = time.Duration(raw.SlowCooldownSeconds) * time.Second
	}
	if raw.SlowRecoveryFastCount > 0 {
		cfg.recoveryFastCount = raw.SlowRecoveryFastCount
	}
	if raw.SlowPenaltyWeight >= 0 {
		cfg.penaltyWeight = raw.SlowPenaltyWeight
	}
	if raw.SlowScoreMarkThreshold > 0 {
		cfg.markScore = raw.SlowScoreMarkThreshold
	}
	if raw.SlowScoreSkipThreshold > 0 {
		cfg.skipScore = raw.SlowScoreSkipThreshold
	}
	if raw.SlowScoreMax > 0 {
		cfg.maxScore = raw.SlowScoreMax
	}
	if raw.SlowScoreDecayIntervalSeconds > 0 {
		cfg.decayInterval = time.Duration(raw.SlowScoreDecayIntervalSeconds) * time.Second
	}
	return normalizeOpenAISlowAccountConfig(cfg)
}

func (s *OpenAIGatewayService) logOpenAIAccountSlowStateChange(accountID int64, report openAIAccountRuntimeReport, cfg openAISlowAccountConfig) {
	if s == nil || accountID <= 0 {
		return
	}
	if report.markedSlow {
		slog.Warn("openai.account_marked_slow",
			"account_id", accountID,
			"first_token_ms", report.firstTokenMs,
			"ttft_ewma_ms", report.ttft,
			"slow_streak", report.slowStreak,
			"slow_score", report.slowScore,
			"sample_count", report.sampleCount,
			"slow_until", report.slowUntil,
			"threshold_ms", cfg.thresholdMs,
			"mark_score", cfg.markScore,
			"skip_score", cfg.skipScore,
			"cooldown_seconds", int(cfg.cooldown/time.Second),
		)
		return
	}
	if report.recoveredSlow {
		slog.Info("openai.account_slow_recovered",
			"account_id", accountID,
			"first_token_ms", report.firstTokenMs,
			"ttft_ewma_ms", report.ttft,
			"fast_streak", report.fastStreak,
			"slow_score", report.slowScore,
			"sample_count", report.sampleCount,
			"recovery_ttft_ms", cfg.recoveryTTFTMs,
		)
	}
}

func (s *OpenAIGatewayService) openAIStickyPreferHigherPriorityConfig(ctx context.Context) openAIStickyPreferHigherPriorityConfig {
	cfg := openAIStickyPreferHigherPriorityConfig{
		previousResponseOnlyWhenUnhealthy: true,
		minInterval:                       time.Minute,
		failureCooldown:                   5 * time.Minute,
		probeEnabled:                      true,
		probeTimeout:                      5 * time.Second,
		probeSuccessTTL:                   30 * time.Second,
		probeFailureTTL:                   time.Minute,
	}
	if s == nil {
		return cfg
	}
	if s.cfg != nil {
		raw := s.cfg.Gateway.OpenAIScheduler
		cfg.stickyEnabled = raw.StickyPreferHigherPriorityEnabled
		cfg.previousResponseEnabled = raw.PreviousResponseRebindEnabled
		cfg.previousResponseOnlyWhenUnhealthy = raw.PreviousResponseRebindOnlyWhenCurrentUnhealthy
		cfg.probeEnabled = raw.StickyFailbackProbeEnabled
		if raw.StickyPreferHigherPriorityMinIntervalSeconds >= 0 {
			cfg.minInterval = time.Duration(raw.StickyPreferHigherPriorityMinIntervalSeconds) * time.Second
		}
		if raw.StickyFailbackFailureCooldownSeconds >= 0 {
			cfg.failureCooldown = time.Duration(raw.StickyFailbackFailureCooldownSeconds) * time.Second
		}
		if raw.StickyFailbackProbeTimeoutSeconds > 0 {
			cfg.probeTimeout = time.Duration(raw.StickyFailbackProbeTimeoutSeconds) * time.Second
		}
		if raw.StickyFailbackProbeSuccessTTLSeconds >= 0 {
			cfg.probeSuccessTTL = time.Duration(raw.StickyFailbackProbeSuccessTTLSeconds) * time.Second
		}
		if raw.StickyFailbackProbeFailureTTLSeconds >= 0 {
			cfg.probeFailureTTL = time.Duration(raw.StickyFailbackProbeFailureTTLSeconds) * time.Second
		}
	}

	if cached, ok := openAIStickyPreferHigherPrioritySettingCache.Load().(*cachedOpenAIStickyPreferHigherPrioritySetting); ok && cached != nil {
		if time.Now().UnixNano() < cached.expiresAt {
			return cached.cfg
		}
	}

	result, _, _ := openAIStickyPreferHigherPrioritySettingSF.Do("openai_sticky_prefer_higher_priority", func() (any, error) {
		if cached, ok := openAIStickyPreferHigherPrioritySettingCache.Load().(*cachedOpenAIStickyPreferHigherPrioritySetting); ok && cached != nil {
			if time.Now().UnixNano() < cached.expiresAt {
				return cached.cfg, nil
			}
		}

		loaded := cfg
		if repo := s.openAIAdvancedSchedulerSettingRepo(); repo != nil {
			dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), openAIAdvancedSchedulerSettingDBTimeout)
			defer cancel()

			values, err := repo.GetMultiple(dbCtx, []string{
				openAIStickyPreferHigherPrioritySettingKey,
				openAIStickyPreferHigherPriorityMinIntervalSettingKey,
				openAIStickyFailbackFailureCooldownSettingKey,
				openAIPreviousResponseRebindSettingKey,
				openAIPreviousResponseRebindOnlyWhenCurrentUnhealthySettingKey,
			})
			if err == nil {
				if value, ok := values[openAIStickyPreferHigherPrioritySettingKey]; ok {
					loaded.stickyEnabled = parseOpenAIBoolSetting(value, loaded.stickyEnabled)
				}
				if value, ok := values[openAIStickyPreferHigherPriorityMinIntervalSettingKey]; ok {
					loaded.minInterval = time.Duration(parseOpenAINonNegativeIntSetting(value, int(loaded.minInterval/time.Second))) * time.Second
				}
				if value, ok := values[openAIStickyFailbackFailureCooldownSettingKey]; ok {
					loaded.failureCooldown = time.Duration(parseOpenAINonNegativeIntSetting(value, int(loaded.failureCooldown/time.Second))) * time.Second
				}
				if value, ok := values[openAIPreviousResponseRebindSettingKey]; ok {
					loaded.previousResponseEnabled = parseOpenAIBoolSetting(value, loaded.previousResponseEnabled)
				}
				if value, ok := values[openAIPreviousResponseRebindOnlyWhenCurrentUnhealthySettingKey]; ok {
					loaded.previousResponseOnlyWhenUnhealthy = parseOpenAIBoolSetting(value, loaded.previousResponseOnlyWhenUnhealthy)
				}
			}
		}

		openAIStickyPreferHigherPrioritySettingCache.Store(&cachedOpenAIStickyPreferHigherPrioritySetting{
			cfg:       loaded,
			expiresAt: time.Now().Add(openAIAdvancedSchedulerSettingCacheTTL).UnixNano(),
		})
		return loaded, nil
	})

	if loaded, ok := result.(openAIStickyPreferHigherPriorityConfig); ok {
		return loaded
	}
	return cfg
}

func parseOpenAIBoolSetting(value string, fallback bool) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fallback
	}
	return strings.EqualFold(trimmed, "true")
}

func parseOpenAINonNegativeIntSetting(value string, fallback int) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}

func (s *OpenAIGatewayService) allowOpenAIStickyFailbackAttempt(kind string, groupID *int64, key string, minInterval time.Duration) bool {
	if s == nil {
		return false
	}
	key = strings.TrimSpace(key)
	if key == "" || minInterval <= 0 {
		return true
	}
	cacheKey := fmt.Sprintf("%s:%d:%s", kind, derefGroupID(groupID), key)
	now := time.Now().UnixNano()
	if previous, ok := s.openaiStickyFailbackLastAttempt.Load(cacheKey); ok {
		if previousNano, ok := previous.(int64); ok && time.Duration(now-previousNano) < minInterval {
			return false
		}
	}
	s.openaiStickyFailbackLastAttempt.Store(cacheKey, now)
	return true
}

func (s *OpenAIGatewayService) RecordOpenAIStickyFailbackFailure(ctx context.Context, accountID int64, statusCode int) {
	if s == nil || accountID <= 0 || statusCode < 500 {
		return
	}
	cfg := s.openAIStickyPreferHigherPriorityConfig(ctx)
	if cfg.failureCooldown <= 0 {
		return
	}
	until := time.Now().Add(cfg.failureCooldown)
	s.openaiStickyFailbackCooldownUntil.Store(accountID, until)
	slog.Warn("openai.sticky_failback_account_cooldown",
		"account_id", accountID,
		"upstream_status", statusCode,
		"cooldown_seconds", int(cfg.failureCooldown/time.Second),
		"cooldown_until", until,
	)
}

func (s *OpenAIGatewayService) openAIStickyFailbackAccountCooldown(accountID int64) (time.Time, bool) {
	if s == nil || accountID <= 0 {
		return time.Time{}, false
	}
	value, ok := s.openaiStickyFailbackCooldownUntil.Load(accountID)
	if !ok {
		return time.Time{}, false
	}
	until, ok := value.(time.Time)
	if !ok || until.IsZero() {
		s.openaiStickyFailbackCooldownUntil.Delete(accountID)
		return time.Time{}, false
	}
	if time.Now().After(until) {
		s.openaiStickyFailbackCooldownUntil.Delete(accountID)
		return time.Time{}, false
	}
	return until, true
}

func (s *OpenAIGatewayService) logOpenAIStickyFailbackSkipped(req OpenAIAccountScheduleRequest, kind string, currentAccountID, candidateAccountID int64, reason string, extra ...slog.Attr) {
	if s == nil {
		return
	}
	attrs := []slog.Attr{
		slog.String("kind", kind),
		slog.String("reason", reason),
		slog.Int64("current_account_id", currentAccountID),
		slog.Int64("candidate_account_id", candidateAccountID),
		slog.Int64("group_id", derefGroupID(req.GroupID)),
		slog.String("model", req.RequestedModel),
		slog.Bool("session_hash_present", strings.TrimSpace(req.SessionHash) != ""),
		slog.Bool("previous_response_id_present", strings.TrimSpace(req.PreviousResponseID) != ""),
	}
	attrs = append(attrs, extra...)
	args := make([]any, 0, len(attrs))
	for _, attr := range attrs {
		args = append(args, attr)
	}
	slog.Info("openai.sticky_failback_skipped", args...)
}

func openAIStickyRebindSkipReason(req OpenAIAccountScheduleRequest) string {
	if req.HasFunctionCallOutput {
		return "function_call_output"
	}
	if !req.PreviousResponseReplayable {
		return "not_replayable"
	}
	return "current_account_healthy"
}

func (s *OpenAIGatewayService) openAIWSSchedulerWeights() GatewayOpenAIWSSchedulerScoreWeightsView {
	if s != nil && s.cfg != nil {
		return GatewayOpenAIWSSchedulerScoreWeightsView{
			Priority:      s.cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Priority,
			Load:          s.cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Load,
			Queue:         s.cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Queue,
			ErrorRate:     s.cfg.Gateway.OpenAIWS.SchedulerScoreWeights.ErrorRate,
			TTFT:          s.cfg.Gateway.OpenAIWS.SchedulerScoreWeights.TTFT,
			Reset:         s.cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Reset,
			QuotaHeadroom: s.cfg.Gateway.OpenAIWS.SchedulerScoreWeights.QuotaHeadroom,
		}
	}
	return GatewayOpenAIWSSchedulerScoreWeightsView{
		Priority:      1.0,
		Load:          1.0,
		Queue:         0.7,
		ErrorRate:     0.8,
		TTFT:          0.5,
		Reset:         0.0,
		QuotaHeadroom: 0.0,
	}
}

type GatewayOpenAIWSSchedulerScoreWeightsView struct {
	Priority  float64
	Load      float64
	Queue     float64
	ErrorRate float64
	TTFT      float64
	// Reset 倾向「会话窗口最早重置」的账号；0 表示关闭（默认）。
	Reset         float64
	QuotaHeadroom float64
}

func openAIQuotaHeadroomFactor(account *Account, now time.Time) float64 {
	if account == nil || len(account.Extra) == 0 || openAIQuotaHeadroomSnapshotStale(account.Extra, now) {
		return openAIQuotaHeadroomNeutralFactor
	}
	primaryUsedPercent, ok := resolveAccountExtraNumber(account.Extra, "codex_primary_used_percent", "codex_7d_used_percent")
	if !ok || openAIQuotaWindowResetAny(account.Extra, now, "primary", "7d") {
		return openAIQuotaHeadroomNeutralFactor
	}

	factor := 1 - clamp01(primaryUsedPercent/100)
	if secondaryUsedPercent, ok := resolveAccountExtraNumber(account.Extra, "codex_secondary_used_percent", "codex_5h_used_percent"); ok &&
		!openAIQuotaWindowResetAny(account.Extra, now, "secondary", "5h") {
		secondaryRemaining := 1 - clamp01(secondaryUsedPercent/100)
		if secondaryRemaining < openAIQuotaHeadroomSecondaryLowRemain {
			factor *= openAIQuotaHeadroomNeutralFactor
		}
	}
	return factor
}

func openAIQuotaHeadroomSnapshotStale(extra map[string]any, now time.Time) bool {
	updatedRaw, ok := extra["codex_usage_updated_at"]
	if !ok {
		return true
	}
	updatedAt, err := parseTime(fmt.Sprint(updatedRaw))
	if err != nil {
		return true
	}
	return now.Sub(updatedAt) >= openAIQuotaHeadroomSnapshotStaleAfter
}

func openAIQuotaWindowResetAny(extra map[string]any, now time.Time, windows ...string) bool {
	for _, window := range windows {
		if openAIQuotaWindowReset(extra, window, now) {
			return true
		}
	}
	return false
}

func clamp01(value float64) float64 {
	switch {
	case value < 0:
		return 0
	case value > 1:
		return 1
	default:
		return value
	}
}

func calcLoadSkewByMoments(sum float64, sumSquares float64, count int) float64 {
	if count <= 1 {
		return 0
	}
	mean := sum / float64(count)
	variance := sumSquares/float64(count) - mean*mean
	if variance < 0 {
		variance = 0
	}
	return math.Sqrt(variance)
}
