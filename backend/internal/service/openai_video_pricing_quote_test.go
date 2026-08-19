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

	quote, err := gateway.QuoteVideoPrice(context.Background(), key, 7, "grok-imagine-video-1.5", "720p", 10, false)
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

	quote, err := gateway.QuoteVideoPrice(context.Background(), key, 7, "grok-imagine-video-1.5", "1080p", 15, true)
	require.NoError(t, err)
	require.NotNil(t, quote.UnitPrice)
	require.NotNil(t, quote.TotalPrice)
	require.Zero(t, *quote.UnitPrice)
	require.Zero(t, *quote.TotalPrice)
}

func TestOpenAIQuoteVideoPriceRejectsValuesSettlementWouldNormalize(t *testing.T) {
	key := &APIKey{Group: &Group{}}
	gateway := &OpenAIGatewayService{billingService: &BillingService{}}

	_, err := gateway.QuoteVideoPrice(context.Background(), key, 7, "grok-imagine-video-1.5", "4k", 8, false)
	require.ErrorIs(t, err, ErrInvalidVideoPricingInput)

	_, err = gateway.QuoteVideoPrice(context.Background(), key, 7, "grok-imagine-video-1.5", "720p", 16, false)
	require.ErrorIs(t, err, ErrInvalidVideoPricingInput)

	_, err = gateway.QuoteVideoPrice(context.Background(), nil, 7, "grok-imagine-video-1.5", "720p", 8, false)
	require.True(t, errors.Is(err, ErrVideoPricingUnavailable))
}

func TestOpenAIQuoteVideoPriceIncludesGrok15InputImageFee(t *testing.T) {
	configuredPrice := 0.20
	key := &APIKey{Group: &Group{
		VideoRateIndependent: true,
		VideoRateMultiplier:  1.5,
		VideoModelPrices: map[string]map[string]float64{
			VideoPriceFamilyGrokImagineVideo15: {VideoBillingResolution720P: configuredPrice},
		},
	}}
	gateway := &OpenAIGatewayService{billingService: &BillingService{}}

	quote, err := gateway.QuoteVideoPrice(context.Background(), key, 7, "grok-imagine-video-1.5", "720p", 10, true)
	require.NoError(t, err)
	require.NotNil(t, quote.UnitPrice)
	require.NotNil(t, quote.TotalPrice)
	require.InDelta(t, (configuredPrice*10+grokImagineVideo15InputImagePrice)*1.5, *quote.TotalPrice, 1e-10)
	require.InDelta(t, *quote.TotalPrice/10, *quote.UnitPrice, 1e-10)
}

func TestCalculateOpenAIVideoCostAddsGrok15InputFeeAcrossFixedPricingSources(t *testing.T) {
	model := "grok-imagine-video-1.5"
	result := &OpenAIForwardResult{
		VideoCount: 1, VideoResolution: VideoBillingResolution720P,
		VideoInputImageCount: 1, VideoDurationSeconds: 1,
	}

	t.Run("default", func(t *testing.T) {
		gateway := &OpenAIGatewayService{billingService: &BillingService{}}
		cost := gateway.calculateOpenAIVideoCost(context.Background(), model, &APIKey{Group: &Group{}}, result, 2)
		require.InDelta(t, defaultGrokImagineVideo15Price720P+grokImagineVideo15InputImagePrice, cost.TotalCost, 1e-12)
		require.InDelta(t, (defaultGrokImagineVideo15Price720P+grokImagineVideo15InputImagePrice)*2, cost.ActualCost, 1e-12)
		require.InDelta(t, grokImagineVideo15InputImagePrice, cost.ImageInputCost, 1e-12)
	})

	t.Run("group", func(t *testing.T) {
		price := 0.20
		gateway := &OpenAIGatewayService{billingService: &BillingService{}}
		key := &APIKey{Group: &Group{VideoModelPrices: map[string]map[string]float64{
			VideoPriceFamilyGrokImagineVideo15: {VideoBillingResolution720P: price},
		}}}
		cost := gateway.calculateOpenAIVideoCost(context.Background(), model, key, result, 1.5)
		require.InDelta(t, price+grokImagineVideo15InputImagePrice, cost.TotalCost, 1e-12)
		require.InDelta(t, (price+grokImagineVideo15InputImagePrice)*1.5, cost.ActualCost, 1e-12)
	})

	t.Run("channel", func(t *testing.T) {
		groupID := int64(501)
		price := 0.30
		gateway := &OpenAIGatewayService{
			billingService: &BillingService{},
			resolver:       newOpenAIImageChannelPricingResolverForTest(t, groupID, model, price),
		}
		key := &APIKey{GroupID: &groupID, Group: &Group{ID: groupID}}
		cost := gateway.calculateOpenAIVideoCost(context.Background(), model, key, result, 1.25)
		require.InDelta(t, price+grokImagineVideo15InputImagePrice, cost.TotalCost, 1e-12)
		require.InDelta(t, (price+grokImagineVideo15InputImagePrice)*1.25, cost.ActualCost, 1e-12)
	})
}
