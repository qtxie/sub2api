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
	router.POST("/capabilities", handler.Capabilities)
	router.POST("/generations", handler.Generate)
	return router
}

func eligibleImageStudioKey(userID int64) *service.APIKey {
	return eligibleImageStudioKeyForPlatform(userID, service.PlatformOpenAI)
}

func eligibleImageStudioKeyForPlatform(userID int64, platform string) *service.APIKey {
	groupID := int64(8)
	return &service.APIKey{
		ID: 7, UserID: userID, Key: "sk-secret", GroupID: &groupID, Status: service.StatusActive,
		Group: &service.Group{ID: groupID, Platform: platform, AllowImageGeneration: true},
	}
}

func imageStudioTestModelCapability(t *testing.T, provider, model string) imageStudioModelCapability {
	t.Helper()
	capabilities, ok := imageStudioCapabilitiesForProvider(provider)
	require.True(t, ok)
	capability, ok := imageStudioCapabilityForModel(capabilities, model)
	require.True(t, ok)
	return capability
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
	capability := imageStudioTestModelCapability(t, service.PlatformOpenAI, imageStudioModel)
	base := imageStudioGenerationRequest{
		APIKeyID: 7, Prompt: "test", Size: "auto", Quality: "auto",
		Background: "auto", OutputCount: 1,
	}
	for _, format := range []string{"png", "jpeg", "webp"} {
		input := base
		input.OutputFormat = format
		require.Empty(t, validateImageStudioInput(input, service.PlatformOpenAI, capability), format)
	}

	base.OutputFormat = "png"
	base.Background = "transparent"
	require.Equal(t, "Unsupported image background", validateImageStudioInput(base, service.PlatformOpenAI, capability))
}

func TestImageStudioValidatesGPTImage2SizesAndQualities(t *testing.T) {
	capability := imageStudioTestModelCapability(t, service.PlatformOpenAI, imageStudioModel)
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
		require.Empty(t, validateImageStudioInput(input, service.PlatformOpenAI, capability), quality)
	}
	base.Quality = "ultra"
	require.Equal(t, "Unsupported image quality", validateImageStudioInput(base, service.PlatformOpenAI, capability))
}

func TestImageStudioDefaultsToAutomaticSizeAndQuality(t *testing.T) {
	input := imageStudioGenerationRequest{}
	normalizeImageStudioInput(&input, service.PlatformOpenAI, imageStudioTestModelCapability(t, service.PlatformOpenAI, imageStudioModel))
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
	require.Equal(t, int64(482<<20), imageStudioMaxResponseBytes(10))
	require.Equal(t, imageStudioMaxResponseBytes(10), imageStudioMaxResponseBytes(99))
}

func TestImageStudioLocalGatewayURLNeverUsesRequestHost(t *testing.T) {
	got, err := imageStudioLocalGatewayURL(&config.Config{Server: config.ServerConfig{Host: "::", Port: 9090}}, "/v1/images/generations")
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1:9090/v1/images/generations", got)
}

