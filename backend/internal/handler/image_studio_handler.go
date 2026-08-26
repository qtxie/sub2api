package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const (
	imageStudioModel                  = "gpt-image-2"
	imageStudioDefaultGeminiModel     = "gemini-3.1-flash-image"
	imageStudioDefaultGrokModel       = "grok-imagine-image-2.0"
	imageStudioOpenAIMaxOutputCount   = 4
	imageStudioGrokMaxOutputCount     = 10
	imageStudioOpenAIMaxInputImages   = 16
	imageStudioGeminiMaxInputImages   = 14
	imageStudioGemini25MaxInputImages = 3
	imageStudioGrokMaxInputImages     = 3
	imageStudioMaxSourceImageBytes    = 6 << 20
	imageStudioMaxSourceImagesBytes   = 14 << 20
	imageStudioMaxRequestBytes        = 20 << 20
	imageStudioMaxOutputCount         = imageStudioGrokMaxOutputCount
	imageStudioPerImageResponseSize   = int64(48 << 20)
	imageStudioResponseOverhead       = int64(2 << 20)
	imageStudioHeartbeatInterval      = 15 * time.Second
)

var (
	imageStudioPricingSizes        = []string{"1024x1024", "1536x1024", "1024x1536", "2048x2048", "2048x1152", "1152x2048", "3840x2160", "2160x3840"}
	errImageStudioResponseTooLarge = errors.New("image gateway response is too large")
)

type imageStudioAPIKeyLoader interface {
	GetByID(context.Context, int64) (*service.APIKey, error)
}

type imageStudioPricingQuoter interface {
	QuoteImageUnitPrice(context.Context, *service.APIKey, int64, string, string) (*service.ImageUnitPriceQuote, error)
}

// ImageStudioHandler proxies panel requests through the matching local gateway
// so API key secrets never leave the backend.
type ImageStudioHandler struct {
	apiKeys       imageStudioAPIKeyLoader
	pricingQuoter imageStudioPricingQuoter
	cfg           *config.Config
	httpClient    *http.Client
}

func NewImageStudioHandler(apiKeys *service.APIKeyService, openAI *service.OpenAIGatewayService, cfg *config.Config) *ImageStudioHandler {
	return &ImageStudioHandler{
		apiKeys:       apiKeys,
		pricingQuoter: openAI,
		cfg:           cfg,
		httpClient:    &http.Client{},
	}
}

type imageStudioGenerationRequest struct {
	APIKeyID      int64                    `json:"api_key_id"`
	Model         string                   `json:"model"`
	Prompt        string                   `json:"prompt"`
	Size          string                   `json:"size"`
	AspectRatio   string                   `json:"aspect_ratio"`
	ImageSize     string                   `json:"image_size"`
	Resolution    string                   `json:"resolution"`
	Quality       string                   `json:"quality"`
	Background    string                   `json:"background"`
	OutputFormat  string                   `json:"output_format"`
	OutputCount   int                      `json:"n"`
	SourceImages  []imageStudioSourceImage `json:"source_images"`
	presentFields map[string]bool
}

type imageStudioSourceImage struct {
	MIMEType string `json:"mime_type"`
	Data     string `json:"data"`
}

type imageStudioPricingRequest struct {
	APIKeyID int64  `json:"api_key_id"`
	Model    string `json:"model"`
}

type imageStudioCapabilitiesRequest struct {
	APIKeyID int64 `json:"api_key_id"`
}

type imageStudioModelCapability struct {
	ID                 string   `json:"id"`
	Label              string   `json:"label"`
	AspectRatios       []string `json:"aspect_ratios"`
	ImageSizes         []string `json:"image_sizes"`
	Resolutions        []string `json:"resolutions"`
	Qualities          []string `json:"qualities"`
	MaxImages          int      `json:"max_images"`
	SupportsCustomSize bool     `json:"supports_custom_size"`
	OutputFormats      []string `json:"output_formats"`
	Backgrounds        []string `json:"backgrounds"`
	MaxInputImages     int      `json:"max_input_images"`
}

type imageStudioCapabilitiesResponse struct {
	Provider     string                       `json:"provider"`
	DefaultModel string                       `json:"default_model"`
	Models       []imageStudioModelCapability `json:"models"`
}

type imageStudioResolutionPrice struct {
	Size         string   `json:"size"`
	BillingTier  string   `json:"billing_tier"`
	PricingKind  string   `json:"pricing_kind"`
	UnitPriceUSD *float64 `json:"unit_price"`
	Model        string   `json:"model,omitempty"`
	ImageSize    string   `json:"image_size,omitempty"`
	Resolution   string   `json:"resolution,omitempty"`
}

type imageStudioPricingResponse struct {
	Currency    string                       `json:"currency"`
	PricingKind string                       `json:"pricing_kind"`
	Prices      []imageStudioResolutionPrice `json:"prices"`
	Provider    string                       `json:"provider"`
	Model       string                       `json:"model"`
}

