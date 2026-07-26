package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestDetectQCProbeRequest_OriginAndUA(t *testing.T) {
	settings := DefaultQCProbeRoutingSettings()
	settings.Enabled = true
	settings.AccountIDs = []int64{11, 22}
	settings.UserAgentSubstrings = []string{"GreenLight-QC"}

	t.Run("tokensqc origin", func(t *testing.T) {
		sel := DetectQCProbeRequest("https://tokensqc.com", "", "Mozilla/5.0", settings)
		require.True(t, sel.Active)
		require.Equal(t, QCProbeSourceTokensQC, sel.Source)
		require.True(t, sel.allowsAccountID(11))
		require.False(t, sel.allowsAccountID(99))
	})

	t.Run("ztest referer", func(t *testing.T) {
		sel := DetectQCProbeRequest("", "https://www.ztest.ai/rankings?x=1", "Mozilla/5.0", settings)
		require.True(t, sel.Active)
		require.Equal(t, QCProbeSourceZtest, sel.Source)
	})

	t.Run("hvoy origin", func(t *testing.T) {
		sel := DetectQCProbeRequest("https://hvoy.ai", "", "curl/8.0", settings)
		require.True(t, sel.Active)
		require.Equal(t, QCProbeSourceHvoy, sel.Source)
	})

	t.Run("apiranking origin", func(t *testing.T) {
		sel := DetectQCProbeRequest("https://apiranking.com", "", "Mozilla/5.0", settings)
		require.True(t, sel.Active)
		require.Equal(t, QCProbeSourceAPIRanking, sel.Source)
	})

	t.Run("global ua", func(t *testing.T) {
		sel := DetectQCProbeRequest("", "", "GreenLight-QC/1.0", settings)
		require.True(t, sel.Active)
		require.Equal(t, QCProbeSourceUserAgent, sel.Source)
	})

	t.Run("empty user agent", func(t *testing.T) {
		sel := DetectQCProbeRequest("", "", "", settings)
		require.True(t, sel.Active)
		require.Equal(t, QCProbeSourceEmptyUserAgent, sel.Source)
		require.True(t, sel.allowsAccountID(11))
		require.False(t, sel.allowsAccountID(99))
	})

	t.Run("dash user agent", func(t *testing.T) {
		// TokensQC / nginx-style missing UA.
		for _, ua := range []string{"-", " - ", "—"} {
			sel := DetectQCProbeRequest("", "", ua, settings)
			require.True(t, sel.Active, "ua=%q", ua)
			require.Equal(t, QCProbeSourceEmptyUserAgent, sel.Source, "ua=%q", ua)
		}
	})

	t.Run("empty ua still prefers known origin", func(t *testing.T) {
		sel := DetectQCProbeRequest("https://tokensqc.com", "", "-", settings)
		require.True(t, sel.Active)
		require.Equal(t, QCProbeSourceTokensQC, sel.Source)
	})

	t.Run("no match", func(t *testing.T) {
		sel := DetectQCProbeRequest("https://example.com", "https://cursor.com", "Mozilla/5.0", settings)
		require.False(t, sel.Active)
	})

	t.Run("disabled", func(t *testing.T) {
		disabled := DefaultQCProbeRoutingSettings()
		disabled.Enabled = false
		sel := DetectQCProbeRequest("https://tokensqc.com", "", "Mozilla/5.0", disabled)
		require.False(t, sel.Active)
	})
}

func TestFilterAccountsForQCProbe(t *testing.T) {
	accounts := []Account{
		{ID: 1, Name: "a"},
		{ID: 2, Name: "b"},
		{ID: 3, Name: "c"},
	}
	settings := DefaultQCProbeRoutingSettings()
	settings.Enabled = true
	settings.AccountIDs = []int64{2, 9}
	settings.Fallback = QCProbeFallbackNormal

	sel := DetectQCProbeRequest("https://tokensqc.com", "", "", settings)
	ctx := WithQCProbeSelection(context.Background(), sel)

	filtered, reject := FilterAccountsForQCProbe(ctx, PlatformAnthropic, accounts)
	require.False(t, reject)
	require.Len(t, filtered, 1)
	require.Equal(t, int64(2), filtered[0].ID)

	// empty intersection + fallback normal keeps original pool
	settings.AccountIDs = []int64{99}
	sel = DetectQCProbeRequest("https://tokensqc.com", "", "", settings)
	ctx = WithQCProbeSelection(context.Background(), sel)
	filtered, reject = FilterAccountsForQCProbe(ctx, PlatformAnthropic, accounts)
	require.False(t, reject)
	require.Len(t, filtered, 3)

	// empty intersection + fallback reject
	settings.Fallback = QCProbeFallbackReject
	sel = DetectQCProbeRequest("https://tokensqc.com", "", "", settings)
	ctx = WithQCProbeSelection(context.Background(), sel)
	filtered, reject = FilterAccountsForQCProbe(ctx, PlatformAnthropic, accounts)
	require.True(t, reject)
	require.Nil(t, filtered)
}

