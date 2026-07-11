package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type imagePlaygroundAPIKeyRepo struct {
	service.APIKeyRepository
	apiKey *service.APIKey
}

func (r imagePlaygroundAPIKeyRepo) GetByID(context.Context, int64) (*service.APIKey, error) {
	if r.apiKey == nil {
		return nil, nil
	}
	copy := *r.apiKey
	return &copy, nil
}

type imagePlaygroundRoundTripper func(*http.Request) (*http.Response, error)

func (f imagePlaygroundRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type imagePlaygroundPricingQuoterStub struct {
	pricingKind string
	prices      map[string]float64
	calls       []imagePlaygroundPricingQuoteCall
}

type imagePlaygroundPricingQuoteCall struct {
	model string
	size  string
}

func (s *imagePlaygroundPricingQuoterStub) QuoteImageUnitPrice(
	_ context.Context,
	_ *service.APIKey,
	_ int64,
	model string,
	size string,
) (*service.ImageUnitPriceQuote, error) {
	s.calls = append(s.calls, imagePlaygroundPricingQuoteCall{model: model, size: size})
	if s.pricingKind == service.ImagePricingKindUsageBased {
		return &service.ImageUnitPriceQuote{PricingKind: service.ImagePricingKindUsageBased}, nil
	}
	price := s.prices[size]
	return &service.ImageUnitPriceQuote{PricingKind: service.ImagePricingKindFixed, UnitPrice: &price}, nil
}

func TestBuildImagePlaygroundGatewayBodyPinsGPTImage2(t *testing.T) {
	body, err := buildImagePlaygroundGatewayBody(imagePlaygroundGenerationRequest{
		Prompt:       "  an orange bicycle  ",
		Size:         "1024x1024",
		Quality:      "high",
		Background:   "auto",
		OutputFormat: "png",
		OutputCount:  2,
	})
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(body, &payload))
	require.Equal(t, imagePlaygroundOpenAIModel, payload["model"])
	require.Equal(t, true, payload["stream"])
	require.NotContains(t, payload, "response_format")
	require.Equal(t, "an orange bicycle", payload["prompt"])
	require.EqualValues(t, 2, payload["n"])
	require.Equal(t, "1024x1024", payload["size"])
	require.Equal(t, "high", payload["quality"])
	require.Equal(t, "auto", payload["background"])
	require.Equal(t, "png", payload["output_format"])
}

func TestBuildImagePlaygroundGatewayBodyOmitsBlankOptions(t *testing.T) {
	body, err := buildImagePlaygroundGatewayBody(imagePlaygroundGenerationRequest{Prompt: "cat", OutputCount: 1})
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(body, &payload))
	require.Equal(t, imagePlaygroundOpenAIModel, payload["model"])
	require.Equal(t, true, payload["stream"])
	require.NotContains(t, payload, "response_format")
	require.NotContains(t, payload, "size")
	require.NotContains(t, payload, "quality")
	require.NotContains(t, payload, "background")
	require.NotContains(t, payload, "output_format")
}

func TestBuildImagePlaygroundGeminiBody(t *testing.T) {
	body, err := buildImagePlaygroundGeminiBody(imagePlaygroundGenerationRequest{
		Prompt: "  a paper kite  ", Size: "1536x1024",
	})
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(body, &payload))
	contents := payload["contents"].([]any)
	parts := contents[0].(map[string]any)["parts"].([]any)
	require.Equal(t, "a paper kite", parts[0].(map[string]any)["text"])
	config := payload["generationConfig"].(map[string]any)
	require.Equal(t, []any{"TEXT", "IMAGE"}, config["responseModalities"])
	require.Equal(t, "3:2", config["imageConfig"].(map[string]any)["aspectRatio"])
}

func TestParseImagePlaygroundGeminiResponse(t *testing.T) {
	images, err := parseImagePlaygroundGeminiResponse([]byte(`{
		"candidates":[{"content":{"parts":[
			{"text":"revised prompt"},
			{"inlineData":{"mimeType":"image/webp","data":"d2VicA=="}}
		]}}]
	}`))
	require.NoError(t, err)
	require.Equal(t, []imagePlaygroundImage{{
		B64JSON: "d2VicA==", MIMEType: "image/webp", RevisedPrompt: "revised prompt",
	}}, images)
}

func TestImagePlaygroundGeminiModelAllowlist(t *testing.T) {
	require.True(t, isImagePlaygroundGeminiModel("gemini-3.1-flash-image"))
	require.True(t, isImagePlaygroundGeminiModel("models/gemini-2.5-flash-image"))
	require.False(t, isImagePlaygroundGeminiModel("gemini-3.1-flash-image/../../other"))
	require.False(t, isImagePlaygroundGeminiModel("gemini-3.1-flash-image-unknown"))
}

