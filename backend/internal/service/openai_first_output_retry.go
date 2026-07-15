package service

import (
	"context"
	"sort"
	"strings"
	"time"
)

// OpenAIFirstOutputAttempt describes one replay-safe /responses attempt. The
// proxy URL is intentionally kept in context only and must never be logged.
type OpenAIFirstOutputAttempt struct {
	Attempt        int
	Retry          int
	Route          string
	ProxyID        int64
	ProxyName      string
	ProxyURL       string
	FreshTransport bool
}

type openAIFirstOutputAttemptContextKey struct{}

func WithOpenAIFirstOutputAttempt(ctx context.Context, attempt OpenAIFirstOutputAttempt) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, openAIFirstOutputAttemptContextKey{}, attempt)
}

func OpenAIFirstOutputAttemptFromContext(ctx context.Context) OpenAIFirstOutputAttempt {
	if ctx == nil {
		return OpenAIFirstOutputAttempt{}
	}
	attempt, _ := ctx.Value(openAIFirstOutputAttemptContextKey{}).(OpenAIFirstOutputAttempt)
	return attempt
}

func NewOpenAIFirstOutputDirectRetryAttempt(retry int) OpenAIFirstOutputAttempt {
	return OpenAIFirstOutputAttempt{
		Attempt:        retry + 1,
		Retry:          retry,
		Route:          "direct",
		FreshTransport: true,
	}
}

func (s *OpenAIGatewayService) openAIProxyURLForAttempt(ctx context.Context, account *Account) string {
	if attempt := OpenAIFirstOutputAttemptFromContext(ctx); attempt.Retry > 0 {
		return attempt.ProxyURL
	}
	if account != nil && account.ProxyID != nil && account.Proxy != nil {
		return account.Proxy.URL()
	}
	return ""
}

// SelectOpenAIFirstOutputRetryRoute chooses an active, non-expired proxy that
// has not already been tried when possible. Missing probe data is treated as
// unknown rather than unhealthy so a configured proxy can still be used when
// Redis has no recent probe result.
func (s *OpenAIGatewayService) SelectOpenAIFirstOutputRetryRoute(
	ctx context.Context,
	account *Account,
	retry int,
	usedProxyIDs map[int64]struct{},
) (OpenAIFirstOutputAttempt, error) {
	attempt := NewOpenAIFirstOutputDirectRetryAttempt(retry)
	if s == nil || s.proxyRepo == nil {
		if proxy := eligibleAssignedOpenAIRetryProxy(account, time.Now()); proxy != nil {
			attempt.Route = "proxy"
			attempt.ProxyID = proxy.ID
			attempt.ProxyName = proxy.Name
			attempt.ProxyURL = proxy.URL()
		}
		return attempt, nil
	}

	proxies, err := s.proxyRepo.ListActive(ctx)
	if err != nil {
		return attempt, err
	}
	now := time.Now()
	eligible := make([]Proxy, 0, len(proxies))
	proxyIDs := make([]int64, 0, len(proxies))
	for i := range proxies {
		proxy := proxies[i]
		if !eligibleOpenAIRetryProxy(&proxy, now) {
			continue
		}
		eligible = append(eligible, proxy)
		proxyIDs = append(proxyIDs, proxy.ID)
	}

	latencies := map[int64]*ProxyLatencyInfo{}
	if s.proxyLatencyCache != nil && len(proxyIDs) > 0 {
		if cached, cacheErr := s.proxyLatencyCache.GetProxyLatencies(ctx, proxyIDs); cacheErr == nil {
			latencies = cached
		}
	}
	eligible = filterHealthyOpenAIRetryProxies(eligible, latencies)
	if len(eligible) == 0 {
		return attempt, nil
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		leftUsed := proxyWasUsed(usedProxyIDs, eligible[i].ID)
		rightUsed := proxyWasUsed(usedProxyIDs, eligible[j].ID)
		if leftUsed != rightUsed {
			return !leftUsed
		}
		leftLatency := openAIRetryProxyLatency(latencies[eligible[i].ID])
		rightLatency := openAIRetryProxyLatency(latencies[eligible[j].ID])
		if leftLatency != rightLatency {
			return leftLatency < rightLatency
		}
		return eligible[i].ID < eligible[j].ID
	})

	selected := eligible[0]
	attempt.Route = "proxy"
	attempt.ProxyID = selected.ID
	attempt.ProxyName = selected.Name
	attempt.ProxyURL = selected.URL()
	return attempt, nil
}

// NewOpenAIFirstOutputRetrySelection creates a fresh slot-acquisition plan for
// an already selected account. Reusing the scheduler result would reuse a
// release callback for a slot that the previous attempt has already returned.
func (s *OpenAIGatewayService) NewOpenAIFirstOutputRetrySelection(account *Account) *AccountSelectionResult {
	if account == nil {
		return nil
	}
	cfg := s.schedulingConfig()
	return &AccountSelectionResult{
		Account: account,
		WaitPlan: &AccountWaitPlan{
			AccountID:      account.ID,
			MaxConcurrency: account.Concurrency,
			Timeout:        cfg.FallbackWaitTimeout,
			MaxWaiting:     cfg.FallbackMaxWaiting,
		},
	}
}

func eligibleAssignedOpenAIRetryProxy(account *Account, now time.Time) *Proxy {
	if account == nil || account.Proxy == nil || !eligibleOpenAIRetryProxy(account.Proxy, now) {
		return nil
	}
	return account.Proxy
}

func eligibleOpenAIRetryProxy(proxy *Proxy, now time.Time) bool {
	if proxy == nil || !proxy.IsActive() || proxy.IsExpired(now) || proxy.Host == "" || proxy.Port <= 0 {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(proxy.Protocol)) {
	case "http", "https", "socks5", "socks5h":
		return true
	default:
		return false
	}
}

func filterHealthyOpenAIRetryProxies(proxies []Proxy, latencies map[int64]*ProxyLatencyInfo) []Proxy {
	healthy := proxies[:0]
	for i := range proxies {
		info := latencies[proxies[i].ID]
		if info != nil {
			quality := strings.ToLower(strings.TrimSpace(info.QualityStatus))
			if !info.Success || quality == "failed" || quality == "fail" || quality == "challenge" {
				continue
			}
		}
		healthy = append(healthy, proxies[i])
	}
	return healthy
}

func openAIRetryProxyLatency(info *ProxyLatencyInfo) int64 {
	if info == nil || info.LatencyMs == nil || *info.LatencyMs < 0 {
		return int64(^uint64(0) >> 1)
	}
	return *info.LatencyMs
}

func proxyWasUsed(used map[int64]struct{}, proxyID int64) bool {
	_, ok := used[proxyID]
	return ok
}