func TestRestrictAccountIDsForQCProbe(t *testing.T) {
	settings := DefaultQCProbeRoutingSettings()
	settings.Enabled = true
	settings.AccountIDs = []int64{5, 6, 7}
	settings.Fallback = QCProbeFallbackNormal
	sel := DetectQCProbeRequest("https://ztest.ai", "", "", settings)
	ctx := WithQCProbeSelection(context.Background(), sel)

	// no existing shortlist -> force QC pool
	ids := RestrictAccountIDsForQCProbe(ctx, PlatformOpenAI, nil)
	require.Equal(t, []int64{5, 6, 7}, ids)

	// intersect existing shortlist
	ids = RestrictAccountIDsForQCProbe(ctx, PlatformOpenAI, []int64{1, 6, 8})
	require.Equal(t, []int64{6}, ids)

	// empty intersection + fallback normal keeps original shortlist
	ids = RestrictAccountIDsForQCProbe(ctx, PlatformOpenAI, []int64{1, 2, 3})
	require.Equal(t, []int64{1, 2, 3}, ids)

	// empty intersection + fallback reject returns empty shortlist
	settings.Fallback = QCProbeFallbackReject
	sel = DetectQCProbeRequest("https://ztest.ai", "", "", settings)
	ctx = WithQCProbeSelection(context.Background(), sel)
	ids = RestrictAccountIDsForQCProbe(ctx, PlatformOpenAI, []int64{1, 2, 3})
	require.Empty(t, ids)

	require.True(t, IsAccountAllowedByQCProbe(ctx, PlatformOpenAI, 5))
	require.False(t, IsAccountAllowedByQCProbe(ctx, PlatformOpenAI, 1))
}

func TestNormalizeQCProbeRoutingSettings(t *testing.T) {
	in := &QCProbeRoutingSettings{
		Enabled:    true,
		Fallback:   "REJECT",
		PoolScope:  "GLOBAL",
		AccountIDs: []int64{0, 3, 3, -1, 4},
		Sources: map[string]QCProbeSourceConfig{
			" TokensQC ": {Enabled: true, Origins: []string{" https://tokensqc.com ", "", "https://tokensqc.com"}},
		},
		UserAgentSubstrings: []string{" a ", "a", ""},
		ApplyPlatforms:      []string{"OpenAI", "openai", ""},
	}
	out := NormalizeQCProbeRoutingSettings(in)
	require.True(t, out.Enabled)
	require.Equal(t, QCProbeFallbackReject, out.Fallback)
	require.Equal(t, QCProbePoolScopeGlobal, out.PoolScope)
	require.Equal(t, []int64{3, 4}, out.AccountIDs)
	require.Equal(t, []string{"https://tokensqc.com"}, out.Sources["tokensqc"].Origins)
	// Partial sources merge must keep default QC sites.
	require.Contains(t, out.Sources, QCProbeSourceZtest)
	require.Equal(t, []string{"https://ztest.ai", "https://www.ztest.ai"}, out.Sources[QCProbeSourceZtest].Origins)
	require.Equal(t, []string{"a"}, out.UserAgentSubstrings)
	require.Equal(t, []string{"openai"}, out.ApplyPlatforms)
}

