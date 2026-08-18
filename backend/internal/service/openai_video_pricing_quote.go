package service

import (
	"context"
	"errors"
	"strings"
)

const (
	VideoPricingKindFixed      = "fixed"
	VideoPricingKindUsageBased = "usage_based"
)

var (
	ErrVideoPricingUnavailable  = errors.New("video pricing is unavailable")
	ErrInvalidVideoPricingInput = errors.New("invalid video pricing input")
)

// VideoPriceQuote describes the effective price for one generated video.
// UnitPrice is the effective USD price per second and TotalPrice is the price
// for the requested duration. Token-priced channels cannot be quoted before
// generation completes.
type VideoPriceQuote struct {
	PricingKind string   `json:"pricing_kind"`
	UnitPrice   *float64 `json:"unit_price"`
	TotalPrice  *float64 `json:"total_price"`
}

// QuoteVideoPrice follows the same group, channel and video multiplier
// resolution used by RecordUsage so panel estimates stay aligned with
// settlement.
func (s *OpenAIGatewayService) QuoteVideoPrice(
	ctx context.Context,
	apiKey *APIKey,
	userID int64,
	model string,
	resolution string,
	durationSeconds int,
) (*VideoPriceQuote, error) {
	if s == nil || s.billingService == nil || apiKey == nil || apiKey.Group == nil {
		return nil, ErrVideoPricingUnavailable
	}
	model = strings.TrimSpace(model)
	normalizedResolution, ok := LookupVideoBillingResolution(resolution)
	if model == "" || !ok || durationSeconds < VideoBillingMinDurationSeconds || durationSeconds > VideoBillingMaxDurationSeconds {
		return nil, ErrInvalidVideoPricingInput
	}

	apiKey = s.apiKeyWithFreshGroupMediaPricing(ctx, apiKey)
	group := apiKey.Group
	if group == nil {
		return nil, ErrVideoPricingUnavailable
	}

	if resolved := s.resolveOpenAIChannelPricing(ctx, model, apiKey); resolved != nil && resolved.Mode == BillingModeToken {
		return &VideoPriceQuote{PricingKind: VideoPricingKindUsageBased}, nil
	}

	baseMultiplier := 1.0
	if s.cfg != nil {
		baseMultiplier = s.cfg.Default.RateMultiplier
	}
	groupID := group.ID
	if apiKey.GroupID != nil && *apiKey.GroupID > 0 {
		groupID = *apiKey.GroupID
	}
	if groupID > 0 {
		baseMultiplier = s.ResolveUserGroupRateMultiplier(ctx, userID, groupID, group.RateMultiplier)
	}

	cost := s.calculateOpenAIVideoCost(ctx, model, apiKey, &OpenAIForwardResult{
		VideoCount:           1,
		VideoResolution:      normalizedResolution,
		VideoDurationSeconds: durationSeconds,
	}, resolveVideoRateMultiplier(apiKey, baseMultiplier))
	if cost == nil {
		return nil, ErrVideoPricingUnavailable
	}
	totalPrice := cost.ActualCost
	unitPrice := totalPrice / float64(durationSeconds)
	return &VideoPriceQuote{
		PricingKind: VideoPricingKindFixed,
		UnitPrice:   &unitPrice,
		TotalPrice:  &totalPrice,
	}, nil
}
