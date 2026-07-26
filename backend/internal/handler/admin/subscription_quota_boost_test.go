package admin

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestQuotaBoostPolicyRequestRequiresMonthlyLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.PUT("/quota-boost", func(c *gin.Context) {
		var req QuotaBoostPolicyRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.Status(http.StatusBadRequest)
			return
		}
		c.JSON(http.StatusOK, gin.H{"monthly_limit": *req.MonthlyLimit})
	})

	for _, tt := range []struct {
		name       string
		body       string
		wantStatus int
	}{
		{name: "missing", body: `{}`, wantStatus: http.StatusBadRequest},
		{name: "null", body: `{"monthly_limit":null}`, wantStatus: http.StatusBadRequest},
		{name: "negative", body: `{"monthly_limit":-1}`, wantStatus: http.StatusBadRequest},
		{name: "too large", body: `{"monthly_limit":32}`, wantStatus: http.StatusBadRequest},
		{name: "zero", body: `{"monthly_limit":0}`, wantStatus: http.StatusOK},
	} {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPut, "/quota-boost", bytes.NewBufferString(tt.body))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, request)
			require.Equal(t, tt.wantStatus, recorder.Code)
		})
	}
}