var imageStudioCapabilities = map[string]imageStudioCapabilitiesResponse{
	service.PlatformOpenAI: {
		Provider: service.PlatformOpenAI, DefaultModel: imageStudioModel,
		Models: []imageStudioModelCapability{{
			ID: imageStudioModel, Label: "GPT Image 2",
			AspectRatios: []string{}, ImageSizes: imageStudioPricingSizes, Resolutions: []string{},
			Qualities: []string{"auto", "low", "medium", "high"}, MaxImages: imageStudioOpenAIMaxOutputCount,
			SupportsCustomSize: true, OutputFormats: []string{"png", "jpeg", "webp"}, Backgrounds: []string{"auto", "opaque", "transparent"},
			MaxInputImages: imageStudioOpenAIMaxInputImages,
		}},
	},
	service.PlatformGemini: {
		Provider: service.PlatformGemini, DefaultModel: imageStudioDefaultGeminiModel,
		Models: []imageStudioModelCapability{
			{
				ID: imageStudioDefaultGeminiModel, Label: "Gemini 3.1 Flash Image",
				AspectRatios: []string{"1:1", "1:4", "1:8", "2:3", "3:2", "3:4", "4:1", "4:3", "4:5", "5:4", "8:1", "9:16", "16:9", "21:9"},
				ImageSizes:   []string{"1K", "2K", "4K"}, Resolutions: []string{}, Qualities: []string{}, MaxImages: 1,
				SupportsCustomSize: false, OutputFormats: []string{}, Backgrounds: []string{},
				MaxInputImages: imageStudioGeminiMaxInputImages,
			},
			{
				ID: "gemini-3.1-flash-lite-image", Label: "Gemini 3.1 Flash Lite Image",
				AspectRatios: imageStudioGeminiCommonAspectRatios(), ImageSizes: []string{"1K"}, Resolutions: []string{}, Qualities: []string{}, MaxImages: 1,
				SupportsCustomSize: false, OutputFormats: []string{}, Backgrounds: []string{},
				MaxInputImages: imageStudioGeminiMaxInputImages,
			},
			{
				ID: "gemini-3-pro-image", Label: "Gemini 3 Pro Image",
				AspectRatios: imageStudioGeminiCommonAspectRatios(), ImageSizes: []string{"1K", "2K", "4K"}, Resolutions: []string{}, Qualities: []string{}, MaxImages: 1,
				SupportsCustomSize: false, OutputFormats: []string{}, Backgrounds: []string{},
				MaxInputImages: imageStudioGeminiMaxInputImages,
			},
			{
				ID: "gemini-2.5-flash-image", Label: "Gemini 2.5 Flash Image",
				AspectRatios: imageStudioGeminiCommonAspectRatios(), ImageSizes: []string{}, Resolutions: []string{}, Qualities: []string{}, MaxImages: 1,
				SupportsCustomSize: false, OutputFormats: []string{}, Backgrounds: []string{},
				MaxInputImages: imageStudioGemini25MaxInputImages,
			},
		},
	},
	service.PlatformGrok: {
		Provider: service.PlatformGrok, DefaultModel: imageStudioDefaultGrokModel,
		Models: []imageStudioModelCapability{{
			ID: imageStudioDefaultGrokModel, Label: "Grok Imagine Image 2.0",
			AspectRatios: []string{"auto", "1:1", "16:9", "9:16", "4:3", "3:4", "3:2", "2:3", "2:1", "1:2", "19.5:9", "9:19.5", "20:9", "9:20"},
			ImageSizes:   []string{}, Resolutions: []string{"1k", "2k"}, Qualities: []string{"medium", "low"}, MaxImages: imageStudioGrokMaxOutputCount,
			SupportsCustomSize: false, OutputFormats: []string{}, Backgrounds: []string{},
			MaxInputImages: imageStudioGrokMaxInputImages,
		}},
	},
}

func (input *imageStudioGenerationRequest) UnmarshalJSON(data []byte) error {
	type requestAlias imageStudioGenerationRequest
	var decoded requestAlias
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}

	var rawFields map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawFields); err != nil {
		return err
	}
	*input = imageStudioGenerationRequest(decoded)
	input.presentFields = make(map[string]bool, len(rawFields))
	for field := range rawFields {
		input.presentFields[field] = true
	}
	return nil
}

func (h *ImageStudioHandler) Capabilities(c *gin.Context) {
	subject, ok := servermiddleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if h == nil || h.apiKeys == nil {
		response.InternalError(c, "Image Studio is not configured")
		return
	}

	var input imageStudioCapabilitiesRequest
	if err := c.ShouldBindJSON(&input); err != nil || input.APIKeyID <= 0 {
		response.BadRequest(c, "API key is required")
		return
	}
	apiKey, ok := h.loadEligibleAPIKey(c, input.APIKeyID, subject.UserID)
	if !ok {
		return
	}
	capabilities, ok := imageStudioCapabilitiesForProvider(apiKey.Group.Platform)
	if !ok {
		response.BadRequest(c, "Image Studio does not support this API key platform")
		return
	}
	response.Success(c, capabilities)
}

