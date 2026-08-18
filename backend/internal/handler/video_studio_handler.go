package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const (
	videoStudioModel             = "grok-imagine-video-1.5"
	videoStudioProvider          = service.PlatformGrok
	videoStudioJSONResponseLimit = int64(4 << 20)
)

var (
	videoStudioAspectRatios = []string{"1:1", "16:9", "9:16", "4:3", "3:4", "3:2", "2:3"}
	videoStudioResolutions  = []string{"480p", "720p", "1080p"}
)

type videoStudioAPIKeyLoader interface {
	GetByID(context.Context, int64) (*service.APIKey, error)
}

type videoStudioPricingQuoter interface {
	QuoteVideoPrice(context.Context, *service.APIKey, int64, string, string, int) (*service.VideoPriceQuote, error)
}

type videoStudioTaskTracker interface {
	Register(context.Context, service.VideoStudioTask) error
}

// VideoStudioHandler is the JWT-authenticated panel adapter for Grok video
// generation. It sends the selected API key only to the local gateway.
type VideoStudioHandler struct {
	apiKeys       videoStudioAPIKeyLoader
	pricingQuoter videoStudioPricingQuoter
	tracker       videoStudioTaskTracker
	cfg           *config.Config
	httpClient    *http.Client
}

func NewVideoStudioHandler(
	apiKeys *service.APIKeyService,
	openAI *service.OpenAIGatewayService,
	tracker *service.VideoStudioTracker,
	cfg *config.Config,
) *VideoStudioHandler {
	return &VideoStudioHandler{
		apiKeys:       apiKeys,
		pricingQuoter: openAI,
		tracker:       tracker,
		cfg:           cfg,
		httpClient:    &http.Client{},
	}
}

type videoStudioCapabilitiesRequest struct {
	APIKeyID int64 `json:"api_key_id"`
}

type videoStudioPricingRequest struct {
	APIKeyID int64 `json:"api_key_id"`
	Duration int   `json:"duration"`
}

type videoStudioGenerationRequest struct {
	APIKeyID    int64  `json:"api_key_id"`
	Prompt      string `json:"prompt"`
	Duration    int    `json:"duration"`
	AspectRatio string `json:"aspect_ratio"`
	Resolution  string `json:"resolution"`
}

type videoStudioCapabilitiesResponse struct {
	Provider        string   `json:"provider"`
	Model           string   `json:"model"`
	Label           string   `json:"label"`
	MinDuration     int      `json:"min_duration"`
	MaxDuration     int      `json:"max_duration"`
	DefaultDuration int      `json:"default_duration"`
	AspectRatios    []string `json:"aspect_ratios"`
	Resolutions     []string `json:"resolutions"`
}

type videoStudioResolutionPrice struct {
	Resolution string   `json:"resolution"`
	UnitPrice  *float64 `json:"unit_price"`
	TotalPrice *float64 `json:"total_price"`
}

type videoStudioPricingResponse struct {
	Currency    string                       `json:"currency"`
	PricingKind string                       `json:"pricing_kind"`
	Provider    string                       `json:"provider"`
	Model       string                       `json:"model"`
	Duration    int                          `json:"duration"`
	Prices      []videoStudioResolutionPrice `json:"prices"`
}

type videoStudioGenerationResponse struct {
	RequestID string `json:"request_id"`
	Status    string `json:"status"`
}

type videoStudioStatusVideo struct {
	Duration          int    `json:"duration,omitempty"`
	RespectModeration *bool  `json:"respect_moderation,omitempty"`
	ContentURL        string `json:"content_url,omitempty"`
}

type videoStudioStatusResponse struct {
	RequestID string                  `json:"request_id"`
	Status    string                  `json:"status"`
	Progress  *int                    `json:"progress,omitempty"`
	Model     string                  `json:"model"`
	Video     *videoStudioStatusVideo `json:"video,omitempty"`
	Error     string                  `json:"error,omitempty"`
}

