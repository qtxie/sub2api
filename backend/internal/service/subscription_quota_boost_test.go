package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

type quotaBoostRepoStub struct {
	userSubRepoNoop
	sub *UserSubscription
}

func (r *quotaBoostRepoStub) copy() *UserSubscription {
	if r.sub == nil {
		return nil
	}
	cp := *r.sub
	return &cp
}

func (r *quotaBoostRepoStub) GetByID(context.Context, int64) (*UserSubscription, error) {
	if r.sub == nil {
		return nil, ErrSubscriptionNotFound
	}
	return r.copy(), nil
}

func (r *quotaBoostRepoStub) GetByIDForUpdate(context.Context, int64) (*UserSubscription, error) {
	if r.sub == nil {
		return nil, ErrSubscriptionNotFound
	}
	return r.copy(), nil
}

func (r *quotaBoostRepoStub) UpdateQuotaBoostMonthlyLimit(_ context.Context, _ int64, limit int) error {
	r.sub.QuotaBoostMonthlyLimit = limit
	if limit == 0 {
		r.sub.QuotaBoostActivatedAt = nil
	}
	return nil
}

func (r *quotaBoostRepoStub) UpdateQuotaBoostActivation(_ context.Context, _ int64, used int, periodStart, activatedAt time.Time) error {
	r.sub.QuotaBoostMonthlyUsed = used
	r.sub.QuotaBoostPeriodStart = &periodStart
	r.sub.QuotaBoostActivatedAt = &activatedAt
	return nil
}

func quotaBoostTestSubscription() *UserSubscription {
	dailyLimit := 10.0
	return &UserSubscription{
		ID:                     1,
		UserID:                 42,
		GroupID:                7,
		Status:                 SubscriptionStatusActive,
		StartsAt:               timezone.Now().Add(-24 * time.Hour),
		ExpiresAt:              timezone.Now().Add(30 * 24 * time.Hour),
		QuotaBoostMonthlyLimit: 2,
		Group: &Group{
			ID:               7,
			SubscriptionType: SubscriptionTypeSubscription,
			DailyLimitUSD:    &dailyLimit,
		},
	}
}

func TestActivateQuotaBoostIsIdempotentWithinServerDay(t *testing.T) {
	repo := &quotaBoostRepoStub{sub: quotaBoostTestSubscription()}
	svc := NewSubscriptionService(nil, repo, nil, nil, nil)

	first, alreadyActive, err := svc.ActivateQuotaBoost(context.Background(), 1, 42)
	require.NoError(t, err)
	require.False(t, alreadyActive)
	require.Equal(t, 1, first.QuotaBoostMonthlyUsedAt(timezone.Now()))
	require.True(t, first.IsQuotaBoostActiveAt(timezone.Now()))

	second, alreadyActive, err := svc.ActivateQuotaBoost(context.Background(), 1, 42)
	require.NoError(t, err)
	require.True(t, alreadyActive)
	require.Equal(t, 1, second.QuotaBoostMonthlyUsedAt(timezone.Now()))
}

func TestActivateQuotaBoostRejectsUnavailableSubscriptions(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*UserSubscription)
		userID  int64
		wantErr error
	}{
		{
			name:    "wrong owner",
			mutate:  func(*UserSubscription) {},
			userID:  99,
			wantErr: ErrSubscriptionNotFound,
		},
		{
			name:    "disabled",
			mutate:  func(sub *UserSubscription) { sub.QuotaBoostMonthlyLimit = 0 },
			userID:  42,
			wantErr: ErrQuotaBoostDisabled,
		},
		{
			name: "monthly exhausted",
			mutate: func(sub *UserSubscription) {
				now := timezone.Now()
				period := timezone.StartOfMonth(now)
				sub.QuotaBoostPeriodStart = &period
				sub.QuotaBoostMonthlyUsed = sub.QuotaBoostMonthlyLimit
			},
			userID:  42,
			wantErr: ErrQuotaBoostMonthlyExhausted,
		},
		{
			name:    "no daily limit",
			mutate:  func(sub *UserSubscription) { sub.Group.DailyLimitUSD = nil },
			userID:  42,
			wantErr: ErrQuotaBoostRequiresDailyLimit,
		},
		{
			name:    "expired",
			mutate:  func(sub *UserSubscription) { sub.ExpiresAt = timezone.Now().Add(-time.Minute) },
			userID:  42,
			wantErr: ErrSubscriptionExpired,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sub := quotaBoostTestSubscription()
			tt.mutate(sub)
			svc := NewSubscriptionService(nil, &quotaBoostRepoStub{sub: sub}, nil, nil, nil)
			_, _, err := svc.ActivateQuotaBoost(context.Background(), 1, tt.userID)
			require.True(t, errors.Is(err, tt.wantErr), "got %v", err)
		})
	}
}

func TestQuotaBoostMonthlyUsageLazilyResets(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, timezone.Location())
	previousPeriod := timezone.StartOfMonth(now).AddDate(0, -1, 0)
	sub := quotaBoostTestSubscription()
	sub.QuotaBoostMonthlyLimit = 3
	sub.QuotaBoostMonthlyUsed = 3
	sub.QuotaBoostPeriodStart = &previousPeriod

	require.Equal(t, 0, sub.QuotaBoostMonthlyUsedAt(now))
	require.Equal(t, 3, sub.QuotaBoostRemainingAt(now))
}

