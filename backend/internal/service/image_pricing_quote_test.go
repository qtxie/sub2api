package service

import (
	"context"
	"testing"
	"time"

	gocache "github.com/patrickmn/go-cache"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/singleflight"
)

type imagePricingUserRateRepoStub struct {
	UserGroupRateRepository
	rate *float64
}

func (r imagePricingUserRateRepoStub) GetByUserAndGroup(context.Context, int64, int64) (*float64, error) {
	return r.rate, nil
}

func TestQuoteImageUnitPriceUsesConfiguredGroupPriceAndUserMultiplier(t *testing.T) {
	price1K := 0.08
	userRate := 1.25
	groupID := int64(12)
	key := &APIKey{
		UserID:  7,
		GroupID: &groupID,
		Group: &Group{
			ID:             groupID,
			RateMultiplier: 1.1,
			ImagePrice1K:   &price1K,
		},
	}
	rateResolver := newUserGroupRateResolver(
		imagePricingUserRateRepoStub{rate: &userRate},
		gocache.New(time.Minute, time.Minute),
		time.Minute,
		&singleflight.Group{},
		"service.image_pricing_test",
	)
	gateway := &GatewayService{
		billingService:        &BillingService{},
		userGroupRateResolver: rateResolver,
	}

	quote, err := gateway.QuoteImageUnitPrice(context.Background(), key, 7, "gpt-image-2", "1024x1024")
	require.NoError(t, err)
	require.Equal(t, ImagePricingKindFixed, quote.PricingKind)
	require.NotNil(t, quote.UnitPrice)
	require.InDelta(t, 0.10, *quote.UnitPrice, 1e-10)
}

func TestQuoteImageUnitPriceHonorsIndependentFreeImageRate(t *testing.T) {
	price2K := 0.20
	key := &APIKey{Group: &Group{
		RateMultiplier:       2,
		ImageRateIndependent: true,
		ImageRateMultiplier:  0,
		ImagePrice2K:         &price2K,
	}}
	gateway := &GatewayService{billingService: &BillingService{}}

	quote, err := gateway.QuoteImageUnitPrice(context.Background(), key, 7, "gpt-image-2", "2K")
	require.NoError(t, err)
	require.NotNil(t, quote.UnitPrice)
	require.Zero(t, *quote.UnitPrice)
}

func TestQuoteImageUnitPriceUsesDefaultResolutionPricing(t *testing.T) {
	key := &APIKey{Group: &Group{RateMultiplier: 1}}
	gateway := &GatewayService{billingService: &BillingService{}}

	quote1K, err := gateway.QuoteImageUnitPrice(context.Background(), key, 7, "gemini-3-pro-image", "1K")
	require.NoError(t, err)
	quote2K, err := gateway.QuoteImageUnitPrice(context.Background(), key, 7, "gemini-3-pro-image", "2K")
	require.NoError(t, err)

	require.InDelta(t, 0.134, *quote1K.UnitPrice, 1e-10)
	require.InDelta(t, 0.201, *quote2K.UnitPrice, 1e-10)
}