func TestGatewayErrorMessage(t *testing.T) {
	require.Equal(t, "rate limited", gatewayErrorMessage([]byte(`{"error":{"message":"rate limited"}}`)))
	require.Equal(t, "plain error", gatewayErrorMessage([]byte("plain error")))
}

func TestReadImagePlaygroundResponseRejectsOverflow(t *testing.T) {
	data, err := readImagePlaygroundResponse(bytes.NewBufferString("12345"), 4)
	require.ErrorIs(t, err, errImagePlaygroundResponseTooLarge)
	require.Nil(t, data)

	data, err = readImagePlaygroundResponse(bytes.NewBufferString("1234"), 4)
	require.NoError(t, err)
	require.Equal(t, []byte("1234"), data)
}

func TestImagePlaygroundPricingMapsOpenAIResolutionsAndPinsModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	key := imagePlaygroundTestKey(1, service.StatusActive, service.PlatformOpenAI, true)
	quoter := &imagePlaygroundPricingQuoterStub{prices: map[string]float64{"1K": 0.1, "2K": 0.15, "4K": 0.3}}
	h := newImagePlaygroundTestHandler(key, nil)
	h.pricingQuoter = quoter

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/image-playground/pricing", bytes.NewBufferString(`{"api_key_id":1,"model":"ignored-model"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 1})
	h.Pricing(c)

	require.Equal(t, http.StatusOK, w.Code)
	var output struct {
		Data imagePlaygroundPricingResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &output))
	require.Equal(t, "USD", output.Data.Currency)
	require.Equal(t, service.ImagePricingKindFixed, output.Data.PricingKind)
	require.Equal(t, []imagePlaygroundResolutionPrice{
		{Size: "1024x1024", BillingTier: "1K", PricingKind: service.ImagePricingKindFixed, UnitPriceUSD: float64Pointer(0.1)},
		{Size: "1536x1024", BillingTier: "2K", PricingKind: service.ImagePricingKindFixed, UnitPriceUSD: float64Pointer(0.15)},
		{Size: "1024x1536", BillingTier: "2K", PricingKind: service.ImagePricingKindFixed, UnitPriceUSD: float64Pointer(0.15)},
		{Size: "3840x2160", BillingTier: "4K", PricingKind: service.ImagePricingKindFixed, UnitPriceUSD: float64Pointer(0.3)},
		{Size: "2160x3840", BillingTier: "4K", PricingKind: service.ImagePricingKindFixed, UnitPriceUSD: float64Pointer(0.3)},
	}, output.Data.Prices)
	require.Equal(t, []imagePlaygroundPricingQuoteCall{
		{model: imagePlaygroundOpenAIModel, size: "1K"},
		{model: imagePlaygroundOpenAIModel, size: "2K"},
		{model: imagePlaygroundOpenAIModel, size: "2K"},
		{model: imagePlaygroundOpenAIModel, size: "4K"},
		{model: imagePlaygroundOpenAIModel, size: "4K"},
	}, quoter.calls)
}

func TestImagePlaygroundPricingUsesGeminiModelAndDefaultTier(t *testing.T) {
	gin.SetMode(gin.TestMode)
	key := imagePlaygroundTestKey(1, service.StatusActive, service.PlatformGemini, true)
	quoter := &imagePlaygroundPricingQuoterStub{pricingKind: service.ImagePricingKindUsageBased}
	h := newImagePlaygroundTestHandler(key, nil)
	h.pricingQuoter = quoter

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/image-playground/pricing", bytes.NewBufferString(`{"api_key_id":1,"model":"gemini-2.5-flash-image"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 1})
	h.Pricing(c)

	require.Equal(t, http.StatusOK, w.Code)
	var output struct {
		Data imagePlaygroundPricingResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &output))
	require.Equal(t, service.ImagePricingKindUsageBased, output.Data.PricingKind)
	require.Len(t, output.Data.Prices, 3)
	for _, price := range output.Data.Prices {
		require.Equal(t, service.ImageBillingSize2K, price.BillingTier)
		require.Nil(t, price.UnitPriceUSD)
	}
	for _, call := range quoter.calls {
		require.Equal(t, "gemini-2.5-flash-image", call.model)
		require.Equal(t, service.ImageBillingSize2K, call.size)
	}
}

func float64Pointer(value float64) *float64 {
	return &value
}