func TestImageStudioCapabilitiesAreProviderSpecific(t *testing.T) {
	tests := []struct {
		provider     string
		defaultModel string
		assert       func(*testing.T, imageStudioCapabilitiesResponse)
	}{
		{
			provider: service.PlatformGemini, defaultModel: imageStudioDefaultGeminiModel,
			assert: func(t *testing.T, capabilities imageStudioCapabilitiesResponse) {
				require.Len(t, capabilities.Models, 4)
				for _, model := range capabilities.Models {
					require.Empty(t, model.Qualities)
					require.Empty(t, model.Backgrounds)
					require.Empty(t, model.OutputFormats)
					require.Equal(t, 1, model.MaxImages)
					require.NotContains(t, model.ID, "preview")
				}
				legacy, ok := imageStudioCapabilityForModel(capabilities, "gemini-2.5-flash-image")
				require.True(t, ok)
				require.Empty(t, legacy.ImageSizes)
			},
		},
		{
			provider: service.PlatformGrok, defaultModel: imageStudioDefaultGrokModel,
			assert: func(t *testing.T, capabilities imageStudioCapabilitiesResponse) {
				require.Len(t, capabilities.Models, 1)
				model := capabilities.Models[0]
				require.Equal(t, []string{"1k", "2k"}, model.Resolutions)
				require.Equal(t, []string{"medium", "low"}, model.Qualities)
				require.Empty(t, model.Backgrounds)
				require.Empty(t, model.OutputFormats)
				require.Equal(t, 10, model.MaxImages)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.provider, func(t *testing.T) {
			handler := &ImageStudioHandler{apiKeys: imageStudioKeyLoaderStub{key: eligibleImageStudioKeyForPlatform(42, test.provider)}}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/capabilities", strings.NewReader(`{"api_key_id":7}`))
			request.Header.Set("Content-Type", "application/json")
			imageStudioTestRouter(handler).ServeHTTP(recorder, request)
			require.Equal(t, http.StatusOK, recorder.Code)
			var payload struct {
				Data imageStudioCapabilitiesResponse `json:"data"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
			require.Equal(t, test.provider, payload.Data.Provider)
			require.Equal(t, test.defaultModel, payload.Data.DefaultModel)
			test.assert(t, payload.Data)
		})
	}
}

func TestImageStudioGenerateGeminiUsesInteractionsAndNormalizesImage(t *testing.T) {
	var upstreamBody map[string]any
	handler := &ImageStudioHandler{
		apiKeys: imageStudioKeyLoaderStub{key: eligibleImageStudioKeyForPlatform(42, service.PlatformGemini)},
		cfg:     &config.Config{Server: config.ServerConfig{Host: "0.0.0.0", Port: 8080}},
	}
	handler.httpClient = &http.Client{Transport: imageStudioRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		require.Equal(t, "http://127.0.0.1:8080/v1beta/interactions", req.URL.String())
		require.Equal(t, "sk-secret", req.Header.Get("x-goog-api-key"))
		require.Empty(t, req.Header.Get("Authorization"))
		require.NoError(t, json.NewDecoder(req.Body).Decode(&upstreamBody))
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
				"created_at":123,"steps":[{"type":"model_output","content":[
					{"type":"image","data":"aGVsbG8=","mime_type":"image/jpeg"}
				]}]
			}`)),
		}, nil
	})}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/generations", strings.NewReader(`{
		"api_key_id":7,"model":"gemini-3-pro-image","prompt":"draw a lighthouse",
		"aspect_ratio":"16:9","image_size":"2K"
	}`))
	request.Header.Set("Content-Type", "application/json")
	imageStudioTestRouter(handler).ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, "gemini-3-pro-image", upstreamBody["model"])
	require.Equal(t, "draw a lighthouse", upstreamBody["input"])
	require.Equal(t, false, upstreamBody["store"])
	require.NotContains(t, upstreamBody, "n")
	require.NotContains(t, upstreamBody, "quality")
	format := upstreamBody["response_format"].(map[string]any)
	require.Equal(t, "image", format["type"])
	require.Equal(t, "16:9", format["aspect_ratio"])
	require.Equal(t, "2K", format["image_size"])

	var payload struct {
		Data struct {
			Data []imageStudioImage `json:"data"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Len(t, payload.Data.Data, 1)
	require.Equal(t, "aGVsbG8=", payload.Data.Data[0].B64JSON)
	require.Equal(t, "image/jpeg", payload.Data.Data[0].MIMEType)
}

func TestNormalizeImageStudioGeminiInteractionResponseIgnoresNonModelOutputSteps(t *testing.T) {
	result, err := normalizeImageStudioGeminiInteractionResponse([]byte(`{
		"steps":[
			{"type":"thought","content":[{"type":"image","data":"dGhvdWdodA==","mime_type":"image/png"}]},
			{"type":"model_output","content":[{"type":"image","data":"ZmluYWw=","mime_type":"image/png"}]}
		]
	}`))
	require.NoError(t, err)

	payload, ok := result.(map[string]any)
	require.True(t, ok)
	images, ok := payload["data"].([]imageStudioImage)
	require.True(t, ok)
	require.Len(t, images, 1)
	require.Equal(t, "ZmluYWw=", images[0].B64JSON)
}