func (h *VideoStudioHandler) Capabilities(c *gin.Context) {
	subject, ok := videoStudioAuthSubject(c)
	if !ok {
		return
	}
	if h == nil || h.apiKeys == nil {
		response.InternalError(c, "Video Studio is not configured")
		return
	}

	var input videoStudioCapabilitiesRequest
	if err := decodeVideoStudioJSON(c, &input); err != nil || input.APIKeyID <= 0 {
		response.BadRequest(c, "Invalid video capabilities request")
		return
	}
	if _, ok := h.loadEligibleAPIKey(c, input.APIKeyID, subject.UserID); !ok {
		return
	}

	response.Success(c, videoStudioCapabilitiesResponse{
		Provider:        videoStudioProvider,
		Model:           videoStudioModel,
		Label:           "Grok Imagine Video 1.5",
		MinDuration:     service.VideoBillingMinDurationSeconds,
		MaxDuration:     service.VideoBillingMaxDurationSeconds,
		DefaultDuration: service.VideoBillingDefaultDurationSeconds,
		AspectRatios:    append([]string(nil), videoStudioAspectRatios...),
		Resolutions:     append([]string(nil), videoStudioResolutions...),
	})
}

func (h *VideoStudioHandler) Pricing(c *gin.Context) {
	subject, ok := videoStudioAuthSubject(c)
	if !ok {
		return
	}
	if h == nil || h.apiKeys == nil || h.pricingQuoter == nil {
		response.InternalError(c, "Video pricing service is not configured")
		return
	}

	var input videoStudioPricingRequest
	if err := decodeVideoStudioJSON(c, &input); err != nil {
		response.BadRequest(c, "Invalid video pricing request")
		return
	}
	if input.APIKeyID <= 0 {
		response.BadRequest(c, "API key is required")
		return
	}
	if !videoStudioDurationAllowed(input.Duration) {
		response.BadRequest(c, "Duration must be between 1 and 15 seconds")
		return
	}
	apiKey, ok := h.loadEligibleAPIKey(c, input.APIKeyID, subject.UserID)
	if !ok {
		return
	}

	prices := make([]videoStudioResolutionPrice, 0, len(videoStudioResolutions))
	pricingKind := service.VideoPricingKindFixed
	for _, resolution := range videoStudioResolutions {
		quote, err := h.pricingQuoter.QuoteVideoPrice(
			c.Request.Context(), apiKey, subject.UserID, videoStudioModel, resolution, input.Duration,
		)
		if err != nil {
			response.InternalError(c, "Failed to calculate video pricing")
			return
		}
		if quote.PricingKind == service.VideoPricingKindUsageBased {
			pricingKind = service.VideoPricingKindUsageBased
		}
		prices = append(prices, videoStudioResolutionPrice{
			Resolution: resolution,
			UnitPrice:  quote.UnitPrice,
			TotalPrice: quote.TotalPrice,
		})
	}

	response.Success(c, videoStudioPricingResponse{
		Currency:    "USD",
		PricingKind: pricingKind,
		Provider:    videoStudioProvider,
		Model:       videoStudioModel,
		Duration:    input.Duration,
		Prices:      prices,
	})
}