func TestResolveAccountsForQCProbe_GlobalScopeIgnoresGroupAndSchedulable(t *testing.T) {
	// Group-schedulable candidates do not include the burn pool.
	groupAccounts := []Account{
		{ID: 1, Name: "prod", Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true},
		{ID: 2, Name: "prod2", Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true},
	}
	// Pool: outside group + manually unschedulable, but active openai.
	poolOutside := Account{
		ID: 16, Name: "burn-out-group", Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true,
	}
	poolUnsched := Account{
		ID: 34, Name: "burn-unsched", Platform: PlatformOpenAI, Status: StatusActive, Schedulable: false,
	}
	loader := func(_ context.Context, ids []int64) ([]Account, error) {
		byID := map[int64]Account{16: poolOutside, 34: poolUnsched}
		out := make([]Account, 0, len(ids))
		for _, id := range ids {
			if acc, ok := byID[id]; ok {
				out = append(out, acc)
			}
		}
		return out, nil
	}

	settings := DefaultQCProbeRoutingSettings()
	settings.Enabled = true
	settings.PoolScope = QCProbePoolScopeGlobal
	settings.AccountIDs = []int64{16, 34}
	settings.Fallback = QCProbeFallbackReject
	sel := DetectQCProbeRequest("https://ztest.ai", "", "", settings)
	require.True(t, sel.Active)
	require.Equal(t, QCProbePoolScopeGlobal, sel.PoolScope)
	ctx := WithQCProbeSelection(context.Background(), sel)

	// Group scope would fall through / reject; global must load pool directly.
	filtered, reject := ResolveAccountsForQCProbe(ctx, PlatformOpenAI, groupAccounts, loader)
	require.False(t, reject)
	require.Len(t, filtered, 2)
	require.Equal(t, int64(16), filtered[0].ID)
	require.Equal(t, int64(34), filtered[1].ID)
	// Manual unschedulable is forced true for scheduling gates.
	require.True(t, filtered[1].Schedulable)
	require.True(t, IsQCProbeGlobalPoolAccount(ctx, PlatformOpenAI, 16))
	require.False(t, IsQCProbeGlobalPoolAccount(ctx, PlatformOpenAI, 1))

	// Empty intersection under group scope still falls back/rejects without inventing accounts.
	settings.PoolScope = QCProbePoolScopeGroup
	sel = DetectQCProbeRequest("https://ztest.ai", "", "", settings)
	ctx = WithQCProbeSelection(context.Background(), sel)
	filtered, reject = ResolveAccountsForQCProbe(ctx, PlatformOpenAI, groupAccounts, loader)
	require.True(t, reject)
	require.Nil(t, filtered)
}

func TestRestrictAccountIDsForQCProbe_GlobalEmptyIntersectionForcesPool(t *testing.T) {
	settings := DefaultQCProbeRoutingSettings()
	settings.Enabled = true
	settings.PoolScope = QCProbePoolScopeGlobal
	settings.AccountIDs = []int64{16, 34}
	settings.Fallback = QCProbeFallbackNormal
	sel := DetectQCProbeRequest("https://ztest.ai", "", "", settings)
	ctx := WithQCProbeSelection(context.Background(), sel)

	ids := RestrictAccountIDsForQCProbe(ctx, PlatformOpenAI, []int64{1, 2, 3})
	require.Equal(t, []int64{16, 34}, ids)
}

func TestResolveAccountsForQCProbe_GlobalScopeRateLimitedFallsBackNormal(t *testing.T) {
	until := time.Now().Add(time.Hour)
	groupAccounts := []Account{
		{ID: 1, Name: "prod", Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true},
	}
	poolRateLimited := Account{
		ID: 16, Name: "burn-rl", Platform: PlatformOpenAI, Status: StatusActive, Schedulable: false,
		RateLimitResetAt: &until,
	}
	loader := func(_ context.Context, ids []int64) ([]Account, error) {
		return []Account{poolRateLimited}, nil
	}

	settings := DefaultQCProbeRoutingSettings()
	settings.Enabled = true
	settings.PoolScope = QCProbePoolScopeGlobal
	settings.AccountIDs = []int64{16}
	settings.Fallback = QCProbeFallbackNormal
	sel := DetectQCProbeRequest("https://ztest.ai", "", "", settings)
	ctx := WithQCProbeSelection(context.Background(), sel)

	filtered, reject := ResolveAccountsForQCProbe(ctx, PlatformOpenAI, groupAccounts, loader)
	require.False(t, reject)
	// Rate-limited burn accounts must not count as a successful pool hit under fallback=normal.
	require.Equal(t, groupAccounts, filtered)
}

func TestIsAccountAllowedByQCProbe_StickyGate(t *testing.T) {
	settings := DefaultQCProbeRoutingSettings()
	settings.Enabled = true
	settings.AccountIDs = []int64{42}
	sel := DetectQCProbeRequest("https://tokensqc.com", "", "", settings)
	ctx := WithQCProbeSelection(context.Background(), sel)

	require.True(t, IsAccountAllowedByQCProbe(ctx, PlatformOpenAI, 42))
	require.False(t, IsAccountAllowedByQCProbe(ctx, PlatformOpenAI, 99))
	// Non-QC request context allows any account.
	require.True(t, IsAccountAllowedByQCProbe(context.Background(), PlatformOpenAI, 99))
}