func TestImagePlaygroundGenerateRejectsIneligibleKeys(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name   string
		key    *service.APIKey
		userID int64
		want   int
	}{
		{name: "other user key", key: imagePlaygroundTestKey(2, service.StatusActive, service.PlatformOpenAI, true), userID: 1, want: http.StatusNotFound},
		{name: "inactive key", key: imagePlaygroundTestKey(1, service.StatusDisabled, service.PlatformOpenAI, true), userID: 1, want: http.StatusBadRequest},
		{name: "group disabled", key: imagePlaygroundTestKey(1, service.StatusActive, service.PlatformOpenAI, false), userID: 1, want: http.StatusForbidden},
		{name: "grok group", key: imagePlaygroundTestKey(1, service.StatusActive, service.PlatformGrok, true), userID: 1, want: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newImagePlaygroundTestHandler(tt.key, func(_ *http.Request) (*http.Response, error) {
				t.Fatal("gateway must not be called")
				return nil, nil
			})
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/image-playground/generations", bytes.NewBufferString(`{"api_key_id":1,"prompt":"cat","n":1}`))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: tt.userID})
			h.Generate(c)
			require.Equal(t, tt.want, w.Code)
		})
	}
}

func TestImagePlaygroundGenerateUsesGeminiNativeGateway(t *testing.T) {
	gin.SetMode(gin.TestMode)
	requestCount := 0
	h := newImagePlaygroundTestHandler(imagePlaygroundTestKey(1, service.StatusActive, service.PlatformGemini, true), func(req *http.Request) (*http.Response, error) {
		requestCount++
		require.Equal(t, "/v1beta/models/gemini-3.1-flash-image:generateContent", req.URL.Path)
		require.Equal(t, "playground-secret", req.Header.Get("x-goog-api-key"))
		require.Empty(t, req.Header.Get("Authorization"))
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewBufferString(`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"cG5n"}}]}}]}`)),
		}, nil
	})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/image-playground/generations", bytes.NewBufferString(`{"api_key_id":1,"model":"gemini-3.1-flash-image","prompt":"cat","n":2}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 1})

	h.Generate(c)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 2, requestCount)
	var output struct {
		Data struct {
			Data []imagePlaygroundImage `json:"data"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &output))
	require.Len(t, output.Data.Data, 2)
	require.Equal(t, "image/png", output.Data.Data[0].MIMEType)
}

func TestImagePlaygroundGenerateRejectsOpenAI4KSizeForGemini(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newImagePlaygroundTestHandler(imagePlaygroundTestKey(1, service.StatusActive, service.PlatformGemini, true), func(_ *http.Request) (*http.Response, error) {
		t.Fatal("gateway must not be called")
		return nil, nil
	})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/image-playground/generations", bytes.NewBufferString(`{"api_key_id":1,"model":"gemini-3.1-flash-image","prompt":"cat","size":"3840x2160","n":1}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 1})

	h.Generate(c)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "Unsupported image size")
}

func TestImagePlaygroundGenerateUsesAntigravityGatewayPrefix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newImagePlaygroundTestHandler(imagePlaygroundTestKey(1, service.StatusActive, service.PlatformAntigravity, true), func(req *http.Request) (*http.Response, error) {
		require.Equal(t, "/antigravity/v1beta/models/gemini-2.5-flash-image:generateContent", req.URL.Path)
		return &http.Response{
			StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(bytes.NewBufferString(`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"cG5n"}}]}}]}`)),
		}, nil
	})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/image-playground/generations", bytes.NewBufferString(`{"api_key_id":1,"model":"gemini-2.5-flash-image","prompt":"cat","n":1}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 1})

	h.Generate(c)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestImagePlaygroundGenerateRejectsMoreThanFourImages(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newImagePlaygroundTestHandler(imagePlaygroundTestKey(1, service.StatusActive, service.PlatformOpenAI, true), func(_ *http.Request) (*http.Response, error) {
		t.Fatal("gateway must not be called")
		return nil, nil
	})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/image-playground/generations", bytes.NewBufferString(`{"api_key_id":1,"prompt":"cat","n":5}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 1})

	h.Generate(c)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "between 1 and 4")
}

func TestImagePlaygroundGenerateRejectsTransparentBackground(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newImagePlaygroundTestHandler(imagePlaygroundTestKey(1, service.StatusActive, service.PlatformOpenAI, true), func(_ *http.Request) (*http.Response, error) {
		t.Fatal("gateway must not be called")
		return nil, nil
	})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/image-playground/generations", bytes.NewBufferString(`{"api_key_id":1,"prompt":"cat","background":"transparent","output_format":"png","n":1}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 1})

	h.Generate(c)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "Unsupported image background")
}