func (h *VideoStudioHandler) Generate(c *gin.Context) {
	subject, ok := videoStudioAuthSubject(c)
	if !ok {
		return
	}
	if h == nil || h.apiKeys == nil || h.httpClient == nil {
		response.InternalError(c, "Video Studio is not configured")
		return
	}

	var input videoStudioGenerationRequest
	if err := decodeVideoStudioJSON(c, &input); err != nil {
		response.BadRequest(c, "Invalid video generation request")
		return
	}
	input.Prompt = strings.TrimSpace(input.Prompt)
	input.AspectRatio = strings.TrimSpace(input.AspectRatio)
	input.Resolution = strings.ToLower(strings.TrimSpace(input.Resolution))
	if message := validateVideoStudioGeneration(input); message != "" {
		response.BadRequest(c, message)
		return
	}
	apiKey, ok := h.loadEligibleAPIKey(c, input.APIKeyID, subject.UserID)
	if !ok {
		return
	}

	body, err := json.Marshal(map[string]any{
		"model":        videoStudioModel,
		"prompt":       input.Prompt,
		"duration":     input.Duration,
		"aspect_ratio": input.AspectRatio,
		"resolution":   input.Resolution,
	})
	if err != nil {
		response.InternalError(c, "Failed to build video generation request")
		return
	}
	request, err := h.newGatewayRequest(c, http.MethodPost, "/v1/videos/generations", body)
	if err != nil {
		response.InternalError(c, "Video gateway is not configured")
		return
	}
	request.Header.Set("Authorization", "Bearer "+apiKey.Key)
	request.Header.Set("Accept", "application/json")

	upstreamBody, ok := h.forwardGatewayJSON(c, request)
	if !ok {
		return
	}
	payload, err := decodeVideoStudioJSONObject(upstreamBody)
	if err != nil {
		response.Error(c, http.StatusBadGateway, "Video gateway returned an invalid response")
		return
	}
	requestID := videoStudioRequestID(payload)
	if !service.IsValidVideoStudioRequestID(requestID) {
		response.Error(c, http.StatusBadGateway, "Video gateway returned no request ID")
		return
	}
	if h.tracker != nil {
		if err := registerVideoStudioTask(c.Request.Context(), h.tracker, service.VideoStudioTask{
			RequestID:   requestID,
			UserID:      subject.UserID,
			APIKeyID:    apiKey.ID,
			Model:       videoStudioModel,
			Duration:    input.Duration,
			AspectRatio: input.AspectRatio,
			Resolution:  input.Resolution,
		}); err != nil {
			logger.L().Warn("video_studio.task_registration_failed",
				zap.String("request_id", requestID),
				zap.Int64("user_id", subject.UserID),
				zap.Int64("api_key_id", apiKey.ID),
				zap.Error(err),
			)
		}
	}
	response.Success(c, videoStudioGenerationResponse{
		RequestID: requestID,
		Status:    service.VideoStudioTaskStatusPending,
	})
}

func (h *VideoStudioHandler) Status(c *gin.Context) {
	subject, ok := videoStudioAuthSubject(c)
	if !ok {
		return
	}
	if h == nil || h.apiKeys == nil || h.httpClient == nil {
		response.InternalError(c, "Video Studio is not configured")
		return
	}
	requestID := strings.TrimSpace(c.Param("request_id"))
	if !service.IsValidVideoStudioRequestID(requestID) {
		response.BadRequest(c, "Video request ID is required")
		return
	}
	apiKeyID, ok := videoStudioAPIKeyIDFromQuery(c)
	if !ok {
		return
	}
	apiKey, ok := h.loadEligibleAPIKey(c, apiKeyID, subject.UserID)
	if !ok {
		return
	}

	request, err := h.newGatewayTaskRequest(c, requestID, false)
	if err != nil {
		response.InternalError(c, "Video gateway is not configured")
		return
	}
	request.Header.Set("Authorization", "Bearer "+apiKey.Key)
	request.Header.Set("Accept", "application/json")
	upstreamBody, ok := h.forwardGatewayJSON(c, request)
	if !ok {
		return
	}

	payload, err := decodeVideoStudioJSONObject(upstreamBody)
	if err != nil {
		response.Error(c, http.StatusBadGateway, "Video gateway returned an invalid response")
		return
	}
	status, ok := parseVideoStudioStatusValue(videoStudioString(payload, "status"))
	if !ok {
		response.Error(c, http.StatusBadGateway, "Video gateway returned an unsupported status")
		return
	}
	if status == service.VideoStudioTaskStatusDone {
		video, _ := payload["video"].(map[string]any)
		rawURL, _ := video["url"].(string)
		if strings.TrimSpace(rawURL) == "" {
			response.Error(c, http.StatusBadGateway, "Video gateway reported completion without content")
			return
		}
	}
	contentURL := strings.TrimRight(c.Request.URL.EscapedPath(), "/") + "/content?api_key_id=" + strconv.FormatInt(apiKeyID, 10)
	response.Success(c, normalizeVideoStudioStatus(payload, requestID, contentURL, status))
}

