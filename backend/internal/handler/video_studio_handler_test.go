package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

type videoStudioKeyLoaderStub struct {
	key *service.APIKey
	err error
}

func (s videoStudioKeyLoaderStub) GetByID(context.Context, int64) (*service.APIKey, error) {
	return s.key, s.err
}

type videoStudioQuoteCall struct {
	model      string
	resolution string
	duration   int
}

type videoStudioQuoterStub struct {
	calls []videoStudioQuoteCall
}

type videoStudioTrackerStub struct {
	task  service.VideoStudioTask
	err   error
	calls int
}

func (s *videoStudioTrackerStub) Register(_ context.Context, task service.VideoStudioTask) error {
	s.calls++
	s.task = task
	return s.err
}

func (s *videoStudioQuoterStub) QuoteVideoPrice(
	_ context.Context,
	_ *service.APIKey,
	_ int64,
	model string,
	resolution string,
	duration int,
) (*service.VideoPriceQuote, error) {
	s.calls = append(s.calls, videoStudioQuoteCall{model: model, resolution: resolution, duration: duration})
	unit := map[string]float64{"480p": 0.08, "720p": 0.14, "1080p": 0.25}[resolution]
	total := unit * float64(duration)
	return &service.VideoPriceQuote{
		PricingKind: service.VideoPricingKindFixed,
		UnitPrice:   &unit,
		TotalPrice:  &total,
	}, nil
}

type videoStudioRoundTripFunc func(*http.Request) (*http.Response, error)

func (f videoStudioRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func videoStudioTestRouter(handler *VideoStudioHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(servermiddleware.ContextKeyUser), servermiddleware.AuthSubject{UserID: 42})
		c.Next()
	})
	router.POST("/capabilities", handler.Capabilities)
	router.POST("/pricing", handler.Pricing)
	router.POST("/generations", handler.Generate)
	router.GET("/videos/:request_id", handler.Status)
	router.GET("/videos/:request_id/content", handler.Content)
	return router
}

func eligibleVideoStudioKey(userID int64) *service.APIKey {
	groupID := int64(8)
	return &service.APIKey{
		ID: 7, UserID: userID, Key: "sk-grok-secret", GroupID: &groupID, Status: service.StatusActive,
		Group: &service.Group{ID: groupID, Platform: service.PlatformGrok, AllowImageGeneration: true},
	}
}

func newVideoStudioHandlerForTest(key *service.APIKey, roundTripper http.RoundTripper) *VideoStudioHandler {
	handler := &VideoStudioHandler{
		apiKeys: videoStudioKeyLoaderStub{key: key},
		cfg:     &config.Config{Server: config.ServerConfig{Host: "0.0.0.0", Port: 8080}},
	}
	if roundTripper != nil {
		handler.httpClient = &http.Client{Transport: roundTripper}
	} else {
		handler.httpClient = &http.Client{}
	}
	return handler
}

func TestVideoStudioGenerateInjectsFixedModelAndForwardsExactFields(t *testing.T) {
	var forwarded map[string]any
	tracker := &videoStudioTrackerStub{}
	handler := newVideoStudioHandlerForTest(eligibleVideoStudioKey(42), videoStudioRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		require.Equal(t, http.MethodPost, request.Method)
		require.Equal(t, "http://127.0.0.1:8080/v1/videos/generations", request.URL.String())
		require.Equal(t, "Bearer sk-grok-secret", request.Header.Get("Authorization"))
		require.NoError(t, json.NewDecoder(request.Body).Decode(&forwarded))
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"task_id":"video-task-123","status":"done"}`)),
		}, nil
	}))
	handler.tracker = tracker

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/generations", strings.NewReader(`{
		"api_key_id":7,"prompt":" waves at dusk ","duration":15,"aspect_ratio":"16:9","resolution":"1080p"
	}`))
	request.Header.Set("Content-Type", "application/json")
	videoStudioTestRouter(handler).ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, map[string]any{
		"model":        videoStudioModel,
		"prompt":       "waves at dusk",
		"duration":     float64(15),
		"aspect_ratio": "16:9",
		"resolution":   "1080p",
	}, forwarded)
	var envelope struct {
		Data videoStudioGenerationResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	require.Equal(t, "video-task-123", envelope.Data.RequestID)
	require.Equal(t, "pending", envelope.Data.Status)
	require.Equal(t, service.VideoStudioTask{
		RequestID: "video-task-123", UserID: 42, APIKeyID: 7, Model: videoStudioModel,
		Duration: 15, AspectRatio: "16:9", Resolution: "1080p",
	}, tracker.task)
}

func TestVideoStudioGenerateRejectsUnsupportedOrUnknownFields(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "client model", body: `{"api_key_id":7,"model":"grok-imagine-video","prompt":"waves","duration":8,"aspect_ratio":"16:9","resolution":"720p"}`},
		{name: "unknown option", body: `{"api_key_id":7,"prompt":"waves","duration":8,"aspect_ratio":"16:9","resolution":"720p","seed":4}`},
		{name: "zero duration", body: `{"api_key_id":7,"prompt":"waves","duration":0,"aspect_ratio":"16:9","resolution":"720p"}`},
		{name: "long duration", body: `{"api_key_id":7,"prompt":"waves","duration":16,"aspect_ratio":"16:9","resolution":"720p"}`},
		{name: "unsupported ratio", body: `{"api_key_id":7,"prompt":"waves","duration":8,"aspect_ratio":"21:9","resolution":"720p"}`},
		{name: "unsupported resolution", body: `{"api_key_id":7,"prompt":"waves","duration":8,"aspect_ratio":"16:9","resolution":"4k"}`},
		{name: "blank prompt", body: `{"api_key_id":7,"prompt":"  ","duration":8,"aspect_ratio":"16:9","resolution":"720p"}`},
		{name: "two objects", body: `{"api_key_id":7,"prompt":"waves","duration":8,"aspect_ratio":"16:9","resolution":"720p"}{}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := newVideoStudioHandlerForTest(eligibleVideoStudioKey(42), videoStudioRoundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("invalid request reached the gateway")
				return nil, nil
			}))
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/generations", strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			videoStudioTestRouter(handler).ServeHTTP(recorder, request)
			require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
		})
	}
}