func (h *ImageStudioHandler) Pricing(c *gin.Context) {
	subject, ok := servermiddleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if h == nil || h.apiKeys == nil || h.pricingQuoter == nil {
		response.InternalError(c, "Image pricing service is not configured")
		return
	}

	var input imageStudioPricingRequest
	if err := c.ShouldBindJSON(&input); err != nil || input.APIKeyID <= 0 {
		response.BadRequest(c, "API key is required")
		return
	}
	apiKey, ok := h.loadEligibleAPIKey(c, input.APIKeyID, subject.UserID)
	if !ok {
		return
	}
	providerCapabilities, ok := imageStudioCapabilitiesForProvider(apiKey.Group.Platform)
	if !ok {
		response.BadRequest(c, "Image Studio does not support this API key platform")
		return
	}
	model := strings.TrimSpace(input.Model)
	if model == "" {
		model = providerCapabilities.DefaultModel
	}
	modelCapability, ok := imageStudioCapabilityForModel(providerCapabilities, model)
	if !ok {
		response.BadRequest(c, "Unsupported image model")
		return
	}

	pricingOptions := imageStudioPricingOptions(apiKey.Group.Platform, modelCapability)
	prices := make([]imageStudioResolutionPrice, 0, len(pricingOptions))
	pricingKind := service.ImagePricingKindFixed
	for _, option := range pricingOptions {
		quote, err := h.pricingQuoter.QuoteImageUnitPrice(c.Request.Context(), apiKey, subject.UserID, model, option.Size)
		if err != nil {
			response.InternalError(c, "Failed to calculate image pricing")
			return
		}
		if quote.PricingKind == service.ImagePricingKindUsageBased {
			pricingKind = service.ImagePricingKindUsageBased
		}
		prices = append(prices, imageStudioResolutionPrice{
			Size:         option.Size,
			BillingTier:  service.NormalizeImageBillingTierOrDefault(option.Size),
			PricingKind:  quote.PricingKind,
			UnitPriceUSD: quote.UnitPrice,
			Model:        model,
			ImageSize:    option.ImageSize,
			Resolution:   option.Resolution,
		})
	}

	response.Success(c, imageStudioPricingResponse{
		Currency: "USD", PricingKind: pricingKind, Prices: prices,
		Provider: apiKey.Group.Platform, Model: model,
	})
}

func (h *ImageStudioHandler) Generate(c *gin.Context) {
	subject, ok := servermiddleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if h == nil || h.apiKeys == nil || h.httpClient == nil {
		response.InternalError(c, "Image Studio is not configured")
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, imageStudioMaxRequestBytes)
	var input imageStudioGenerationRequest
	if err := c.ShouldBindJSON(&input); err != nil {
		if _, ok := extractMaxBytesError(err); ok {
			response.Error(c, http.StatusRequestEntityTooLarge, "Image Studio request is too large")
			return
		}
		response.BadRequest(c, "Invalid image generation request")
		return
	}
	input.Prompt = strings.TrimSpace(input.Prompt)
	input.Model = strings.TrimSpace(input.Model)
	if message := validateImageStudioBaseInput(input); message != "" {
		response.BadRequest(c, message)
		return
	}
	apiKey, ok := h.loadEligibleAPIKey(c, input.APIKeyID, subject.UserID)
	if !ok {
		return
	}
	providerCapabilities, ok := imageStudioCapabilitiesForProvider(apiKey.Group.Platform)
	if !ok {
		response.BadRequest(c, "Image Studio does not support this API key platform")
		return
	}
	if input.Model == "" {
		input.Model = providerCapabilities.DefaultModel
	}
	modelCapability, ok := imageStudioCapabilityForModel(providerCapabilities, input.Model)
	if !ok {
		response.BadRequest(c, "Unsupported image model")
		return
	}
	if message := validateImageStudioProviderFields(input, apiKey.Group.Platform); message != "" {
		response.BadRequest(c, message)
		return
	}
	normalizeImageStudioInput(&input, apiKey.Group.Platform, modelCapability)
	if message := validateImageStudioInput(input, apiKey.Group.Platform, modelCapability); message != "" {
		response.BadRequest(c, message)
		return
	}

	switch apiKey.Group.Platform {
	case service.PlatformOpenAI:
		h.generateImageStudioOpenAI(c, apiKey, input)
	case service.PlatformGemini:
		h.generateImageStudioGemini(c, apiKey, input)
	case service.PlatformGrok:
		h.generateImageStudioGrok(c, apiKey, input)
	default:
		response.BadRequest(c, "Image Studio does not support this API key platform")
	}
}

func (h *ImageStudioHandler) loadEligibleAPIKey(c *gin.Context, apiKeyID, userID int64) (*service.APIKey, bool) {
	apiKey, err := h.apiKeys.GetByID(c.Request.Context(), apiKeyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return nil, false
	}
	if apiKey == nil || apiKey.UserID != userID {
		response.NotFound(c, "API key not found")
		return nil, false
	}
	if !apiKey.IsActive() {
		response.BadRequest(c, "API key is not active")
		return nil, false
	}
	if apiKey.Group == nil || !imageStudioPlatformSupported(apiKey.Group.Platform) {
		response.BadRequest(c, "Image Studio requires an OpenAI, Gemini, or Grok API key")
		return nil, false
	}
	if !service.GroupAllowsImageGeneration(apiKey.Group) {
		response.Forbidden(c, service.ImageGenerationPermissionMessage())
		return nil, false
	}
	return apiKey, true
}