func registerVideoStudioTask(ctx context.Context, tracker videoStudioTaskTracker, task service.VideoStudioTask) error {
	if tracker == nil {
		return nil
	}
	retryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 750*time.Millisecond)
	defer cancel()
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			delay := time.Duration(attempt*attempt) * 25 * time.Millisecond
			timer := time.NewTimer(delay)
			select {
			case <-retryCtx.Done():
				timer.Stop()
				return err
			case <-timer.C:
			}
		}
		if err = tracker.Register(retryCtx, task); err == nil {
			return nil
		}
	}
	return err
}

func (h *VideoStudioHandler) Content(c *gin.Context) {
	subject, ok := videoStudioAuthSubject(c)
	if !ok {
		return
	}
	if h == nil || h.apiKeys == nil || h.httpClient == nil {
		response.InternalError(c, "Video Studio is not configured")
		return
	}
	requestID := strings.TrimSpace(c.Param("request_id"))
	if !service.IsValidVideoStudioRequestID(requestID) {
		response.BadRequest(c, "Video request ID is required")
		return
	}
	apiKeyID, ok := videoStudioAPIKeyIDFromQuery(c)
	if !ok {
		return
	}
	apiKey, ok := h.loadEligibleAPIKey(c, apiKeyID, subject.UserID)
	if !ok {
		return
	}

	request, err := h.newGatewayTaskRequest(c, requestID, true)
	if err != nil {
		response.InternalError(c, "Video gateway is not configured")
		return
	}
	request.Header.Set("Authorization", "Bearer "+apiKey.Key)
	request.Header.Set("Accept", "video/*, application/octet-stream")
	for _, header := range []string{"Range", "If-Range", "If-None-Match", "If-Modified-Since"} {
		copyVideoStudioRequestHeader(c, request, header)
	}

	upstreamResp, err := h.httpClient.Do(request)
	if err != nil {
		response.Error(c, http.StatusBadGateway, "Video gateway request failed")
		return
	}
	defer upstreamResp.Body.Close()
	copyVideoStudioContentHeaders(c.Writer.Header(), upstreamResp.Header)
	c.Status(upstreamResp.StatusCode)
	_, _ = io.Copy(c.Writer, upstreamResp.Body)
}

func videoStudioAuthSubject(c *gin.Context) (servermiddleware.AuthSubject, bool) {
	subject, ok := servermiddleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return servermiddleware.AuthSubject{}, false
	}
	return subject, true
}

func (h *VideoStudioHandler) loadEligibleAPIKey(c *gin.Context, apiKeyID, userID int64) (*service.APIKey, bool) {
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
	if apiKey.Group == nil || apiKey.Group.Platform != service.PlatformGrok {
		response.BadRequest(c, "Video Studio requires a Grok API key")
		return nil, false
	}
	if !service.GroupAllowsImageGeneration(apiKey.Group) {
		response.Forbidden(c, service.ImageGenerationPermissionMessage())
		return nil, false
	}
	return apiKey, true
}

func decodeVideoStudioJSON(c *gin.Context, output any) error {
	if c == nil || c.Request == nil || c.Request.Body == nil {
		return errors.New("request body is required")
	}
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request must contain one JSON object")
		}
		return err
	}
	return nil
}

func validateVideoStudioGeneration(input videoStudioGenerationRequest) string {
	if input.APIKeyID <= 0 {
		return "API key is required"
	}
	if input.Prompt == "" {
		return "Prompt is required"
	}
	if !videoStudioDurationAllowed(input.Duration) {
		return "Duration must be between 1 and 15 seconds"
	}
	if !videoStudioOptionAllowed(input.AspectRatio, videoStudioAspectRatios) {
		return "Unsupported video aspect ratio"
	}
	if !videoStudioOptionAllowed(input.Resolution, videoStudioResolutions) {
		return "Unsupported video resolution"
	}
	return ""
}