func TestVideoStudioRequiresOwnedActivePermittedGrokKey(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*service.APIKey)
		wantStatus int
	}{
		{name: "foreign", mutate: func(key *service.APIKey) { key.UserID = 99 }, wantStatus: http.StatusNotFound},
		{name: "inactive", mutate: func(key *service.APIKey) { key.Status = service.StatusDisabled }, wantStatus: http.StatusBadRequest},
		{name: "non grok", mutate: func(key *service.APIKey) { key.Group.Platform = service.PlatformOpenAI }, wantStatus: http.StatusBadRequest},
		{name: "composite", mutate: func(key *service.APIKey) { key.Group.Platform = service.PlatformComposite }, wantStatus: http.StatusBadRequest},
		{name: "permission disabled", mutate: func(key *service.APIKey) { key.Group.AllowImageGeneration = false }, wantStatus: http.StatusForbidden},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			key := eligibleVideoStudioKey(42)
			test.mutate(key)
			handler := newVideoStudioHandlerForTest(key, videoStudioRoundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("ineligible key reached the gateway")
				return nil, nil
			}))
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/capabilities", strings.NewReader(`{"api_key_id":7}`))
			request.Header.Set("Content-Type", "application/json")
			videoStudioTestRouter(handler).ServeHTTP(recorder, request)
			require.Equal(t, test.wantStatus, recorder.Code, recorder.Body.String())
		})
	}
}