func normalizeImageStudioInput(input *imageStudioGenerationRequest, provider string, capability imageStudioModelCapability) {
	input.Model = strings.TrimSpace(input.Model)
	input.Size = strings.ToLower(strings.TrimSpace(input.Size))
	input.AspectRatio = strings.TrimSpace(input.AspectRatio)
	input.ImageSize = strings.TrimSpace(input.ImageSize)
	input.Resolution = strings.ToLower(strings.TrimSpace(input.Resolution))
	input.Quality = strings.ToLower(strings.TrimSpace(input.Quality))
	input.Background = strings.ToLower(strings.TrimSpace(input.Background))
	input.OutputFormat = strings.ToLower(strings.TrimSpace(input.OutputFormat))
	for index := range input.SourceImages {
		input.SourceImages[index].MIMEType = strings.ToLower(strings.TrimSpace(input.SourceImages[index].MIMEType))
		input.SourceImages[index].Data = strings.TrimSpace(input.SourceImages[index].Data)
	}
	switch provider {
	case service.PlatformOpenAI:
		if input.Size == "" {
			input.Size = "auto"
		}
		if input.Quality == "" {
			input.Quality = "auto"
		}
		if input.Background == "" {
			input.Background = "auto"
		}
		if input.OutputFormat == "" {
			input.OutputFormat = "png"
		}
		if input.OutputCount == 0 {
			input.OutputCount = 1
		}
	case service.PlatformGemini:
		if input.AspectRatio == "" {
			input.AspectRatio = "1:1"
		}
		if input.ImageSize == "" && len(capability.ImageSizes) > 0 {
			input.ImageSize = capability.ImageSizes[0]
		}
		input.OutputCount = 1
	case service.PlatformGrok:
		if input.AspectRatio == "" {
			input.AspectRatio = "auto"
		}
		if input.Resolution == "" {
			input.Resolution = "1k"
		}
		if input.Quality == "" {
			input.Quality = "medium"
		}
		if input.OutputCount == 0 {
			input.OutputCount = 1
		}
	}
}

func validateImageStudioBaseInput(input imageStudioGenerationRequest) string {
	if input.APIKeyID <= 0 {
		return "API key is required"
	}
	if input.Prompt == "" {
		return "Prompt is required"
	}
	return ""
}

func validateImageStudioInput(input imageStudioGenerationRequest, provider string, capability imageStudioModelCapability) string {
	if message := validateImageStudioSourceImages(input.SourceImages, capability.MaxInputImages); message != "" {
		return message
	}
	switch provider {
	case service.PlatformOpenAI:
		if input.OutputCount < 1 || input.OutputCount > imageStudioOpenAIMaxOutputCount {
			return "Image count must be between 1 and 4"
		}
		if !validGPTImage2Size(input.Size) {
			return "Unsupported image size"
		}
		if !imageStudioOptionAllowed(input.Quality, capability.Qualities...) {
			return "Unsupported image quality"
		}
		if !imageStudioOptionAllowed(input.Background, capability.Backgrounds...) {
			return "Unsupported image background"
		}
		if !imageStudioOptionAllowed(input.OutputFormat, capability.OutputFormats...) {
			return "Unsupported image output format"
		}
		if input.Background == "transparent" && input.OutputFormat == "jpeg" {
			return "Transparent backgrounds require PNG or WebP output"
		}
	case service.PlatformGemini:
		if !imageStudioOptionAllowed(input.AspectRatio, capability.AspectRatios...) {
			return "Unsupported image aspect ratio"
		}
		if len(capability.ImageSizes) == 0 {
			if input.ImageSize != "" || input.fieldWasProvided("image_size") {
				return "Image size is not supported by this Gemini model"
			}
		} else if !imageStudioOptionAllowed(input.ImageSize, capability.ImageSizes...) {
			return "Unsupported image size"
		}
	case service.PlatformGrok:
		if input.OutputCount < 1 || input.OutputCount > imageStudioGrokMaxOutputCount {
			return "Image count must be between 1 and 10"
		}
		if !imageStudioOptionAllowed(input.AspectRatio, capability.AspectRatios...) {
			return "Unsupported image aspect ratio"
		}
		if !imageStudioOptionAllowed(input.Resolution, capability.Resolutions...) {
			return "Unsupported image resolution"
		}
		if !imageStudioOptionAllowed(input.Quality, capability.Qualities...) {
			return "Unsupported image quality"
		}
	default:
		return "Unsupported image provider"
	}
	return ""
}

func validateImageStudioSourceImages(images []imageStudioSourceImage, maxImages int) string {
	if len(images) == 0 {
		return ""
	}
	if maxImages <= 0 {
		return "Source images are not supported by this model"
	}
	if len(images) > maxImages {
		return fmt.Sprintf("A maximum of %d source images is supported by this model", maxImages)
	}

	totalBytes := 0
	for index, image := range images {
		position := index + 1
		if !imageStudioSourceMIMETypeSupported(image.MIMEType) {
			return fmt.Sprintf("Source image %d must use image/png, image/jpeg, or image/webp", position)
		}
		if image.Data == "" {
			return fmt.Sprintf("Source image %d data is required", position)
		}
		if strings.HasPrefix(strings.ToLower(image.Data), "data:") {
			return fmt.Sprintf("Source image %d must contain base64 data without a data URL prefix", position)
		}
		if base64.StdEncoding.DecodedLen(len(image.Data)) > imageStudioMaxSourceImageBytes+2 {
			return fmt.Sprintf("Source image %d exceeds the 6 MB limit", position)
		}
		decoded, err := base64.StdEncoding.Strict().DecodeString(image.Data)
		if err != nil || len(decoded) == 0 {
			return fmt.Sprintf("Source image %d must contain valid base64 data", position)
		}
		if len(decoded) > imageStudioMaxSourceImageBytes {
			return fmt.Sprintf("Source image %d exceeds the 6 MB limit", position)
		}
		if !imageStudioSourceContentMatchesMIMEType(decoded, image.MIMEType) {
			return fmt.Sprintf("Source image %d content does not match its MIME type", position)
		}
		totalBytes += len(decoded)
		if totalBytes > imageStudioMaxSourceImagesBytes {
			return "Source images exceed the 14 MB total limit"
		}
	}
	return ""
}

