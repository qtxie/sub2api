package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const (
	imagePlaygroundOpenAIModel        = "gpt-image-2"
	imagePlaygroundDefaultGeminiModel = "gemini-3.1-flash-image"
	imagePlaygroundMaxOutputCount     = 4
	imagePlaygroundMaxResponseBytes   = 64 << 20
)

var errImagePlaygroundResponseTooLarge = errors.New("image gateway response is too large")

// ImagePlaygroundHandler proxies image-studio requests through the local
// gateway so a browser never receives an API key's secret value.
type ImagePlaygroundHandler struct {
	apiKeyService *service.APIKeyService
	pricingQuoter imagePlaygroundPricingQuoter
	cfg           *config.Config
	httpClient    *http.Client
}

type imagePlaygroundPricingQuoter interface {
	QuoteImageUnitPrice(context.Context, *service.APIKey, int64, string, string) (*service.ImageUnitPriceQuote, error)
}

func NewImagePlaygroundHandler(apiKeyService *service.APIKeyService, gatewayService *service.GatewayService, cfg *config.Config) *ImagePlaygroundHandler {
	return &ImagePlaygroundHandler{
		apiKeyService: apiKeyService,
		pricingQuoter: gatewayService,
		cfg:           cfg,
		httpClient:    &http.Client{},
	}
}

type imagePlaygroundGenerationRequest struct {
	APIKeyID     int64  `json:"api_key_id"`
	Model        string `json:"model"`
	Prompt       string `json:"prompt"`
	Size         string `json:"size"`
	Quality      string `json:"quality"`
	Background   string `json:"background"`
	OutputFormat string `json:"output_format"`
	OutputCount  int    `json:"n"`
}

type imagePlaygroundPricingRequest struct {
	APIKeyID int64  `json:"api_key_id"`
	Model    string `json:"model"`
}

type imagePlaygroundResolutionPrice struct {
	Size         string   `json:"size"`
	BillingTier  string   `json:"billing_tier"`
	PricingKind  string   `json:"pricing_kind"`
	UnitPriceUSD *float64 `json:"unit_price"`
}

type imagePlaygroundPricingResponse struct {
	Currency    string                           `json:"currency"`
	PricingKind string                           `json:"pricing_kind"`
	Prices      []imagePlaygroundResolutionPrice `json:"prices"`
}

var (
	imagePlaygroundBaseSizes   = []string{"1024x1024", "1536x1024", "1024x1536"}
	imagePlaygroundOpenAISizes = []string{"1024x1024", "1536x1024", "1024x1536", "3840x2160", "2160x3840"}
)

// Pricing returns effective per-image prices for every size shown by Image Studio.
func (h *ImagePlaygroundHandler) Pricing(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if h.apiKeyService == nil || h.pricingQuoter == nil {
		response.InternalError(c, "Image pricing service is not configured")
		return
	}

	var input imagePlaygroundPricingRequest
	if err := c.ShouldBindJSON(&input); err != nil || input.APIKeyID <= 0 {
		response.BadRequest(c, "API key is required")
		return
	}
	apiKey, ok := h.loadEligibleAPIKey(c, input.APIKeyID, subject.UserID)
	if !ok {
		return
	}

	model := imagePlaygroundOpenAIModel
	if apiKey.Group.Platform != service.PlatformOpenAI {
		model = strings.TrimSpace(input.Model)
		if model == "" {
			model = imagePlaygroundDefaultGeminiModel
		}
		if !isImagePlaygroundGeminiModel(model) {
			response.BadRequest(c, "Unsupported Gemini image model")
			return
		}
	}

	sizes := imagePlaygroundSizesForPlatform(apiKey.Group.Platform)
	prices := make([]imagePlaygroundResolutionPrice, 0, len(sizes))
	pricingKind := service.ImagePricingKindFixed
	for _, size := range sizes {
		billingTier := service.NormalizeImageBillingTierOrDefault(size)
		if apiKey.Group.Platform != service.PlatformOpenAI {
			// Gemini receives only an aspect ratio from this UI, so billing uses its default tier.
			billingTier = service.ImageBillingSize2K
		}
		quote, err := h.pricingQuoter.QuoteImageUnitPrice(c.Request.Context(), apiKey, subject.UserID, model, billingTier)
		if err != nil {
			response.InternalError(c, "Failed to calculate image pricing")
			return
		}
		if quote.PricingKind == service.ImagePricingKindUsageBased {
			pricingKind = service.ImagePricingKindUsageBased
		}
		prices = append(prices, imagePlaygroundResolutionPrice{
			Size:         size,
			BillingTier:  billingTier,
			PricingKind:  quote.PricingKind,
			UnitPriceUSD: quote.UnitPrice,
		})
	}

	response.Success(c, imagePlaygroundPricingResponse{
		Currency:    "USD",
		PricingKind: pricingKind,
		Prices:      prices,
	})
}