func videoStudioDurationAllowed(duration int) bool {
	return duration >= service.VideoBillingMinDurationSeconds && duration <= service.VideoBillingMaxDurationSeconds
}

func videoStudioOptionAllowed(value string, allowed []string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func videoStudioAPIKeyIDFromQuery(c *gin.Context) (int64, bool) {
	query := c.Request.URL.Query()
	values, exists := query["api_key_id"]
	if !exists || len(values) != 1 {
		response.BadRequest(c, "API key is required")
		return 0, false
	}
	for key := range query {
		if key != "api_key_id" && key != "timezone" {
			response.BadRequest(c, "Invalid video request query")
			return 0, false
		}
	}
	apiKeyID, err := strconv.ParseInt(strings.TrimSpace(values[0]), 10, 64)
	if err != nil || apiKeyID <= 0 {
		response.BadRequest(c, "API key is required")
		return 0, false
	}
	return apiKeyID, true
}

func (h *VideoStudioHandler) newGatewayRequest(c *gin.Context, method, path string, body []byte) (*http.Request, error) {
	gatewayURL, err := imageStudioLocalGatewayURL(h.cfg, path)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(c.Request.Context(), method, gatewayURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if len(body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	for _, header := range []string{"Accept-Language", "User-Agent", "X-Request-ID"} {
		copyVideoStudioRequestHeader(c, request, header)
	}
	return request, nil
}

func (h *VideoStudioHandler) newGatewayTaskRequest(c *gin.Context, requestID string, content bool) (*http.Request, error) {
	baseURL, err := imageStudioLocalGatewayURL(h.cfg, "/v1/videos")
	if err != nil {
		return nil, err
	}
	gatewayURL := baseURL + "/" + url.PathEscape(requestID)
	if content {
		gatewayURL += "/content"
	}
	request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, gatewayURL, nil)
	if err != nil {
		return nil, err
	}
	for _, header := range []string{"Accept-Language", "User-Agent", "X-Request-ID"} {
		copyVideoStudioRequestHeader(c, request, header)
	}
	return request, nil
}

func (h *VideoStudioHandler) forwardGatewayJSON(c *gin.Context, request *http.Request) ([]byte, bool) {
	upstreamResp, err := h.httpClient.Do(request)
	if err != nil {
		response.Error(c, http.StatusBadGateway, "Video gateway request failed")
		return nil, false
	}
	defer upstreamResp.Body.Close()
	body, err := readVideoStudioJSONResponse(upstreamResp.Body)
	if err != nil {
		response.Error(c, http.StatusBadGateway, "Video gateway returned an invalid response")
		return nil, false
	}
	if upstreamResp.StatusCode >= http.StatusBadRequest {
		message := imageStudioGatewayErrorMessage(body)
		if message == "" {
			message = "Video gateway request failed"
		}
		response.Error(c, upstreamResp.StatusCode, message)
		return nil, false
	}
	return body, true
}

func readVideoStudioJSONResponse(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, videoStudioJSONResponseLimit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > videoStudioJSONResponseLimit {
		return nil, errors.New("video gateway response is too large")
	}
	return data, nil
}

func decodeVideoStudioJSONObject(body []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil || payload == nil {
		if err == nil {
			err = errors.New("response is not a JSON object")
		}
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("response contains multiple JSON values")
		}
		return nil, err
	}
	return payload, nil
}

func videoStudioRequestID(payload map[string]any) string {
	paths := [][]string{
		{"request_id"}, {"id"}, {"data", "request_id"}, {"data", "id"},
		{"video", "request_id"}, {"video", "id"}, {"task_id"}, {"data", "task_id"}, {"video", "task_id"},
	}
	for _, path := range paths {
		value := any(payload)
		for _, part := range path {
			object, ok := value.(map[string]any)
			if !ok {
				value = nil
				break
			}
			value = object[part]
		}
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

func videoStudioString(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return strings.TrimSpace(value)
}

func normalizeVideoStudioStatus(payload map[string]any, requestID, contentURL, status string) videoStudioStatusResponse {
	result := videoStudioStatusResponse{
		RequestID: requestID,
		Status:    status,
		Progress:  videoStudioProgress(payload["progress"]),
		Model:     videoStudioModel,
		Error:     sanitizeVideoStudioStatusError(payload["error"]),
	}
	video, _ := payload["video"].(map[string]any)
	if video == nil {
		return result
	}
	output := &videoStudioStatusVideo{
		Duration:          videoStudioPositiveInt(video["duration"]),
		RespectModeration: videoStudioBool(video["respect_moderation"]),
	}
	if rawURL, ok := video["url"].(string); ok && strings.TrimSpace(rawURL) != "" {
		output.ContentURL = contentURL
	}
	if output.Duration > 0 || output.RespectModeration != nil || output.ContentURL != "" {
		result.Video = output
	}
	return result
}

func parseVideoStudioStatusValue(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case service.VideoStudioTaskStatusPending:
		return service.VideoStudioTaskStatusPending, true
	case service.VideoStudioTaskStatusDone:
		return service.VideoStudioTaskStatusDone, true
	case service.VideoStudioTaskStatusFailed:
		return service.VideoStudioTaskStatusFailed, true
	case service.VideoStudioTaskStatusExpired:
		return service.VideoStudioTaskStatusExpired, true
	default:
		return "", false
	}
}

func videoStudioProgress(value any) *int {
	progress := videoStudioInt(value)
	if progress == nil {
		return nil
	}
	if *progress < 0 {
		*progress = 0
	}
	if *progress > 100 {
		*progress = 100
	}
	return progress
}

func videoStudioPositiveInt(value any) int {
	parsed := videoStudioInt(value)
	if parsed == nil || *parsed <= 0 {
		return 0
	}
	return *parsed
}

func videoStudioInt(value any) *int {
	var parsed int64
	switch typed := value.(type) {
	case json.Number:
		value, err := strconv.ParseFloat(typed.String(), 64)
		if err != nil {
			return nil
		}
		parsed = int64(value)
	case float64:
		parsed = int64(typed)
	case int:
		parsed = int64(typed)
	default:
		return nil
	}
	result := int(parsed)
	return &result
}

func videoStudioBool(value any) *bool {
	parsed, ok := value.(bool)
	if !ok {
		return nil
	}
	return &parsed
}

func sanitizeVideoStudioStatusError(value any) string {
	message := ""
	switch typed := value.(type) {
	case string:
		message = typed
	case map[string]any:
		message, _ = typed["message"].(string)
	}
	message = strings.ToValidUTF8(strings.TrimSpace(message), "")
	message = strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' {
			return -1
		}
		return r
	}, message)
	if strings.Contains(message, "://") || strings.Contains(message, "/v1/videos/") {
		return "Video generation failed"
	}
	if runes := []rune(message); len(runes) > 512 {
		message = string(runes[:512])
	}
	return message
}

func copyVideoStudioRequestHeader(c *gin.Context, request *http.Request, name string) {
	if value := strings.TrimSpace(c.GetHeader(name)); value != "" {
		request.Header.Set(name, value)
	}
}

func copyVideoStudioContentHeaders(destination, source http.Header) {
	for _, name := range []string{
		"Accept-Ranges", "Cache-Control", "Content-Disposition", "Content-Encoding",
		"Content-Length", "Content-Range", "Content-Type", "ETag", "Last-Modified",
	} {
		for _, value := range source.Values(name) {
			destination.Add(name, value)
		}
	}
	destination.Set("X-Content-Type-Options", "nosniff")
}