func TestImagePlaygroundGenerateForwardsPinnedRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newImagePlaygroundTestHandler(imagePlaygroundTestKey(1, service.StatusActive, service.PlatformOpenAI, true), func(req *http.Request) (*http.Response, error) {
		require.Equal(t, http.MethodPost, req.Method)
		require.Equal(t, "/v1/images/generations", req.URL.Path)
		require.Equal(t, "Bearer playground-secret", req.Header.Get("Authorization"))
		require.Equal(t, "text/event-stream, application/json", req.Header.Get("Accept"))
		var body map[string]any
		require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
		require.Equal(t, imagePlaygroundOpenAIModel, body["model"])
		require.Equal(t, true, body["stream"])
		require.NotContains(t, body, "response_format")
		require.Equal(t, "cat", body["prompt"])
		require.Equal(t, "3840x2160", body["size"])
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewBufferString(`{"created":1,"data":[{"b64_json":"abc"}]}`)),
		}, nil
	})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/image-playground/generations", bytes.NewBufferString(`{"api_key_id":1,"prompt":"cat","size":"3840x2160","n":1}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 1})
	h.Generate(c)
	require.Equal(t, http.StatusOK, w.Code)
	var body struct {
		Code int `json:"code"`
		Data struct {
			Data []struct {
				B64JSON string `json:"b64_json"`
			} `json:"data"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, 0, body.Code)
	require.Equal(t, "abc", body.Data.Data[0].B64JSON)
}

func TestImagePlaygroundGenerateRelaysGatewayStream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stream := "event: image_generation.partial_image\n" +
		"data: {\"type\":\"image_generation.partial_image\",\"b64_json\":\"partial\"}\n\n" +
		":\n\n" +
		"event: image_generation.completed\n" +
		"data: {\"type\":\"image_generation.completed\",\"b64_json\":\"final\"}\n\n" +
		"data: [DONE]\n\n"
	h := newImagePlaygroundTestHandler(imagePlaygroundTestKey(1, service.StatusActive, service.PlatformOpenAI, true), func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(bytes.NewBufferString(stream)),
		}, nil
	})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/image-playground/generations", bytes.NewBufferString(`{"api_key_id":1,"prompt":"cat","n":1}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 1})

	h.Generate(c)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "text/event-stream; charset=utf-8", w.Header().Get("Content-Type"))
	require.Equal(t, "no", w.Header().Get("X-Accel-Buffering"))
	require.True(t, w.Flushed)
	require.Equal(t, stream, w.Body.String())
}

func TestRelayImagePlaygroundStreamReportsOverflowInBand(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	err := relayImagePlaygroundStream(c, bytes.NewBufferString("12345"), http.StatusOK, 4)

	require.ErrorIs(t, err, errImagePlaygroundResponseTooLarge)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "1234")
	require.Contains(t, w.Body.String(), "event: error")
	require.Contains(t, w.Body.String(), "response is too large")
}

func TestImagePlaygroundGeneratePassesGatewayErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newImagePlaygroundTestHandler(imagePlaygroundTestKey(1, service.StatusActive, service.PlatformOpenAI, true), func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusTooManyRequests, Header: make(http.Header), Body: io.NopCloser(bytes.NewBufferString(`{"error":{"message":"rate limited"}}`))}, nil
	})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/image-playground/generations", bytes.NewBufferString(`{"api_key_id":1,"prompt":"cat","n":1}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 1})
	h.Generate(c)
	require.Equal(t, http.StatusTooManyRequests, w.Code)
	require.Contains(t, w.Body.String(), "rate limited")
}

func imagePlaygroundTestKey(userID int64, status, platform string, allowImageGeneration bool) *service.APIKey {
	return &service.APIKey{
		ID: 1, UserID: userID, Key: "playground-secret", Status: status,
		Group: &service.Group{Platform: platform, AllowImageGeneration: allowImageGeneration},
	}
}

func newImagePlaygroundTestHandler(apiKey *service.APIKey, roundTrip imagePlaygroundRoundTripper) *ImagePlaygroundHandler {
	apiKeyService := service.NewAPIKeyService(imagePlaygroundAPIKeyRepo{apiKey: apiKey}, nil, nil, nil, nil, nil, &config.Config{})
	return &ImagePlaygroundHandler{
		apiKeyService: apiKeyService,
		cfg:           &config.Config{Server: config.ServerConfig{Host: "127.0.0.1", Port: 8080}},
		httpClient:    &http.Client{Transport: roundTrip},
	}
}