func imageStudioSourceMIMETypeSupported(mimeType string) bool {
	switch mimeType {
	case "image/png", "image/jpeg", "image/webp":
		return true
	default:
		return false
	}
}

func imageStudioSourceContentMatchesMIMEType(data []byte, mimeType string) bool {
	switch mimeType {
	case "image/png":
		return len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	case "image/jpeg":
		return len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff
	case "image/webp":
		return len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP"))
	default:
		return false
	}
}

func validateImageStudioProviderFields(input imageStudioGenerationRequest, provider string) string {
	var unsupported []string
	operation := "image generation"
	if len(input.SourceImages) > 0 {
		operation = "image editing"
	}
	switch provider {
	case service.PlatformOpenAI:
		unsupported = []string{"aspect_ratio", "image_size", "resolution"}
	case service.PlatformGemini:
		unsupported = []string{"size", "quality", "background", "output_format", "n", "resolution"}
	case service.PlatformGrok:
		unsupported = []string{"size", "image_size", "background", "output_format"}
		if len(input.SourceImages) > 0 {
			unsupported = append(unsupported, "resolution", "quality", "n")
			if len(input.SourceImages) == 1 {
				unsupported = append(unsupported, "aspect_ratio")
			}
		}
	default:
		return "Unsupported image provider"
	}
	for _, field := range unsupported {
		if input.fieldWasProvided(field) {
			return fmt.Sprintf("Field %q is not supported for %s %s", field, provider, operation)
		}
	}
	return ""
}

func (input imageStudioGenerationRequest) fieldWasProvided(field string) bool {
	if input.presentFields != nil {
		return input.presentFields[field]
	}
	switch field {
	case "size":
		return input.Size != ""
	case "aspect_ratio":
		return input.AspectRatio != ""
	case "image_size":
		return input.ImageSize != ""
	case "resolution":
		return input.Resolution != ""
	case "quality":
		return input.Quality != ""
	case "background":
		return input.Background != ""
	case "output_format":
		return input.OutputFormat != ""
	case "n":
		return input.OutputCount != 0
	case "source_images":
		return len(input.SourceImages) > 0
	default:
		return false
	}
}

func imageStudioPlatformSupported(platform string) bool {
	_, ok := imageStudioCapabilities[platform]
	return ok
}

func imageStudioCapabilitiesForProvider(provider string) (imageStudioCapabilitiesResponse, bool) {
	capabilities, ok := imageStudioCapabilities[provider]
	return capabilities, ok
}

func imageStudioCapabilityForModel(capabilities imageStudioCapabilitiesResponse, model string) (imageStudioModelCapability, bool) {
	for _, capability := range capabilities.Models {
		if capability.ID == model {
			return capability, true
		}
	}
	return imageStudioModelCapability{}, false
}

func imageStudioGeminiCommonAspectRatios() []string {
	return []string{"1:1", "2:3", "3:2", "3:4", "4:3", "4:5", "5:4", "9:16", "16:9", "21:9"}
}

type imageStudioPricingOption struct {
	Size       string
	ImageSize  string
	Resolution string
}

func imageStudioPricingOptions(provider string, capability imageStudioModelCapability) []imageStudioPricingOption {
	switch provider {
	case service.PlatformOpenAI:
		options := make([]imageStudioPricingOption, 0, len(imageStudioPricingSizes))
		for _, size := range imageStudioPricingSizes {
			options = append(options, imageStudioPricingOption{Size: size})
		}
		return options
	case service.PlatformGemini:
		imageSizes := capability.ImageSizes
		if len(imageSizes) == 0 {
			imageSizes = []string{"1K"}
		}
		options := make([]imageStudioPricingOption, 0, len(imageSizes))
		for _, imageSize := range imageSizes {
			options = append(options, imageStudioPricingOption{Size: imageSize, ImageSize: imageSize})
		}
		return options
	case service.PlatformGrok:
		options := make([]imageStudioPricingOption, 0, len(capability.Resolutions))
		for _, resolution := range capability.Resolutions {
			options = append(options, imageStudioPricingOption{Size: strings.ToUpper(resolution), Resolution: resolution})
		}
		return options
	default:
		return nil
	}
}

