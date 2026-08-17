package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestParseGrokMediaRequestPreservesDocumentedImageControlsAndBillingResolution(t *testing.T) {
	body := []byte(`{
		"model":"grok-imagine-image-2.0",
		"prompt":"draw a cat",
		"aspect_ratio":"16:9",
		"resolution":"1k",
		"quality":"low",
		"n":4,
		"response_format":"b64_json"
	}`)

	info := ParseGrokMediaRequest("application/json", body)
	require.Equal(t, "grok-imagine-image-2.0", info.Model)
	require.Equal(t, "16:9", info.AspectRatio)
	require.Equal(t, "1k", info.ImageResolution)
	require.Equal(t, ImageBillingSize1K, info.SizeTier)
	require.Equal(t, "low", info.Quality)
	require.Equal(t, 4, info.N)
	require.Equal(t, "b64_json", info.ResponseFormat)

	meta := grokMediaUsageFromResponse(
		GrokMediaEndpointImagesGenerations,
		info,
		[]byte(`{"data":[{"b64_json":"aGVsbG8=","mime_type":"image/jpeg"}]}`),
	)
	require.Equal(t, 1, meta.ImageCount)
	require.Equal(t, ImageBillingSize1K, meta.ImageSize)
	require.Equal(t, "1k", meta.ImageInputSize)
}

func TestGrokMediaImageSanitizerPreservesDocumentedControls(t *testing.T) {
	body := []byte(`{
		"model":"grok-imagine-image-2.0",
		"prompt":"draw a cat",
		"aspect_ratio":"3:2",
		"resolution":"2k",
		"quality":"medium",
		"n":10,
		"response_format":"b64_json",
		"size":"1024x1024"
	}`)

	out, contentType, err := sanitizeGrokMediaForwardBody(GrokMediaEndpointImagesGenerations, body, "application/json")
	require.NoError(t, err)
	require.Equal(t, "application/json", contentType)
	require.False(t, gjson.GetBytes(out, "size").Exists(), "legacy OpenAI size must not reach xAI")
	require.Equal(t, "3:2", gjson.GetBytes(out, "aspect_ratio").String())
	require.Equal(t, "2k", gjson.GetBytes(out, "resolution").String())
	require.Equal(t, "medium", gjson.GetBytes(out, "quality").String())
	require.Equal(t, int64(10), gjson.GetBytes(out, "n").Int())
	require.Equal(t, "b64_json", gjson.GetBytes(out, "response_format").String())
}
