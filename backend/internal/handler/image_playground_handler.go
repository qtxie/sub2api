package handler

import (
	"bytes"
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
	imagePlaygroundModel            = "gpt-image-2"
	imagePlaygroundMaxOutputCount   = 4
	imagePlaygroundMaxResponseBytes = 64 << 20
)

var errImagePlaygroundResponseTooLarge = errors.New("image gateway response is too large")

// ImagePlaygroundHandler proxies image-studio requests through the local
// gateway so a browser never receives an API key's secret value.
type ImagePlaygroundHandler struct {
	apiKeyService *service.APIKeyService
	cfg           *config.Config
	httpClient    *http.Client
}

func NewImagePlaygroundHandler(apiKeyService *service.APIKeyService, cfg *config.Config) *ImagePlaygroundHandler {
	return &ImagePlaygroundHandler{
		apiKeyService: apiKeyService,
		cfg:           cfg,
		httpClient:    &http.Client{},
	}
}

type imagePlaygroundGenerationRequest struct {
	APIKeyID     int64  `json:"api_key_id"`
	Prompt       string `json:"prompt"`
	Size         string `json:"size"`
	Quality      string `json:"quality"`
	Background   string `json:"background"`
	OutputFormat string `json:"output_format"`
	OutputCount  int    `json:"n"`
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

	apiKey, err := h.apiKeyService.GetByID(c.Request.Context(), input.APIKeyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if apiKey == nil || apiKey.UserID != subject.UserID {
		response.NotFound(c, "API key not found")
		return
	}
	if !apiKey.IsActive() {
		response.BadRequest(c, "API key is not active")
		return
	}
	if apiKey.Group == nil || apiKey.Group.Platform != service.PlatformOpenAI {
		response.BadRequest(c, "Image Studio requires an OpenAI API key")
		return
	}
	if !service.GroupAllowsImageGeneration(apiKey.Group) {
		response.Forbidden(c, service.ImageGenerationPermissionMessage())
		return
	}

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

func buildImagePlaygroundGatewayBody(input imagePlaygroundGenerationRequest) ([]byte, error) {
	payload := map[string]any{
		"model":  imagePlaygroundModel,
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

	if !allowed(input.Size, "1024x1024", "1536x1024", "1024x1536") {
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