func TestVideoStudioCapabilitiesExposeOnlySupportedOptions(t *testing.T) {
	handler := newVideoStudioHandlerForTest(eligibleVideoStudioKey(42), nil)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/capabilities", strings.NewReader(`{"api_key_id":7}`))
	request.Header.Set("Content-Type", "application/json")
	videoStudioTestRouter(handler).ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var envelope struct {
		Data videoStudioCapabilitiesResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	require.Equal(t, service.PlatformGrok, envelope.Data.Provider)
	require.Equal(t, videoStudioModel, envelope.Data.Model)
	require.Equal(t, 1, envelope.Data.MinDuration)
	require.Equal(t, 15, envelope.Data.MaxDuration)
	require.Equal(t, 8, envelope.Data.DefaultDuration)
	require.Equal(t, videoStudioAspectRatios, envelope.Data.AspectRatios)
	require.Equal(t, videoStudioResolutions, envelope.Data.Resolutions)
}

func TestVideoStudioPricingQuotesEverySupportedResolution(t *testing.T) {
	quoter := &videoStudioQuoterStub{}
	handler := newVideoStudioHandlerForTest(eligibleVideoStudioKey(42), nil)
	handler.pricingQuoter = quoter
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/pricing", strings.NewReader(`{"api_key_id":7,"duration":10}`))
	request.Header.Set("Content-Type", "application/json")
	videoStudioTestRouter(handler).ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, []videoStudioQuoteCall{
		{model: videoStudioModel, resolution: "480p", duration: 10},
		{model: videoStudioModel, resolution: "720p", duration: 10},
		{model: videoStudioModel, resolution: "1080p", duration: 10},
	}, quoter.calls)
	var envelope struct {
		Data videoStudioPricingResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	require.Equal(t, "USD", envelope.Data.Currency)
	require.Equal(t, service.VideoPricingKindFixed, envelope.Data.PricingKind)
	require.Len(t, envelope.Data.Prices, 3)
	require.InDelta(t, 2.5, *envelope.Data.Prices[2].TotalPrice, 1e-10)
}

func TestVideoStudioStatusUsesBoundGatewayAndRemovesSignedURLs(t *testing.T) {
	handler := newVideoStudioHandlerForTest(eligibleVideoStudioKey(42), videoStudioRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		require.Equal(t, http.MethodGet, request.Method)
		require.Equal(t, "http://127.0.0.1:8080/v1/videos/task-123", request.URL.String())
		require.Equal(t, "Bearer sk-grok-secret", request.Header.Get("Authorization"))
		require.Nil(t, request.Body)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
				"status":"done","model":"mapped-upstream-model","counter":9007199254740993,"progress":140,
				"usage":{"prompt_tokens":99},
				"download_url":"/v1/videos/task-123/content",
				"video":{"url":"https://vidgen.x.ai/signed/private.mp4","duration":8,"respect_moderation":false}
			}`)),
		}, nil
	}))

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/videos/task-123?api_key_id=7&timezone=Asia%2FShanghai", nil)
	videoStudioTestRouter(handler).ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.NotContains(t, recorder.Body.String(), "vidgen.x.ai")
	require.NotContains(t, recorder.Body.String(), "/v1/videos/task-123/content")
	require.NotContains(t, recorder.Body.String(), "usage")
	require.NotContains(t, recorder.Body.String(), "counter")
	require.NotContains(t, recorder.Body.String(), "download_url")
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	decoder := json.NewDecoder(bytes.NewReader(recorder.Body.Bytes()))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&envelope))
	require.Equal(t, "task-123", envelope.Data["request_id"])
	require.Equal(t, videoStudioModel, envelope.Data["model"])
	require.Equal(t, service.VideoStudioTaskStatusDone, envelope.Data["status"])
	require.Equal(t, json.Number("100"), envelope.Data["progress"])
	video, ok := envelope.Data["video"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "/videos/task-123/content?api_key_id=7", video["content_url"])
	require.Equal(t, false, video["respect_moderation"])
	require.NotContains(t, video, "url")
}

func TestVideoStudioStatusNormalizesFailureAndSanitizesURLFromError(t *testing.T) {
	handler := newVideoStudioHandlerForTest(eligibleVideoStudioKey(42), videoStudioRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"status":"failed","error":{"message":"fetch https://vidgen.x.ai/signed/private.mp4 failed"}}`)),
		}, nil
	}))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/videos/task-123?api_key_id=7", nil)
	videoStudioTestRouter(handler).ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.NotContains(t, recorder.Body.String(), "vidgen.x.ai")
	var envelope struct {
		Data videoStudioStatusResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	require.Equal(t, service.VideoStudioTaskStatusFailed, envelope.Data.Status)
	require.Equal(t, "Video generation failed", envelope.Data.Error)
}

func TestVideoStudioStatusRejectsUnsupportedUpstreamStatus(t *testing.T) {
	handler := newVideoStudioHandlerForTest(eligibleVideoStudioKey(42), videoStudioRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"status":"queued"}`)),
		}, nil
	}))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/videos/task-123?api_key_id=7", nil)
	videoStudioTestRouter(handler).ServeHTTP(recorder, request)
	require.Equal(t, http.StatusBadGateway, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), "unsupported status")
}

func TestVideoStudioStatusRejectsDoneWithoutContent(t *testing.T) {
	handler := newVideoStudioHandlerForTest(eligibleVideoStudioKey(42), videoStudioRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"status":"done","video":{"duration":8}}`)),
		}, nil
	}))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/videos/task-123?api_key_id=7", nil)
	videoStudioTestRouter(handler).ServeHTTP(recorder, request)
	require.Equal(t, http.StatusBadGateway, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), "without content")
}

func TestVideoStudioStatusRejectsForeignKeyBeforeGateway(t *testing.T) {
	handler := newVideoStudioHandlerForTest(eligibleVideoStudioKey(99), videoStudioRoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("foreign key reached the gateway")
		return nil, nil
	}))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/videos/task-123?api_key_id=7", nil)
	videoStudioTestRouter(handler).ServeHTTP(recorder, request)
	require.Equal(t, http.StatusNotFound, recorder.Code, recorder.Body.String())
}

