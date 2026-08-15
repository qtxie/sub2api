package handler

import (
	"bytes"
	"context"
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
	imageStudioModel                = "gpt-image-2"
	imageStudioMaxOutputCount       = 4
	imageStudioPerImageResponseSize = int64(48 << 20)
	imageStudioResponseOverhead     = int64(2 << 20)
	imageStudioHeartbeatInterval    = 15 * time.Second
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

// ImageStudioHandler proxies panel requests through the local OpenAI gateway so
// API key secrets never leave the backend.
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
	APIKeyID     int64  `json:"api_key_id"`
	Prompt       string `json:"prompt"`
	Size         string `json:"size"`
	Quality      string `json:"quality"`
	Background   string `json:"background"`
	OutputFormat string `json:"output_format"`
	OutputCount  int    `json:"n"`
}

type imageStudioPricingRequest struct {
	APIKeyID int64 `json:"api_key_id"`
}

type imageStudioResolutionPrice struct {
	Size         string   `json:"size"`
	BillingTier  string   `json:"billing_tier"`
	PricingKind  string   `json:"pricing_kind"`
	UnitPriceUSD *float64 `json:"unit_price"`
}

type imageStudioPricingResponse struct {
	Currency    string                       `json:"currency"`
	PricingKind string                       `json:"pricing_kind"`
	Prices      []imageStudioResolutionPrice `json:"prices"`
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

	prices := make([]imageStudioResolutionPrice, 0, len(imageStudioPricingSizes))
	pricingKind := service.ImagePricingKindFixed
	for _, size := range imageStudioPricingSizes {
		quote, err := h.pricingQuoter.QuoteImageUnitPrice(c.Request.Context(), apiKey, subject.UserID, imageStudioModel, size)
		if err != nil {
			response.InternalError(c, "Failed to calculate image pricing")
			return
		}
		if quote.PricingKind == service.ImagePricingKindUsageBased {
			pricingKind = service.ImagePricingKindUsageBased
		}
		prices = append(prices, imageStudioResolutionPrice{
			Size:         size,
			BillingTier:  service.NormalizeImageBillingTierOrDefault(size),
			PricingKind:  quote.PricingKind,
			UnitPriceUSD: quote.UnitPrice,
		})
	}

	response.Success(c, imageStudioPricingResponse{Currency: "USD", PricingKind: pricingKind, Prices: prices})
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

	var input imageStudioGenerationRequest
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "Invalid image generation request")
		return
	}
	normalizeImageStudioInput(&input)
	if message := validateImageStudioInput(input); message != "" {
		response.BadRequest(c, message)
		return
	}
	apiKey, ok := h.loadEligibleAPIKey(c, input.APIKeyID, subject.UserID)
	if !ok {
		return
	}

	body, err := json.Marshal(map[string]any{
		"model":           imageStudioModel,
		"prompt":          input.Prompt,
		"n":               input.OutputCount,
		"stream":          true,
		"response_format": "b64_json",
		"size":            input.Size,
		"quality":         input.Quality,
		"background":      input.Background,
		"output_format":   input.OutputFormat,
	})
	if err != nil {
		response.InternalError(c, "Failed to build image generation request")
		return
	}
	gatewayURL, err := imageStudioLocalGatewayURL(h.cfg, "/v1/images/generations")
	if err != nil {
		response.InternalError(c, "Image gateway is not configured")
		return
	}
	upstreamReq, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, gatewayURL, bytes.NewReader(body))
	if err != nil {
		response.InternalError(c, "Failed to create image generation request")
		return
	}
	upstreamReq.Header.Set("Authorization", "Bearer "+apiKey.Key)
	upstreamReq.Header.Set("Content-Type", "application/json")
	upstreamReq.Header.Set("Accept", "text/event-stream, application/json")
	copyImageStudioRequestHeader(c, upstreamReq, "Accept-Language")
	copyImageStudioRequestHeader(c, upstreamReq, "User-Agent")

	upstreamResp, err := h.httpClient.Do(upstreamReq)
	if err != nil {
		response.InternalError(c, "Image gateway request failed")
		return
	}
	defer upstreamResp.Body.Close()
	maxBytes := imageStudioMaxResponseBytes(input.OutputCount)
	if upstreamResp.StatusCode < http.StatusBadRequest && isImageStudioEventStream(upstreamResp.Header.Get("Content-Type")) {
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
	if err := json.Unmarshal(upstreamBody, &output); err != nil {
		response.Error(c, http.StatusBadGateway, "Image gateway returned an invalid response")
		return
	}
	response.Success(c, output)
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
	if apiKey.Group == nil || apiKey.Group.Platform != service.PlatformOpenAI {
		response.BadRequest(c, "Image Studio requires an OpenAI API key")
		return nil, false
	}
	if !service.GroupAllowsImageGeneration(apiKey.Group) {
		response.Forbidden(c, service.ImageGenerationPermissionMessage())
		return nil, false
	}
	return apiKey, true
}

func normalizeImageStudioInput(input *imageStudioGenerationRequest) {
	input.Prompt = strings.TrimSpace(input.Prompt)
	input.Size = strings.ToLower(strings.TrimSpace(input.Size))
	input.Quality = strings.ToLower(strings.TrimSpace(input.Quality))
	input.Background = strings.ToLower(strings.TrimSpace(input.Background))
	input.OutputFormat = strings.ToLower(strings.TrimSpace(input.OutputFormat))
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
}

func validateImageStudioInput(input imageStudioGenerationRequest) string {
	if input.APIKeyID <= 0 {
		return "API key is required"
	}
	if input.Prompt == "" {
		return "Prompt is required"
	}
	if input.OutputCount < 1 || input.OutputCount > imageStudioMaxOutputCount {
		return "Image count must be between 1 and 4"
	}
	if !validGPTImage2Size(input.Size) {
		return "Unsupported image size"
	}
	if !imageStudioOptionAllowed(input.Quality, "auto", "low", "medium", "high") {
		return "Unsupported image quality"
	}
	if !imageStudioOptionAllowed(input.Background, "auto", "opaque") {
		return "Unsupported image background"
	}
	if !imageStudioOptionAllowed(input.OutputFormat, "png", "jpeg", "webp") {
		return "Unsupported image output format"
	}
	return ""
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
