package service

import (
	"context"
	"errors"
	"testing"
	"time"

	gocache "github.com/patrickmn/go-cache"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/singleflight"
)

func TestOpenAIQuoteVideoPriceUsesConfiguredPriceAndUserMultiplier(t *testing.T) {
	configuredPrice := 0.20
	userRate := 1.25
	groupID := int64(12)
	key := &APIKey{
		UserID:  7,
		GroupID: &groupID,
		Group: &Group{
			ID:             groupID,
			RateMultiplier: 1.1,
			VideoModelPrices: map[string]map[string]float64{
				VideoPriceFamilyGrokImagineVideo15: {VideoBillingResolution720P: configuredPrice},
			},
		},
	}
	resolver := newUserGroupRateResolver(
		openAIImagePricingUserRateRepoStub{rate: &userRate},
		gocache.New(time.Minute, time.Minute),
		time.Minute,
		&singleflight.Group{},
		"service.openai_video_pricing_test",
	)
	gateway := &OpenAIGatewayService{billingService: &BillingService{}, userGroupRateResolver: resolver}

	quote, err := gateway.QuoteVideoPrice(context.Background(), key, 7, "grok-imagine-video-1.5", "720p", 10)
	require.NoError(t, err)
	require.Equal(t, VideoPricingKindFixed, quote.PricingKind)
	require.NotNil(t, quote.UnitPrice)
	require.NotNil(t, quote.TotalPrice)
	require.InDelta(t, 0.25, *quote.UnitPrice, 1e-10)
	require.InDelta(t, 2.50, *quote.TotalPrice, 1e-10)
}

func TestOpenAIQuoteVideoPriceHonorsIndependentFreeVideoRate(t *testing.T) {
	price1080P := 0.25
	key := &APIKey{Group: &Group{
		RateMultiplier:       2,
		VideoRateIndependent: true,
		VideoRateMultiplier:  0,
		VideoPrice1080P:      &price1080P,
	}}
	gateway := &OpenAIGatewayService{billingService: &BillingService{}}

	quote, err := gateway.QuoteVideoPrice(context.Background(), key, 7, "grok-imagine-video-1.5", "1080p", 15)
	require.NoError(t, err)
	require.NotNil(t, quote.UnitPrice)
	require.NotNil(t, quote.TotalPrice)
	require.Zero(t, *quote.UnitPrice)
	require.Zero(t, *quote.TotalPrice)
}

func TestOpenAIQuoteVideoPriceRejectsValuesSettlementWouldNormalize(t *testing.T) {
	key := &APIKey{Group: &Group{}}
	gateway := &OpenAIGatewayService{billingService: &BillingService{}}

	_, err := gateway.QuoteVideoPrice(context.Background(), key, 7, "grok-imagine-video-1.5", "4k", 8)
	require.ErrorIs(t, err, ErrInvalidVideoPricingInput)

	_, err = gateway.QuoteVideoPrice(context.Background(), key, 7, "grok-imagine-video-1.5", "720p", 16)
	require.ErrorIs(t, err, ErrInvalidVideoPricingInput)

	_, err = gateway.QuoteVideoPrice(context.Background(), nil, 7, "grok-imagine-video-1.5", "720p", 8)
	require.True(t, errors.Is(err, ErrVideoPricingUnavailable))
}
