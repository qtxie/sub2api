package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGeminiV1BetaInteractionsRejectsInvalidOrUnsupportedRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "invalid json", body: `{`, want: "Invalid interactions request body"},
		{name: "missing model", body: `{"input":"draw"}`, want: "Missing model"},
		{name: "unsafe model", body: `{"model":"../image","input":"draw"}`, want: "Invalid model"},
		{name: "streaming", body: `{"model":"gemini-3.1-flash-image","input":"draw","stream":true}`, want: "Streaming interactions are not supported"},
		{name: "background", body: `{"model":"gemini-3.1-flash-image","input":"draw","background":true}`, want: "Background interactions are not supported"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/interactions", strings.NewReader(tt.body))

			(&GatewayHandler{}).GeminiV1BetaInteractions(c)

			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Contains(t, rec.Body.String(), tt.want)
		})
	}
}