func (h *ImageStudioHandler) generateImageStudioOpenAI(c *gin.Context, apiKey *service.APIKey, input imageStudioGenerationRequest) {
	payload := map[string]any{
		"model":         imageStudioModel,
		"prompt":        input.Prompt,
		"n":             input.OutputCount,
		"stream":        true,
		"size":          input.Size,
		"quality":       input.Quality,
		"background":    input.Background,
		"output_format": input.OutputFormat,
	}
	path := "/v1/images/generations"
	if len(input.SourceImages) > 0 {
		path = "/v1/images/edits"
		payload["images"] = imageStudioOpenAIImageReferences(input.SourceImages)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		response.InternalError(c, "Failed to build image generation request")
		return
	}
	request, ok := h.newImageStudioGatewayRequest(c, path, body)
	if !ok {
		return
	}
	request.Header.Set("Authorization", "Bearer "+apiKey.Key)
	request.Header.Set("Accept", "text/event-stream, application/json")
	h.forwardImageStudioRequest(c, request, input.OutputCount, true, nil)
}

func (h *ImageStudioHandler) generateImageStudioGrok(c *gin.Context, apiKey *service.APIKey, input imageStudioGenerationRequest) {
	payload := map[string]any{
		"model":  input.Model,
		"prompt": input.Prompt,
	}
	path := "/v1/images/generations"
	outputCount := input.OutputCount
	if len(input.SourceImages) == 0 {
		payload["n"] = input.OutputCount
		payload["aspect_ratio"] = input.AspectRatio
		payload["resolution"] = input.Resolution
		payload["quality"] = input.Quality
		payload["response_format"] = "b64_json"
	} else {
		path = "/v1/images/edits"
		outputCount = 1
		payload["response_format"] = "b64_json"
		references := imageStudioGrokImageReferences(input.SourceImages)
		if len(references) == 1 {
			payload["image"] = references[0]
		} else {
			payload["images"] = references
			if input.AspectRatio != "auto" {
				payload["aspect_ratio"] = input.AspectRatio
			}
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		response.InternalError(c, "Failed to build image generation request")
		return
	}
	request, ok := h.newImageStudioGatewayRequest(c, path, body)
	if !ok {
		return
	}
	request.Header.Set("Authorization", "Bearer "+apiKey.Key)
	request.Header.Set("Accept", "application/json")
	h.forwardImageStudioRequest(c, request, outputCount, false, nil)
}

func (h *ImageStudioHandler) generateImageStudioGemini(c *gin.Context, apiKey *service.APIKey, input imageStudioGenerationRequest) {
	responseFormat := map[string]any{
		"type":         "image",
		"aspect_ratio": input.AspectRatio,
	}
	if input.ImageSize != "" {
		responseFormat["image_size"] = input.ImageSize
	}
	interactionInput := any(input.Prompt)
	if len(input.SourceImages) > 0 {
		parts := make([]map[string]any, 0, len(input.SourceImages)+1)
		parts = append(parts, map[string]any{"type": "text", "text": input.Prompt})
		for _, source := range input.SourceImages {
			parts = append(parts, map[string]any{
				"type":      "image",
				"mime_type": source.MIMEType,
				"data":      source.Data,
			})
		}
		interactionInput = parts
	}
	body, err := json.Marshal(map[string]any{
		"model":           input.Model,
		"input":           interactionInput,
		"response_format": responseFormat,
		"store":           false,
	})
	if err != nil {
		response.InternalError(c, "Failed to build image generation request")
		return
	}
	request, ok := h.newImageStudioGatewayRequest(c, "/v1beta/interactions", body)
	if !ok {
		return
	}
	request.Header.Set("x-goog-api-key", apiKey.Key)
	request.Header.Set("Accept", "application/json")
	h.forwardImageStudioRequest(c, request, 1, false, normalizeImageStudioGeminiInteractionResponse)
}

func imageStudioOpenAIImageReferences(images []imageStudioSourceImage) []map[string]string {
	references := make([]map[string]string, 0, len(images))
	for _, image := range images {
		references = append(references, map[string]string{
			"image_url": imageStudioSourceImageDataURL(image),
		})
	}
	return references
}

func imageStudioGrokImageReferences(images []imageStudioSourceImage) []map[string]string {
	references := make([]map[string]string, 0, len(images))
	for _, image := range images {
		references = append(references, map[string]string{
			"type": "image_url",
			"url":  imageStudioSourceImageDataURL(image),
		})
	}
	return references
}

func imageStudioSourceImageDataURL(image imageStudioSourceImage) string {
	return "data:" + image.MIMEType + ";base64," + image.Data
}

func (h *ImageStudioHandler) newImageStudioGatewayRequest(c *gin.Context, path string, body []byte) (*http.Request, bool) {
	gatewayURL, err := imageStudioLocalGatewayURL(h.cfg, path)
	if err != nil {
		response.InternalError(c, "Image gateway is not configured")
		return nil, false
	}
	request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, gatewayURL, bytes.NewReader(body))
	if err != nil {
		response.InternalError(c, "Failed to create image generation request")
		return nil, false
	}
	request.Header.Set("Content-Type", "application/json")
	copyImageStudioRequestHeader(c, request, "Accept-Language")
	copyImageStudioRequestHeader(c, request, "User-Agent")
	return request, true
}

type imageStudioResponseNormalizer func([]byte) (any, error)