func TestVideoStudioContentPreservesRangeStatusHeadersAndBody(t *testing.T) {
	handler := newVideoStudioHandlerForTest(eligibleVideoStudioKey(42), videoStudioRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		require.Equal(t, "http://127.0.0.1:8080/v1/videos/task-123/content", request.URL.String())
		require.Equal(t, "bytes=4-7", request.Header.Get("Range"))
		require.Equal(t, "Bearer sk-grok-secret", request.Header.Get("Authorization"))
		return &http.Response{
			StatusCode: http.StatusPartialContent,
			Header: http.Header{
				"Content-Type":   []string{"video/mp4"},
				"Content-Length": []string{"4"},
				"Content-Range":  []string{"bytes 4-7/12"},
				"Accept-Ranges":  []string{"bytes"},
				"Etag":           []string{`"video-etag"`},
				"Set-Cookie":     []string{"upstream-secret=1"},
				"X-Private":      []string{"do-not-copy"},
			},
			Body: io.NopCloser(strings.NewReader("DATA")),
		}, nil
	}))

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/videos/task-123/content?api_key_id=7&timezone=Asia%2FShanghai", nil)
	request.Header.Set("Range", "bytes=4-7")
	videoStudioTestRouter(handler).ServeHTTP(recorder, request)

	require.Equal(t, http.StatusPartialContent, recorder.Code)
	require.Equal(t, "DATA", recorder.Body.String())
	require.Equal(t, "video/mp4", recorder.Header().Get("Content-Type"))
	require.Equal(t, "bytes 4-7/12", recorder.Header().Get("Content-Range"))
	require.Equal(t, "bytes", recorder.Header().Get("Accept-Ranges"))
	require.Equal(t, `"video-etag"`, recorder.Header().Get("ETag"))
	require.Empty(t, recorder.Header().Get("Set-Cookie"))
	require.Empty(t, recorder.Header().Get("X-Private"))
}

func TestVideoStudioContentPreservesRangeNotSatisfiable(t *testing.T) {
	handler := newVideoStudioHandlerForTest(eligibleVideoStudioKey(42), videoStudioRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusRequestedRangeNotSatisfiable,
			Header:     http.Header{"Content-Range": []string{"bytes */12"}},
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	}))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/videos/task-123/content?api_key_id=7", nil)
	request.Header.Set("Range", "bytes=99-")
	videoStudioTestRouter(handler).ServeHTTP(recorder, request)
	require.Equal(t, http.StatusRequestedRangeNotSatisfiable, recorder.Code)
	require.Equal(t, "bytes */12", recorder.Header().Get("Content-Range"))
}

func TestVideoStudioCreateRejectsSuccessfulResponseWithoutTaskID(t *testing.T) {
	handler := newVideoStudioHandlerForTest(eligibleVideoStudioKey(42), videoStudioRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"status":"pending"}`)),
		}, nil
	}))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/generations", strings.NewReader(`{
		"api_key_id":7,"prompt":"waves","duration":8,"aspect_ratio":"16:9","resolution":"720p"
	}`))
	request.Header.Set("Content-Type", "application/json")
	videoStudioTestRouter(handler).ServeHTTP(recorder, request)
	require.Equal(t, http.StatusBadGateway, recorder.Code, recorder.Body.String())
}

func TestVideoStudioCreateRejectsInvalidTaskIDWithoutTracking(t *testing.T) {
	tracker := &videoStudioTrackerStub{}
	handler := newVideoStudioHandlerForTest(eligibleVideoStudioKey(42), videoStudioRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"request_id":"invalid/task"}`)),
		}, nil
	}))
	handler.tracker = tracker
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/generations", strings.NewReader(`{
		"api_key_id":7,"prompt":"waves","duration":8,"aspect_ratio":"16:9","resolution":"720p"
	}`))
	request.Header.Set("Content-Type", "application/json")
	videoStudioTestRouter(handler).ServeHTTP(recorder, request)
	require.Equal(t, http.StatusBadGateway, recorder.Code, recorder.Body.String())
	require.Empty(t, tracker.task.RequestID)
}

func TestVideoStudioTrackerFailureDoesNotDiscardAcceptedRequest(t *testing.T) {
	tracker := &videoStudioTrackerStub{err: errors.New("queue unavailable")}
	handler := newVideoStudioHandlerForTest(eligibleVideoStudioKey(42), videoStudioRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"request_id":"task-accepted"}`)),
		}, nil
	}))
	handler.tracker = tracker
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/generations", strings.NewReader(`{
		"api_key_id":7,"prompt":"waves","duration":8,"aspect_ratio":"16:9","resolution":"720p"
	}`))
	request.Header.Set("Content-Type", "application/json")
	videoStudioTestRouter(handler).ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, "task-accepted", tracker.task.RequestID)
	require.Equal(t, 3, tracker.calls)
}
