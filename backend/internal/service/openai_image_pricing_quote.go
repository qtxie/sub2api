package service

import (
	"context"
	"errors"
)

const (
	ImagePricingKindFixed      = "fixed"
	ImagePricingKindUsageBased = "usage_based"
)

var ErrImagePricingUnavailable = errors.New("image pricing is unavailable")

// ImageUnitPriceQuote describes the effective price for one generated image.
// Token-priced channels cannot be quoted before generation completes.
type ImageUnitPriceQuote struct {
	PricingKind string   `json:"pricing_kind"`
	UnitPrice   *float64 `json:"unit_price"`
}

// QuoteImageUnitPrice follows the same group, channel and multiplier resolution
// used by RecordUsage so the panel estimate stays aligned with settlement.
func (s *OpenAIGatewayService) QuoteImageUnitPrice(
	ctx context.Context,
	apiKey *APIKey,
	userID int64,
	model string,
	size string,
) (*ImageUnitPriceQuote, error) {
	if s == nil || s.billingService == nil || apiKey == nil || apiKey.Group == nil {
		return nil, ErrImagePricingUnavailable
	}

	apiKey = s.apiKeyWithFreshGroupMediaPricing(ctx, apiKey)
	group := apiKey.Group
	if group == nil {
		return nil, ErrImagePricingUnavailable
	}

	if resolved := s.resolveOpenAIChannelPricing(ctx, model, apiKey); resolved != nil && resolved.Mode == BillingModeToken {
		return &ImageUnitPriceQuote{PricingKind: ImagePricingKindUsageBased}, nil
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

	cost := s.calculateOpenAIImageCost(ctx, model, apiKey, &OpenAIForwardResult{
		ImageCount: 1,
		ImageSize:  NormalizeImageBillingTierOrDefault(size),
	}, resolveImageRateMultiplier(apiKey, baseMultiplier))
	if cost == nil {
		return nil, ErrImagePricingUnavailable
	}
	price := cost.ActualCost
	return &ImageUnitPriceQuote{PricingKind: ImagePricingKindFixed, UnitPrice: &price}, nil
}