func (h *ImageStudioHandler) forwardImageStudioRequest(
	c *gin.Context,
	request *http.Request,
	outputCount int,
	allowStream bool,
	normalize imageStudioResponseNormalizer,
) {
	upstreamResp, err := h.httpClient.Do(request)
	if err != nil {
		response.InternalError(c, "Image gateway request failed")
		return
	}
	defer upstreamResp.Body.Close()
	maxBytes := imageStudioMaxResponseBytes(outputCount)
	if allowStream && upstreamResp.StatusCode < http.StatusBadRequest && isImageStudioEventStream(upstreamResp.Header.Get("Content-Type")) {
		if err := relayImageStudioStream(c, upstreamResp.Body, upstreamResp.StatusCode, maxBytes); err != nil && !c.Writer.Written() {
			response.InternalError(c, "Failed to relay image gateway stream")
		}
		return
	}

	upstreamBody, err := readImageStudioResponse(upstreamResp.Body, maxBytes)
	if err != nil {
		if errors.Is(err, errImageStudioResponseTooLarge) {
			response.Error(c, http.StatusBadGateway, "Image gateway response is too large")
			return
		}
		response.InternalError(c, "Failed to read image gateway response")
		return
	}
	if upstreamResp.StatusCode >= http.StatusBadRequest {
		message := imageStudioGatewayErrorMessage(upstreamBody)
		if message == "" {
			message = "Image gateway request failed"
		}
		response.Error(c, upstreamResp.StatusCode, message)
		return
	}

	var output any
	if normalize != nil {
		output, err = normalize(upstreamBody)
	} else {
		err = json.Unmarshal(upstreamBody, &output)
	}
	if err != nil {
		response.Error(c, http.StatusBadGateway, err.Error())
		return
	}
	response.Success(c, output)
}