func TestRecheckSelectedOpenAIAccountFromDB_GlobalQCProbeAllowsManualUnschedulable(t *testing.T) {
	// Outside request group + manually unschedulable burn account.
	burn := Account{
		ID:          34,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: false,
		Concurrency: 1,
		GroupIDs:    []int64{999},
	}
	repo := schedulerTestOpenAIAccountRepo{accounts: []Account{burn}}
	svc := &OpenAIGatewayService{
		accountRepo:       repo,
		cfg:               &config.Config{RunMode: config.RunModeStandard},
		schedulerSnapshot: &SchedulerSnapshotService{cache: &openAISnapshotCacheStub{}},
	}

	settings := DefaultQCProbeRoutingSettings()
	settings.Enabled = true
	settings.PoolScope = QCProbePoolScopeGlobal
	settings.AccountIDs = []int64{34}
	settings.Fallback = QCProbeFallbackReject
	sel := DetectQCProbeRequest("https://ztest.ai", "", "", settings)
	ctx := WithQCProbeSelection(context.Background(), sel)

	groupID := int64(1)
	// Without QC context the outside-group unschedulable account must be rejected.
	require.Nil(t, svc.recheckSelectedOpenAIAccountFromDB(context.Background(), &burn, &groupID, PlatformOpenAI, "gpt-5.1", false, ""))

	// With global QC context, DB recheck must keep the account and force Schedulable.
	fresh := svc.recheckSelectedOpenAIAccountFromDB(ctx, &burn, &groupID, PlatformOpenAI, "gpt-5.1", false, "")
	require.NotNil(t, fresh)
	require.Equal(t, int64(34), fresh.ID)
	require.True(t, fresh.Schedulable)
	require.True(t, fresh.IsSchedulable())
}

func TestOpenAITryStickySessionHit_GlobalQCProbeAllowsOutsideGroupUnschedulable(t *testing.T) {
	sessionHash := "sess-qc-global"
	burn := Account{
		ID:          34,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: false,
		Concurrency: 1,
		GroupIDs:    []int64{999},
	}
	repo := stubOpenAIAccountRepo{accounts: []Account{burn}}
	cache := &stubGatewayCache{
		sessionBindings: map[string]int64{
			"openai:" + sessionHash: 34,
		},
	}
	svc := &OpenAIGatewayService{
		accountRepo:       repo,
		cache:             cache,
		cfg:               &config.Config{RunMode: config.RunModeStandard},
		schedulerSnapshot: &SchedulerSnapshotService{cache: &openAISnapshotCacheStub{
			accountsByID: map[int64]*Account{34: &burn},
		}},
	}

	settings := DefaultQCProbeRoutingSettings()
	settings.Enabled = true
	settings.PoolScope = QCProbePoolScopeGlobal
	settings.AccountIDs = []int64{34}
	settings.Fallback = QCProbeFallbackReject
	sel := DetectQCProbeRequest("https://ztest.ai", "", "", settings)
	ctx := WithQCProbeSelection(context.Background(), sel)

	groupID := int64(1)
	hit := svc.tryStickySessionHit(ctx, &groupID, PlatformOpenAI, sessionHash, "gpt-5.1", nil, false, 0, "")
	require.NotNil(t, hit, "global QC burn account must survive sticky recheck")
	require.Equal(t, int64(34), hit.ID)
	require.True(t, hit.Schedulable)
}

func TestOpenAITryStickySessionHit_RejectsOutsideQCPool(t *testing.T) {
	sessionHash := "sess-qc"
	repo := stubOpenAIAccountRepo{
		accounts: []Account{
			{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1},
			{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1},
		},
	}
	cache := &stubGatewayCache{
		sessionBindings: map[string]int64{
			"openai:" + sessionHash: 1,
		},
	}
	svc := &OpenAIGatewayService{
		accountRepo: repo,
		cache:       cache,
	}

	settings := DefaultQCProbeRoutingSettings()
	settings.Enabled = true
	settings.AccountIDs = []int64{2}
	sel := DetectQCProbeRequest("https://tokensqc.com", "", "", settings)
	ctx := WithQCProbeSelection(context.Background(), sel)

	hit := svc.tryStickySessionHit(ctx, nil, PlatformOpenAI, sessionHash, "gpt-4", nil, false, 0, "")
	require.Nil(t, hit, "sticky account outside QC pool must be rejected")
	require.Equal(t, 1, cache.deletedSessions["openai:"+sessionHash])

	// In-pool sticky still works.
	cache.sessionBindings["openai:"+sessionHash] = 2
	hit = svc.tryStickySessionHit(ctx, nil, PlatformOpenAI, sessionHash, "gpt-4", nil, false, 0, "")
	require.NotNil(t, hit)
	require.Equal(t, int64(2), hit.ID)
}