func TestImageStudioGenerateGrokUsesOnlyDocumentedOptions(t *testing.T) {
	var upstreamBody map[string]any
	handler := &ImageStudioHandler{
		apiKeys: imageStudioKeyLoaderStub{key: eligibleImageStudioKeyForPlatform(42, service.PlatformGrok)},
		cfg:     &config.Config{Server: config.ServerConfig{Host: "127.0.0.1", Port: 8080}},
	}
	handler.httpClient = &http.Client{Transport: imageStudioRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		require.Equal(t, "http://127.0.0.1:8080/v1/images/generations", req.URL.String())
		require.Equal(t, "Bearer sk-secret", req.Header.Get("Authorization"))
		require.NoError(t, json.NewDecoder(req.Body).Decode(&upstreamBody))
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"data":[{"b64_json":"aGVsbG8=","mime_type":"image/jpeg"}]}`)),
		}, nil
	})}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/generations", strings.NewReader(`{
		"api_key_id":7,"model":"grok-imagine-image-2.0","prompt":"draw a lighthouse",
		"aspect_ratio":"20:9","resolution":"2k","quality":"low","n":10
	}`))
	request.Header.Set("Content-Type", "application/json")
	imageStudioTestRouter(handler).ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, imageStudioDefaultGrokModel, upstreamBody["model"])
	require.Equal(t, "20:9", upstreamBody["aspect_ratio"])
	require.Equal(t, "2k", upstreamBody["resolution"])
	require.Equal(t, "low", upstreamBody["quality"])
	require.Equal(t, float64(10), upstreamBody["n"])
	require.Equal(t, "b64_json", upstreamBody["response_format"])
	require.NotContains(t, upstreamBody, "size")
	require.NotContains(t, upstreamBody, "background")
	require.NotContains(t, upstreamBody, "output_format")
}

func TestImageStudioRejectsProviderUnsupportedAndUnknownFields(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		body     string
	}{
		{
			name: "gemini quality", provider: service.PlatformGemini,
			body: `{"api_key_id":7,"model":"gemini-3.1-flash-image","prompt":"draw","aspect_ratio":"1:1","quality":"high"}`,
		},
		{
			name: "gemini count", provider: service.PlatformGemini,
			body: `{"api_key_id":7,"model":"gemini-3.1-flash-image","prompt":"draw","aspect_ratio":"1:1","n":1}`,
		},
		{
			name: "grok background", provider: service.PlatformGrok,
			body: `{"api_key_id":7,"model":"grok-imagine-image-2.0","prompt":"draw","aspect_ratio":"1:1","resolution":"1k","quality":"medium","n":1,"background":"auto"}`,
		},
		{
			name: "unknown field", provider: service.PlatformGrok,
			body: `{"api_key_id":7,"model":"grok-imagine-image-2.0","prompt":"draw","aspect_ratio":"1:1","resolution":"1k","quality":"medium","n":1,"seed":7}`,
		},
		{
			name: "grok too many", provider: service.PlatformGrok,
			body: `{"api_key_id":7,"model":"grok-imagine-image-2.0","prompt":"draw","aspect_ratio":"1:1","resolution":"1k","quality":"medium","n":11}`,
		},
		{
			name: "gemini 2.5 image size", provider: service.PlatformGemini,
			body: `{"api_key_id":7,"model":"gemini-2.5-flash-image","prompt":"draw","aspect_ratio":"1:1","image_size":"1K"}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := &ImageStudioHandler{
				apiKeys: imageStudioKeyLoaderStub{key: eligibleImageStudioKeyForPlatform(42, test.provider)},
				httpClient: &http.Client{Transport: imageStudioRoundTripFunc(func(*http.Request) (*http.Response, error) {
					t.Fatal("invalid request reached the gateway")
					return nil, nil
				})},
				cfg: &config.Config{Server: config.ServerConfig{Host: "127.0.0.1", Port: 8080}},
			}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/generations", strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			imageStudioTestRouter(handler).ServeHTTP(recorder, request)
			require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
		})
	}
}

func TestImageStudioPricingUsesProviderModelSizes(t *testing.T) {
	tests := []struct {
		name       string
		provider   string
		model      string
		wantSizes  []string
		resolution bool
	}{
		{name: "gemini lite", provider: service.PlatformGemini, model: "gemini-3.1-flash-lite-image", wantSizes: []string{"1K"}},
		{name: "gemini pro", provider: service.PlatformGemini, model: "gemini-3-pro-image", wantSizes: []string{"1K", "2K", "4K"}},
		{name: "grok", provider: service.PlatformGrok, model: imageStudioDefaultGrokModel, wantSizes: []string{"1K", "2K"}, resolution: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := &ImageStudioHandler{
				apiKeys:       imageStudioKeyLoaderStub{key: eligibleImageStudioKeyForPlatform(42, test.provider)},
				pricingQuoter: imageStudioQuoterStub{price: 0.25},
			}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/pricing", strings.NewReader(`{"api_key_id":7,"model":"`+test.model+`"}`))
			request.Header.Set("Content-Type", "application/json")
			imageStudioTestRouter(handler).ServeHTTP(recorder, request)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			var payload struct {
				Data imageStudioPricingResponse `json:"data"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
			require.Equal(t, test.provider, payload.Data.Provider)
			require.Equal(t, test.model, payload.Data.Model)
			sizes := make([]string, 0, len(payload.Data.Prices))
			for _, price := range payload.Data.Prices {
				sizes = append(sizes, price.Size)
				if test.resolution {
					require.NotEmpty(t, price.Resolution)
				}
			}
			require.Equal(t, test.wantSizes, sizes)
		})
	}
}