type imageStudioImage struct {
	B64JSON       string `json:"b64_json,omitempty"`
	URL           string `json:"url,omitempty"`
	MIMEType      string `json:"mime_type,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
}

func normalizeImageStudioGeminiInteractionResponse(body []byte) (any, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, errors.New("Image gateway returned an invalid response")
	}
	if wrapped, ok := payload["response"].(map[string]any); ok {
		payload = wrapped
	}

	images := make([]imageStudioImage, 0, 1)
	steps, _ := payload["steps"].([]any)
	for _, rawStep := range steps {
		step, _ := rawStep.(map[string]any)
		stepType, _ := step["type"].(string)
		if !strings.EqualFold(strings.TrimSpace(stepType), "model_output") {
			continue
		}
		content, _ := step["content"].([]any)
		images = append(images, imageStudioInteractionImages(content)...)
	}
	if len(images) == 0 {
		if output, ok := payload["output"].([]any); ok {
			images = append(images, imageStudioInteractionImages(output)...)
		}
	}
	if len(images) == 0 {
		if outputImage, ok := payload["output_image"].(map[string]any); ok {
			if image, ok := imageStudioInteractionImage(outputImage, ""); ok {
				images = append(images, image)
			}
		}
	}
	if len(images) == 0 {
		return nil, errors.New("Image gateway returned no usable images")
	}

	output := map[string]any{"data": images}
	if created, ok := payload["created_at"].(float64); ok {
		output["created"] = created
	}
	return output, nil
}

func imageStudioInteractionImages(content []any) []imageStudioImage {
	texts := make([]string, 0, 1)
	for _, rawBlock := range content {
		block, _ := rawBlock.(map[string]any)
		if text, _ := block["text"].(string); strings.TrimSpace(text) != "" {
			texts = append(texts, strings.TrimSpace(text))
		}
	}
	revisedPrompt := strings.Join(texts, "\n")
	images := make([]imageStudioImage, 0, 1)
	for _, rawBlock := range content {
		block, _ := rawBlock.(map[string]any)
		if image, ok := imageStudioInteractionImage(block, revisedPrompt); ok {
			images = append(images, image)
		}
	}
	return images
}

func imageStudioInteractionImage(block map[string]any, revisedPrompt string) (imageStudioImage, bool) {
	if block == nil {
		return imageStudioImage{}, false
	}
	blockType, _ := block["type"].(string)
	blockType = strings.ToLower(strings.TrimSpace(blockType))
	data, _ := block["data"].(string)
	mimeType, _ := block["mime_type"].(string)
	hasInlineData := false
	if inline, ok := block["inlineData"].(map[string]any); ok {
		hasInlineData = true
		data, _ = inline["data"].(string)
		mimeType, _ = inline["mimeType"].(string)
	}
	if inline, ok := block["inline_data"].(map[string]any); ok {
		hasInlineData = true
		data, _ = inline["data"].(string)
		mimeType, _ = inline["mime_type"].(string)
	}
	if strings.TrimSpace(data) != "" {
		if blockType != "image" && !hasInlineData {
			return imageStudioImage{}, false
		}
		mimeType = imageStudioNormalizeMIMEType(mimeType)
		if !imageStudioIsRasterMIMEType(mimeType) || !imageStudioValidBase64(data) {
			return imageStudioImage{}, false
		}
		return imageStudioImage{B64JSON: strings.TrimSpace(data), MIMEType: mimeType, RevisedPrompt: revisedPrompt}, true
	}
	if blockType != "image" {
		return imageStudioImage{}, false
	}
	uri, _ := block["uri"].(string)
	if strings.TrimSpace(uri) == "" {
		uri, _ = block["url"].(string)
	}
	if strings.TrimSpace(uri) == "" {
		return imageStudioImage{}, false
	}
	parsed, err := url.Parse(strings.TrimSpace(uri))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return imageStudioImage{}, false
	}
	return imageStudioImage{URL: parsed.String(), MIMEType: imageStudioNormalizeMIMEType(mimeType), RevisedPrompt: revisedPrompt}, true
}

func imageStudioNormalizeMIMEType(value string) string {
	value = strings.TrimSpace(value)
	if mediaType, _, err := mime.ParseMediaType(value); err == nil {
		value = mediaType
	}
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "image/jpg" {
		return "image/jpeg"
	}
	return value
}

func imageStudioIsRasterMIMEType(value string) bool {
	switch value {
	case "image/png", "image/jpeg", "image/webp":
		return true
	default:
		return false
	}
}

func imageStudioValidBase64(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if _, err := base64.StdEncoding.DecodeString(value); err == nil {
		return true
	}
	_, err := base64.RawStdEncoding.DecodeString(value)
	return err == nil
}

func validGPTImage2Size(size string) bool {
	if size == "auto" {
		return true
	}
	widthText, heightText, ok := strings.Cut(size, "x")
	if !ok || widthText == "" || heightText == "" {
		return false
	}
	width, err := strconv.Atoi(widthText)
	if err != nil {
		return false
	}
	height, err := strconv.Atoi(heightText)
	if err != nil || width <= 0 || height <= 0 {
		return false
	}
	if width%16 != 0 || height%16 != 0 || width > 3840 || height > 3840 {
		return false
	}
	shortEdge, longEdge := width, height
	if shortEdge > longEdge {
		shortEdge, longEdge = longEdge, shortEdge
	}
	if longEdge > shortEdge*3 {
		return false
	}
	pixels := int64(width) * int64(height)
	return pixels >= 655360 && pixels <= 8294400
}

func imageStudioOptionAllowed(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func imageStudioLocalGatewayURL(cfg *config.Config, path string) (string, error) {
	if cfg == nil || cfg.Server.Port <= 0 || cfg.Server.Port > 65535 {
		return "", errors.New("invalid local gateway configuration")
	}
	host := strings.TrimSpace(cfg.Server.Host)
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	if parsed := net.ParseIP(strings.Trim(host, "[]")); parsed != nil {
		host = parsed.String()
	}
	return (&url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(host, strconv.Itoa(cfg.Server.Port)),
		Path:   "/" + strings.TrimLeft(path, "/"),
	}).String(), nil
}

func imageStudioMaxResponseBytes(outputCount int) int64 {
	if outputCount < 1 {
		outputCount = 1
	}
	if outputCount > imageStudioMaxOutputCount {
		outputCount = imageStudioMaxOutputCount
	}
	return int64(outputCount)*imageStudioPerImageResponseSize + imageStudioResponseOverhead
}

func copyImageStudioRequestHeader(c *gin.Context, req *http.Request, name string) {
	if value := strings.TrimSpace(c.GetHeader(name)); value != "" {
		req.Header.Set(name, value)
	}
}

func isImageStudioEventStream(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0])
	}
	return strings.EqualFold(mediaType, "text/event-stream")
}

type imageStudioStreamRead struct {
	data []byte
	err  error
}

func relayImageStudioStream(c *gin.Context, body io.Reader, statusCode int, maxBytes int64) error {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return errors.New("streaming is not supported by response writer")
	}
	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")
	c.Status(statusCode)
	_, _ = io.WriteString(c.Writer, ": image-studio connected\n\n")
	flusher.Flush()

	reads := make(chan imageStudioStreamRead, 1)
	go func() {
		buffer := make([]byte, 32*1024)
		for {
			n, err := body.Read(buffer)
			chunk := append([]byte(nil), buffer[:n]...)
			select {
			case reads <- imageStudioStreamRead{data: chunk, err: err}:
			case <-c.Request.Context().Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()

	ticker := time.NewTicker(imageStudioHeartbeatInterval)
	defer ticker.Stop()
	var written int64
	for {
		select {
		case <-c.Request.Context().Done():
			return c.Request.Context().Err()
		case <-ticker.C:
			if _, err := io.WriteString(c.Writer, ": image-studio heartbeat\n\n"); err != nil {
				return err
			}
			flusher.Flush()
		case result := <-reads:
			if len(result.data) > 0 {
				if written+int64(len(result.data)) > maxBytes {
					message, _ := json.Marshal(map[string]any{
						"type":  "error",
						"error": map[string]string{"message": "Image gateway response is too large"},
					})
					_, _ = fmt.Fprintf(c.Writer, "event: error\ndata: %s\n\n", message)
					flusher.Flush()
					return errImageStudioResponseTooLarge
				}
				if _, err := c.Writer.Write(result.data); err != nil {
					return err
				}
				written += int64(len(result.data))
				flusher.Flush()
			}
			if result.err == io.EOF {
				return nil
			}
			if result.err != nil {
				return result.err
			}
		}
	}
}

func readImageStudioResponse(body io.Reader, maxBytes int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, errImageStudioResponseTooLarge
	}
	return data, nil
}

func imageStudioGatewayErrorMessage(body []byte) string {
	var payload struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &payload) == nil {
		if message := strings.TrimSpace(payload.Error.Message); message != "" {
			return message
		}
		return strings.TrimSpace(payload.Message)
	}
	return strings.TrimSpace(string(body))
}