func (h *ImagePlaygroundHandler) Generate(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if h.apiKeyService == nil {
		response.InternalError(c, "API key service is not configured")
		return
	}

	var input imagePlaygroundGenerationRequest
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "Invalid image generation request")
		return
	}
	input.Prompt = strings.TrimSpace(input.Prompt)
	if input.APIKeyID <= 0 {
		response.BadRequest(c, "API key is required")
		return
	}
	if input.Prompt == "" {
		response.BadRequest(c, "Prompt is required")
		return
	}
	if input.OutputCount < 1 || input.OutputCount > imagePlaygroundMaxOutputCount {
		response.BadRequest(c, "Image count must be between 1 and 4")
		return
	}
	if message := validateImagePlaygroundOptions(input); message != "" {
		response.BadRequest(c, message)
		return
	}

	apiKey, ok := h.loadEligibleAPIKey(c, input.APIKeyID, subject.UserID)
	if !ok {
		return
	}
	if !imagePlaygroundSizeAllowed(input.Size, imagePlaygroundSizesForPlatform(apiKey.Group.Platform)) {
		response.BadRequest(c, "Unsupported image size")
		return
	}

	if apiKey.Group.Platform == service.PlatformOpenAI {
		h.generateOpenAI(c, apiKey, input)
		return
	}
	h.generateGemini(c, apiKey, input)
}

func (h *ImagePlaygroundHandler) loadEligibleAPIKey(c *gin.Context, apiKeyID, userID int64) (*service.APIKey, bool) {
	apiKey, err := h.apiKeyService.GetByID(c.Request.Context(), apiKeyID)
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
	if apiKey.Group == nil || !imagePlaygroundPlatformSupported(apiKey.Group.Platform) {
		response.BadRequest(c, "Image Studio requires an OpenAI, Gemini, or Antigravity API key")
		return nil, false
	}
	if !service.GroupAllowsImageGeneration(apiKey.Group) {
		response.Forbidden(c, service.ImageGenerationPermissionMessage())
		return nil, false
	}
	return apiKey, true
}

