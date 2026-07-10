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

func TestBuildImagePlaygroundGatewayBodyPinsGPTImage2(t *testing.T) {
	body, err := buildImagePlaygroundGatewayBody(imagePlaygroundGenerationRequest{
		Prompt:       "  an orange bicycle  ",
		Size:         "1024x1024",
		Quality:      "high",
		Background:   "transparent",
		OutputFormat: "png",
		OutputCount:  2,
	})
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(body, &payload))
	require.Equal(t, imagePlaygroundModel, payload["model"])
	require.Equal(t, "b64_json", payload["response_format"])
	require.Equal(t, "an orange bicycle", payload["prompt"])
	require.EqualValues(t, 2, payload["n"])
	require.Equal(t, "1024x1024", payload["size"])
	require.Equal(t, "high", payload["quality"])
	require.Equal(t, "transparent", payload["background"])
	require.Equal(t, "png", payload["output_format"])
}

func TestBuildImagePlaygroundGatewayBodyOmitsBlankOptions(t *testing.T) {
	body, err := buildImagePlaygroundGatewayBody(imagePlaygroundGenerationRequest{Prompt: "cat", OutputCount: 1})
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(body, &payload))
	require.Equal(t, imagePlaygroundModel, payload["model"])
	require.Equal(t, "b64_json", payload["response_format"])
	require.NotContains(t, payload, "size")
	require.NotContains(t, payload, "quality")
	require.NotContains(t, payload, "background")
	require.NotContains(t, payload, "output_format")
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

func TestImagePlaygroundGenerateForwardsPinnedRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newImagePlaygroundTestHandler(imagePlaygroundTestKey(1, service.StatusActive, service.PlatformOpenAI, true), func(req *http.Request) (*http.Response, error) {
		require.Equal(t, http.MethodPost, req.Method)
		require.Equal(t, "/v1/images/generations", req.URL.Path)
		require.Equal(t, "Bearer playground-secret", req.Header.Get("Authorization"))
		var body map[string]any
		require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
		require.Equal(t, imagePlaygroundModel, body["model"])
		require.Equal(t, "b64_json", body["response_format"])
		require.Equal(t, "cat", body["prompt"])
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewBufferString(`{"created":1,"data":[{"b64_json":"abc"}]}`)),
		}, nil
	})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/image-playground/generations", bytes.NewBufferString(`{"api_key_id":1,"prompt":"cat","n":1}`))
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
