package service

import (
	"context"
	"testing"

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
	require.Equal(t, []int64{3, 4}, out.AccountIDs)
	require.Equal(t, []string{"https://tokensqc.com"}, out.Sources["tokensqc"].Origins)
	// Partial sources merge must keep default QC sites.
	require.Contains(t, out.Sources, QCProbeSourceZtest)
	require.Equal(t, []string{"https://ztest.ai", "https://www.ztest.ai"}, out.Sources[QCProbeSourceZtest].Origins)
	require.Equal(t, []string{"a"}, out.UserAgentSubstrings)
	require.Equal(t, []string{"openai"}, out.ApplyPlatforms)
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