func (h *ImagePlaygroundHandler) generateOpenAI(c *gin.Context, apiKey *service.APIKey, input imagePlaygroundGenerationRequest) {
	body, err := buildImagePlaygroundGatewayBody(input)
	if err != nil {
		response.InternalError(c, "Failed to build image generation request")
		return
	}

	upstreamReq, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, h.localGatewayURL(c, "/v1/images/generations"), bytes.NewReader(body))
	if err != nil {
		response.InternalError(c, "Failed to create image generation request")
		return
	}
	upstreamReq.Header.Set("Authorization", "Bearer "+apiKey.Key)
	upstreamReq.Header.Set("Content-Type", "application/json")
	upstreamReq.Header.Set("Accept", "text/event-stream, application/json")
	if lang := c.GetHeader("Accept-Language"); lang != "" {
		upstreamReq.Header.Set("Accept-Language", lang)
	}
	if ua := c.GetHeader("User-Agent"); ua != "" {
		upstreamReq.Header.Set("User-Agent", ua)
	}

	upstreamResp, err := h.httpClient.Do(upstreamReq)
	if err != nil {
		response.InternalError(c, "Image gateway request failed")
		return
	}
	defer upstreamResp.Body.Close()
	if upstreamResp.StatusCode < http.StatusBadRequest && isImagePlaygroundEventStream(upstreamResp.Header.Get("Content-Type")) {
		if err := relayImagePlaygroundStream(c, upstreamResp.Body, upstreamResp.StatusCode, imagePlaygroundMaxResponseBytes); err != nil {
			if !c.Writer.Written() {
				response.InternalError(c, "Failed to relay image gateway stream")
			}
		}
		return
	}
	upstreamBody, err := readImagePlaygroundResponse(upstreamResp.Body, imagePlaygroundMaxResponseBytes)
	if err != nil {
		if errors.Is(err, errImagePlaygroundResponseTooLarge) {
			response.Error(c, http.StatusBadGateway, "Image gateway response is too large")
			return
		}
		response.InternalError(c, "Failed to read image gateway response")
		return
	}
	if upstreamResp.StatusCode >= 400 {
		message := gatewayErrorMessage(upstreamBody)
		if message == "" {
			message = "Image gateway request failed"
		}
		response.Error(c, upstreamResp.StatusCode, message)
		return
	}
	var output any
	if err := json.Unmarshal(upstreamBody, &output); err != nil {
		response.InternalError(c, "Image gateway returned an invalid response")
		return
	}
	response.Success(c, output)
}

func (h *ImagePlaygroundHandler) generateGemini(c *gin.Context, apiKey *service.APIKey, input imagePlaygroundGenerationRequest) {
	model := strings.TrimSpace(input.Model)
	if model == "" {
		model = imagePlaygroundDefaultGeminiModel
	}
	if !isImagePlaygroundGeminiModel(model) {
		response.BadRequest(c, "Unsupported Gemini image model")
		return
	}
	body, err := buildImagePlaygroundGeminiBody(input)
	if err != nil {
		response.InternalError(c, "Failed to build image generation request")
		return
	}

	images := make([]imagePlaygroundImage, 0, input.OutputCount)
	for len(images) < input.OutputCount {
		pathPrefix := "/v1beta"
		if apiKey.Group.Platform == service.PlatformAntigravity {
			pathPrefix = "/antigravity/v1beta"
		}
		path := pathPrefix + "/models/" + model + ":generateContent"
		upstreamReq, requestErr := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, h.localGatewayURL(c, path), bytes.NewReader(body))
		if requestErr != nil {
			response.InternalError(c, "Failed to create image generation request")
			return
		}
		upstreamReq.Header.Set("x-goog-api-key", apiKey.Key)
		upstreamReq.Header.Set("Content-Type", "application/json")
		upstreamReq.Header.Set("Accept", "application/json")
		copyImagePlaygroundRequestHeaders(c, upstreamReq)

		upstreamResp, requestErr := h.httpClient.Do(upstreamReq)
		if requestErr != nil {
			response.InternalError(c, "Image gateway request failed")
			return
		}
		upstreamBody, readErr := readImagePlaygroundResponse(upstreamResp.Body, imagePlaygroundMaxResponseBytes)
		_ = upstreamResp.Body.Close()
		if readErr != nil {
			if errors.Is(readErr, errImagePlaygroundResponseTooLarge) {
				response.Error(c, http.StatusBadGateway, "Image gateway response is too large")
			} else {
				response.InternalError(c, "Failed to read image gateway response")
			}
			return
		}
		if upstreamResp.StatusCode >= http.StatusBadRequest {
			message := gatewayErrorMessage(upstreamBody)
			if message == "" {
				message = "Image gateway request failed"
			}
			response.Error(c, upstreamResp.StatusCode, message)
			return
		}
		generated, parseErr := parseImagePlaygroundGeminiResponse(upstreamBody)
		if parseErr != nil {
			response.Error(c, http.StatusBadGateway, parseErr.Error())
			return
		}
		remaining := input.OutputCount - len(images)
		if len(generated) > remaining {
			generated = generated[:remaining]
		}
		images = append(images, generated...)
	}
	response.Success(c, map[string]any{"data": images})
}

