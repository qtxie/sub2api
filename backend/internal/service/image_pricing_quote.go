package service

import (
	"context"
	"errors"
	"strings"
)

const (
	ImagePricingKindFixed      = "fixed"
	ImagePricingKindUsageBased = "usage_based"
)

var ErrImagePricingUnavailable = errors.New("image pricing is unavailable")

// ImageUnitPriceQuote is the effective user-facing price for one generated image.
// UnitPrice is nil when the channel bills from measured token usage.
type ImageUnitPriceQuote struct {
	PricingKind string
	UnitPrice   *float64
}

// QuoteImageUnitPrice follows the same precedence as generated-image settlement.
func (s *GatewayService) QuoteImageUnitPrice(
	ctx context.Context,
	apiKey *APIKey,
	userID int64,
	model string,
	imageSize string,
) (*ImageUnitPriceQuote, error) {
	if s == nil || s.billingService == nil || apiKey == nil || apiKey.Group == nil {
		return nil, ErrImagePricingUnavailable
	}

	model = strings.TrimSpace(model)
	if model == "" {
		return nil, ErrImagePricingUnavailable
	}
	sizeTier := NormalizeImageBillingTierOrDefault(imageSize)

	groupID := apiKey.Group.ID
	if groupID <= 0 && apiKey.GroupID != nil {
		groupID = *apiKey.GroupID
	}
	baseMultiplier := apiKey.Group.RateMultiplier
	if groupID > 0 {
		baseMultiplier = s.getUserGroupRateMultiplier(ctx, userID, groupID, baseMultiplier)
	}
	imageMultiplier := resolveImageRateMultiplier(apiKey, baseMultiplier)

	resolved := s.resolveChannelPricing(ctx, model, apiKey)
	if resolved != nil && resolved.Mode == BillingModeToken {
		return &ImageUnitPriceQuote{PricingKind: ImagePricingKindUsageBased}, nil
	}

	groupConfig := imagePriceConfigFromAPIKey(apiKey)
	var cost *CostBreakdown
	if apiKeyHasConfiguredImagePrice(apiKey, sizeTier) {
		cost = s.billingService.CalculateImageCost(model, sizeTier, 1, groupConfig, imageMultiplier)
	} else if resolved != nil {
		var err error
		cost, err = s.billingService.CalculateCostUnified(CostInput{
			Ctx:            ctx,
			Model:          model,
			GroupID:        &groupID,
			RequestCount:   1,
			SizeTier:       sizeTier,
			RateMultiplier: imageMultiplier,
			Resolver:       s.resolver,
			Resolved:       resolved,
		})
		if err != nil {
			return nil, err
		}
	} else {
		cost = s.billingService.CalculateImageCost(model, sizeTier, 1, groupConfig, imageMultiplier)
	}
	if cost == nil {
		return nil, ErrImagePricingUnavailable
	}

	unitPrice := cost.ActualCost
	return &ImageUnitPriceQuote{
		PricingKind: ImagePricingKindFixed,
		UnitPrice:   &unitPrice,
	}, nil
}
