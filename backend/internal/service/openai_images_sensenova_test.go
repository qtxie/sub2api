package service

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func sensenovaImagesTestContext(t *testing.T, body []byte) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	return c
}

// TestParseOpenAIImagesRequestForPlatform_DefaultModel 校验空 model 的缺省值随分组平台变化：
// sensenova 分组缺省 sensenova-u1.5-lite，openai 分组维持 gpt-image-2。
func TestParseOpenAIImagesRequestForPlatform_DefaultModel(t *testing.T) {
	svc := &OpenAIGatewayService{}

	body := []byte(`{"prompt":"draw a seal"}`)
	parsed, err := svc.ParseOpenAIImagesRequestForPlatform(sensenovaImagesTestContext(t, body), body, PlatformSensenova)
	require.NoError(t, err)
	require.Equal(t, SensenovaDefaultImageModel, parsed.Model)
	require.Equal(t, OpenAIImagesCapabilityNative, parsed.RequiredCapability)

	parsed, err = svc.ParseOpenAIImagesRequestForPlatform(sensenovaImagesTestContext(t, body), body, PlatformOpenAI)
	require.NoError(t, err)
	require.Equal(t, "gpt-image-2", parsed.Model)

	// 兼容入口保持 openai 缺省语义。
	parsed, err = svc.ParseOpenAIImagesRequest(sensenovaImagesTestContext(t, body), body)
	require.NoError(t, err)
	require.Equal(t, "gpt-image-2", parsed.Model)
}

// TestValidateOpenAIImagesModelForPlatform 校验分组平台域内的图片模型收窄：
// sensenova 分组只收 sensenova-u*，openai 分组只收 gpt-image-*，跨平台模型直接拒绝。
func TestValidateOpenAIImagesModelForPlatform(t *testing.T) {
	require.NoError(t, ValidateOpenAIImagesModelForPlatform(PlatformSensenova, "sensenova-u1.5-lite"))
	require.NoError(t, ValidateOpenAIImagesModelForPlatform(PlatformSensenova, "sensenova-u1.5-fast"))
	require.Error(t, ValidateOpenAIImagesModelForPlatform(PlatformSensenova, "gpt-image-2"))
	require.Error(t, ValidateOpenAIImagesModelForPlatform(PlatformSensenova, "grok-imagine-image-2.0"))

	require.NoError(t, ValidateOpenAIImagesModelForPlatform(PlatformOpenAI, "gpt-image-2"))
	require.Error(t, ValidateOpenAIImagesModelForPlatform(PlatformOpenAI, "sensenova-u1.5-lite"))

	// 其余平台（grok 走专用解析）不做二次约束，仅维持全局白名单。
	require.NoError(t, ValidateOpenAIImagesModelForPlatform(PlatformGrok, "sensenova-u1.5-lite"))
}

func TestIsSensenovaImageGenerationModel(t *testing.T) {
	require.True(t, IsSensenovaImageGenerationModel("sensenova-u1.5-lite"))
	require.True(t, IsSensenovaImageGenerationModel("sensenova-u1.5-fast"))
	require.True(t, IsSensenovaImageGenerationModel("Sensenova-U1.5-Lite"))
	require.True(t, IsSensenovaImageGenerationModel("sensenova-u2-future"))
	require.False(t, IsSensenovaImageGenerationModel("gpt-image-2"))
	require.False(t, IsSensenovaImageGenerationModel("sensenova-chat"))
	require.False(t, IsSensenovaImageGenerationModel(""))
}

// TestParseOpenAIImagesRequest_SensenovaEditsShape 校验 sensenova 分组的 JSON edits
// （images[].image_url，支持 data URL）解析路径与 OpenAI 一致。
func TestParseOpenAIImagesRequest_SensenovaEditsShape(t *testing.T) {
	svc := &OpenAIGatewayService{}
	body := []byte(`{"model":"sensenova-u1.5-lite","prompt":"背景换成冰川","images":[{"image_url":"data:image/png;base64,aGVsbG8="}],"size":"auto","response_format":"url","watermark":false}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	parsed, err := svc.ParseOpenAIImagesRequestForPlatform(c, body, PlatformSensenova)
	require.NoError(t, err)
	require.Equal(t, "/v1/images/edits", parsed.Endpoint)
	require.Equal(t, "sensenova-u1.5-lite", parsed.Model)
	require.Equal(t, []string{"data:image/png;base64,aGVsbG8="}, parsed.InputImageURLs)
	require.Equal(t, "auto", parsed.Size)
	require.Equal(t, "url", parsed.ResponseFormat)
}