func buildImagePlaygroundGatewayBody(input imagePlaygroundGenerationRequest) ([]byte, error) {
	payload := map[string]any{
		"model":  imagePlaygroundOpenAIModel,
		"prompt": strings.TrimSpace(input.Prompt),
		"n":      input.OutputCount,
		"stream": true,
	}
	for field, value := range map[string]string{
		"size":          input.Size,
		"quality":       input.Quality,
		"background":    input.Background,
		"output_format": input.OutputFormat,
	} {
		if value = strings.TrimSpace(value); value != "" {
			payload[field] = value
		}
	}
	return json.Marshal(payload)
}

func buildImagePlaygroundGeminiBody(input imagePlaygroundGenerationRequest) ([]byte, error) {
	imageConfig := map[string]any{"aspectRatio": imagePlaygroundGeminiAspectRatio(input.Size)}
	payload := map[string]any{
		"contents": []map[string]any{{
			"role":  "user",
			"parts": []map[string]any{{"text": strings.TrimSpace(input.Prompt)}},
		}},
		"generationConfig": map[string]any{
			"responseModalities": []string{"TEXT", "IMAGE"},
			"imageConfig":        imageConfig,
		},
	}
	return json.Marshal(payload)
}

type imagePlaygroundImage struct {
	B64JSON       string `json:"b64_json"`
	MIMEType      string `json:"mime_type,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
}

func parseImagePlaygroundGeminiResponse(body []byte) ([]imagePlaygroundImage, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, errors.New("Image gateway returned an invalid response")
	}
	if wrapped, ok := payload["response"].(map[string]any); ok {
		payload = wrapped
	}
	candidates, _ := payload["candidates"].([]any)
	images := make([]imagePlaygroundImage, 0, 1)
	for _, rawCandidate := range candidates {
		candidateImageStart := len(images)
		candidate, _ := rawCandidate.(map[string]any)
		content, _ := candidate["content"].(map[string]any)
		parts, _ := content["parts"].([]any)
		textParts := make([]string, 0, 1)
		for _, rawPart := range parts {
			part, _ := rawPart.(map[string]any)
			if text, _ := part["text"].(string); strings.TrimSpace(text) != "" {
				textParts = append(textParts, strings.TrimSpace(text))
			}
			inline, _ := part["inlineData"].(map[string]any)
			if inline == nil {
				inline, _ = part["inline_data"].(map[string]any)
			}
			data, _ := inline["data"].(string)
			if strings.TrimSpace(data) == "" {
				continue
			}
			mimeType, _ := inline["mimeType"].(string)
			if mimeType == "" {
				mimeType, _ = inline["mime_type"].(string)
			}
			images = append(images, imagePlaygroundImage{B64JSON: data, MIMEType: mimeType})
		}
		if len(textParts) > 0 {
			for i := candidateImageStart; i < len(images); i++ {
				if images[i].RevisedPrompt == "" {
					images[i].RevisedPrompt = strings.Join(textParts, "\n")
				}
			}
		}
	}
	if len(images) == 0 {
		return nil, errors.New("Image gateway returned no usable images")
	}
	return images, nil
}

func imagePlaygroundPlatformSupported(platform string) bool {
	return platform == service.PlatformOpenAI || platform == service.PlatformGemini || platform == service.PlatformAntigravity
}

func imagePlaygroundSizesForPlatform(platform string) []string {
	if platform == service.PlatformOpenAI {
		return imagePlaygroundOpenAISizes
	}
	return imagePlaygroundBaseSizes
}

func imagePlaygroundSizeAllowed(size string, sizes []string) bool {
	size = strings.ToLower(strings.TrimSpace(size))
	if size == "" {
		return true
	}
	for _, candidate := range sizes {
		if size == candidate {
			return true
		}
	}
	return false
}

func isImagePlaygroundGeminiModel(model string) bool {
	model = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(model)), "models/")
	switch model {
	case "gemini-2.0-flash-exp-image-generation",
		"gemini-2.5-flash-image",
		"gemini-3-pro-image",
		"gemini-3-pro-image-preview",
		"gemini-3.1-flash-image",
		"gemini-3.1-flash-image-preview",
		"gemini-3.1-flash-lite-image":
		return true
	default:
		return false
	}
}

func imagePlaygroundGeminiAspectRatio(size string) string {
	switch strings.TrimSpace(size) {
	case "1536x1024":
		return "3:2"
	case "1024x1536":
		return "2:3"
	default:
		return "1:1"
	}
}

func copyImagePlaygroundRequestHeaders(c *gin.Context, req *http.Request) {
	if lang := c.GetHeader("Accept-Language"); lang != "" {
		req.Header.Set("Accept-Language", lang)
	}
	if ua := c.GetHeader("User-Agent"); ua != "" {
		req.Header.Set("User-Agent", ua)
	}
}

func isImagePlaygroundEventStream(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return strings.EqualFold(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]), "text/event-stream")
	}
	return strings.EqualFold(mediaType, "text/event-stream")
}

func relayImagePlaygroundStream(c *gin.Context, body io.Reader, statusCode int, maxBytes int64) error {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return errors.New("streaming is not supported by response writer")
	}

	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(statusCode)

	limited := &io.LimitedReader{R: body, N: maxBytes + 1}
	buffer := make([]byte, 32*1024)
	var written int64
	for {
		n, readErr := limited.Read(buffer)
		if n > 0 {
			remaining := maxBytes - written
			if int64(n) > remaining {
				if remaining > 0 {
					if _, err := c.Writer.Write(buffer[:remaining]); err != nil {
						return err
					}
					flusher.Flush()
				}
				message, _ := json.Marshal(map[string]any{
					"type":  "error",
					"error": map[string]string{"message": "Image gateway response is too large"},
				})
				_, _ = fmt.Fprintf(c.Writer, "\nevent: error\ndata: %s\n\n", message)
				flusher.Flush()
				return errImagePlaygroundResponseTooLarge
			}
			if _, err := c.Writer.Write(buffer[:n]); err != nil {
				return err
			}
			written += int64(n)
			flusher.Flush()
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func validateImagePlaygroundOptions(input imagePlaygroundGenerationRequest) string {
	allowed := func(value string, values ...string) bool {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			return true
		}
		for _, candidate := range values {
			if value == candidate {
				return true
			}
		}
		return false
	}

	if !imagePlaygroundSizeAllowed(input.Size, imagePlaygroundOpenAISizes) {
		return "Unsupported image size"
	}
	if !allowed(input.Quality, "low", "medium", "high") {
		return "Unsupported image quality"
	}
	if !allowed(input.Background, "auto", "opaque") {
		return "Unsupported image background"
	}
	if !allowed(input.OutputFormat, "png", "jpeg", "webp") {
		return "Unsupported image output format"
	}
	return ""
}

func (h *ImagePlaygroundHandler) localGatewayURL(c *gin.Context, path string) string {
	return (&ChatHandler{cfg: h.cfg}).localGatewayURL(c, path)
}

func gatewayErrorMessage(body []byte) string {
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

func readImagePlaygroundResponse(body io.Reader, maxBytes int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, errImagePlaygroundResponseTooLarge
	}
	return data, nil
}
