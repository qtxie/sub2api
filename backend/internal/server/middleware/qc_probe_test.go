package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestQCProbeRouting_AlwaysStoresSelectionSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(QCProbeRouting(nil))

	var sawSelection bool
	r.POST("/chat/completions", func(c *gin.Context) {
		sel, ok := service.QCProbeSelectionFromContext(c.Request.Context())
		require.True(t, ok, "QC selection snapshot must always be stored")
		// nil settingService uses defaults (feature off).
		require.False(t, sel.Active)
		require.False(t, service.IsQCProbeClient(c.Request.Context()))
		sawSelection = true
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "/chat/completions", nil)
	req.Header.Set("Origin", "https://tokensqc.com")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusNoContent, w.Code)
	require.True(t, sawSelection)
}
