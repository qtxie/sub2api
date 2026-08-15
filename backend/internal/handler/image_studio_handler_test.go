package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type imageStudioKeyLoaderStub struct {
	key *service.APIKey
	err error
}

func (s imageStudioKeyLoaderStub) GetByID(context.Context, int64) (*service.APIKey, error) {
	return s.key, s.err
}

type imageStudioQuoterStub struct {
	price float64
}

func (s imageStudioQuoterStub) QuoteImageUnitPrice(context.Context, *service.APIKey, int64, string, string) (*service.ImageUnitPriceQuote, error) {
	price := s.price
	return &service.ImageUnitPriceQuote{PricingKind: service.ImagePricingKindFixed, UnitPrice: &price}, nil
}

type imageStudioRoundTripFunc func(*http.Request) (*http.Response, error)

func (f imageStudioRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func imageStudioTestRouter(handler *ImageStudioHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(servermiddleware.ContextKeyUser), servermiddleware.AuthSubject{UserID: 42})
		c.Next()
	})
	router.POST("/pricing", handler.Pricing)
	router.POST("/generations", handler.Generate)
	return router
}

func eligibleImageStudioKey(userID int64) *service.APIKey {
	groupID := int64(8)
	return &service.APIKey{
		ID: 7, UserID: userID, Key: "sk-secret", GroupID: &groupID, Status: service.StatusActive,
		Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, AllowImageGeneration: true},
	}
}

func TestImageStudioGenerateUsesFixedModelAndBase64Response(t *testing.T) {
	var upstreamBody map[string]any
	handler := &ImageStudioHandler{
		apiKeys: imageStudioKeyLoaderStub{key: eligibleImageStudioKey(42)},
		cfg:     &config.Config{Server: config.ServerConfig{Host: "0.0.0.0", Port: 8080}},
	}
	handler.httpClient = &http.Client{Transport: imageStudioRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		require.Equal(t, "http://127.0.0.1:8080/v1/images/generations", req.URL.String())
		require.Equal(t, "Bearer sk-secret", req.Header.Get("Authorization"))
		require.NoError(t, json.NewDecoder(req.Body).Decode(&upstreamBody))
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"created":1,"data":[{"b64_json":"aGVsbG8="}]}`)),
		}, nil
	})}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/generations", bytes.NewBufferString(`{
		"api_key_id":7,"prompt":"draw a lighthouse","size":"3840x2160","quality":"high",
		"background":"opaque","output_format":"webp","n":4
	}`))
	request.Header.Set("Content-Type", "application/json")
	imageStudioTestRouter(handler).ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, imageStudioModel, upstreamBody["model"])
	require.Equal(t, "b64_json", upstreamBody["response_format"])
	require.Equal(t, true, upstreamBody["stream"])
	require.Equal(t, float64(4), upstreamBody["n"])
}

func TestImageStudioValidatesGPTImage2BackgroundAndFormats(t *testing.T) {
	base := imageStudioGenerationRequest{
		APIKeyID: 7, Prompt: "test", Size: "auto", Quality: "auto",
		Background: "auto", OutputCount: 1,
	}
	for _, format := range []string{"png", "jpeg", "webp"} {
		input := base
		input.OutputFormat = format
		require.Empty(t, validateImageStudioInput(input), format)
	}

	base.OutputFormat = "png"
	base.Background = "transparent"
	require.Equal(t, "Unsupported image background", validateImageStudioInput(base))
}

func TestImageStudioValidatesGPTImage2SizesAndQualities(t *testing.T) {
	for _, size := range []string{"auto", "1024x640", "1024x1024", "1536x864", "2048x2048", "3840x2160", "2160x3840"} {
		require.True(t, validGPTImage2Size(size), size)
	}
	for _, size := range []string{"", "1024-1024", "1025x1024", "3856x1024", "3840x1264", "800x800", "3840x2176"} {
		require.False(t, validGPTImage2Size(size), size)
	}

	base := imageStudioGenerationRequest{
		APIKeyID: 7, Prompt: "test", Size: "1536x864", Background: "auto", OutputFormat: "png", OutputCount: 1,
	}
	for _, quality := range []string{"auto", "low", "medium", "high"} {
		input := base
		input.Quality = quality
		require.Empty(t, validateImageStudioInput(input), quality)
	}
	base.Quality = "ultra"
	require.Equal(t, "Unsupported image quality", validateImageStudioInput(base))
}

func TestImageStudioDefaultsToAutomaticSizeAndQuality(t *testing.T) {
	input := imageStudioGenerationRequest{}
	normalizeImageStudioInput(&input)
	require.Equal(t, "auto", input.Size)
	require.Equal(t, "auto", input.Quality)
}

func TestImageStudioRejectsKeyOwnedByAnotherUser(t *testing.T) {
	handler := &ImageStudioHandler{
		apiKeys:    imageStudioKeyLoaderStub{key: eligibleImageStudioKey(99)},
		httpClient: &http.Client{},
		cfg:        &config.Config{Server: config.ServerConfig{Host: "127.0.0.1", Port: 8080}},
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/generations", strings.NewReader(`{
		"api_key_id":7,"prompt":"test","size":"1024x1024","quality":"medium",
		"background":"auto","output_format":"png","n":1
	}`))
	request.Header.Set("Content-Type", "application/json")
	imageStudioTestRouter(handler).ServeHTTP(recorder, request)
	require.Equal(t, http.StatusNotFound, recorder.Code)
}

func TestImageStudioPricingReturnsAllOpenAISizes(t *testing.T) {
	handler := &ImageStudioHandler{
		apiKeys:       imageStudioKeyLoaderStub{key: eligibleImageStudioKey(42)},
		pricingQuoter: imageStudioQuoterStub{price: 0.25},
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/pricing", strings.NewReader(`{"api_key_id":7}`))
	request.Header.Set("Content-Type", "application/json")
	imageStudioTestRouter(handler).ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code)
	var payload struct {
		Data imageStudioPricingResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Len(t, payload.Data.Prices, 8)
	require.Equal(t, "4K", payload.Data.Prices[7].BillingTier)
}

func TestImageStudioResponseBudgetScalesWithCount(t *testing.T) {
	require.Equal(t, int64(50<<20), imageStudioMaxResponseBytes(1))
	require.Greater(t, imageStudioMaxResponseBytes(4), int64(64<<20))
	require.Equal(t, int64(194<<20), imageStudioMaxResponseBytes(4))
	require.Equal(t, imageStudioMaxResponseBytes(4), imageStudioMaxResponseBytes(99))
}

func TestImageStudioLocalGatewayURLNeverUsesRequestHost(t *testing.T) {
	got, err := imageStudioLocalGatewayURL(&config.Config{Server: config.ServerConfig{Host: "::", Port: 9090}}, "/v1/images/generations")
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1:9090/v1/images/generations", got)
}