func TestQuotaBoostDoublesOnlyDailyLimitForToday(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, timezone.Location())
	yesterday := now.AddDate(0, 0, -1)
	dailyLimit := 10.0
	weeklyLimit := 50.0
	sub := quotaBoostTestSubscription()
	sub.Group.DailyLimitUSD = &dailyLimit
	sub.Group.WeeklyLimitUSD = &weeklyLimit
	sub.QuotaBoostActivatedAt = &now

	require.Equal(t, 20.0, *sub.EffectiveDailyLimitUSDAt(sub.Group, now))
	require.Equal(t, 10.0, *sub.EffectiveDailyLimitUSDAt(sub.Group, yesterday))
	require.Equal(t, 50.0, *sub.Group.WeeklyLimitUSD)
}

func TestSetQuotaBoostMonthlyLimitValidatesRangeAndDailyLimit(t *testing.T) {
	repo := &quotaBoostRepoStub{sub: quotaBoostTestSubscription()}
	svc := NewSubscriptionService(nil, repo, nil, nil, nil)

	_, err := svc.SetQuotaBoostMonthlyLimit(context.Background(), 1, 32)
	require.ErrorIs(t, err, ErrInvalidQuotaBoostLimit)

	repo.sub.Group.DailyLimitUSD = nil
	_, err = svc.SetQuotaBoostMonthlyLimit(context.Background(), 1, 1)
	require.ErrorIs(t, err, ErrQuotaBoostRequiresDailyLimit)

	updated, err := svc.SetQuotaBoostMonthlyLimit(context.Background(), 1, 0)
	require.NoError(t, err)
	require.Zero(t, updated.QuotaBoostMonthlyLimit)
}

func TestDisablingQuotaBoostRevokesAnActiveDailyBoost(t *testing.T) {
	repo := &quotaBoostRepoStub{sub: quotaBoostTestSubscription()}
	now := timezone.Now()
	repo.sub.QuotaBoostActivatedAt = &now
	periodStart := timezone.StartOfMonth(now)
	repo.sub.QuotaBoostPeriodStart = &periodStart
	repo.sub.QuotaBoostMonthlyUsed = 1
	svc := NewSubscriptionService(nil, repo, nil, nil, nil)

	require.Equal(t, 20.0, *repo.sub.EffectiveDailyLimitUSDAt(repo.sub.Group, now))

	updated, err := svc.SetQuotaBoostMonthlyLimit(context.Background(), repo.sub.ID, 0)
	require.NoError(t, err)
	require.False(t, updated.IsQuotaBoostActiveAt(now))
	require.Equal(t, 10.0, *updated.EffectiveDailyLimitUSDAt(updated.Group, now))
	require.Nil(t, updated.QuotaBoostActivatedAt)

	reenabled, err := svc.SetQuotaBoostMonthlyLimit(context.Background(), repo.sub.ID, 2)
	require.NoError(t, err)
	require.False(t, reenabled.IsQuotaBoostActiveAt(now))
	require.Equal(t, 10.0, *reenabled.EffectiveDailyLimitUSDAt(reenabled.Group, now))

	activated, alreadyActive, err := svc.ActivateQuotaBoost(context.Background(), repo.sub.ID, 42)
	require.NoError(t, err)
	require.False(t, alreadyActive)
	require.True(t, activated.IsQuotaBoostActiveAt(timezone.Now()))
	require.Equal(t, 2, activated.QuotaBoostMonthlyUsedAt(timezone.Now()))
}

type quotaBoostBillingCache struct {
	BillingCache
	dailyUsage float64
}

func (c *quotaBoostBillingCache) GetSubscriptionCache(context.Context, int64, int64) (*SubscriptionCacheData, error) {
	return &SubscriptionCacheData{
		Status:     SubscriptionStatusActive,
		ExpiresAt:  timezone.Now().Add(24 * time.Hour),
		DailyUsage: c.dailyUsage,
	}, nil
}

func TestBillingEligibilityUsesBoostedDailyLimit(t *testing.T) {
	dailyLimit := 10.0
	group := &Group{ID: 7, SubscriptionType: SubscriptionTypeSubscription, DailyLimitUSD: &dailyLimit}
	sub := quotaBoostTestSubscription()
	now := timezone.Now()
	sub.QuotaBoostActivatedAt = &now
	cache := &quotaBoostBillingCache{dailyUsage: 15}
	svc := &BillingCacheService{cache: cache, cfg: &config.Config{}}

	err := svc.CheckBillingEligibility(context.Background(), &User{ID: 42}, nil, group, sub, "")
	require.NoError(t, err)

	yesterday := timezone.StartOfDay(now).Add(-time.Minute)
	sub.QuotaBoostActivatedAt = &yesterday
	err = svc.CheckBillingEligibility(context.Background(), &User{ID: 42}, nil, group, sub, "")
	require.ErrorIs(t, err, ErrDailyLimitExceeded)
}
