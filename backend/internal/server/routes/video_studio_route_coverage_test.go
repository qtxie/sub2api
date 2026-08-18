package routes

import (
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRegisterUserRoutesIncludesVideoStudioEndpoints(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	v1 := router.Group("/api/v1")
	passthrough := func(c *gin.Context) { c.Next() }

	RegisterUserRoutes(
		v1,
		&handler.Handlers{},
		servermiddleware.JWTAuthMiddleware(passthrough),
		servermiddleware.AuditLogMiddleware(passthrough),
		nil,
		servermiddleware.NewPanelRateLimiter(nil, nil),
	)

	want := map[string]bool{
		http.MethodPost + " /api/v1/video-studio/capabilities":              false,
		http.MethodPost + " /api/v1/video-studio/pricing":                   false,
		http.MethodPost + " /api/v1/video-studio/generations":               false,
		http.MethodGet + " /api/v1/video-studio/videos/:request_id":         false,
		http.MethodGet + " /api/v1/video-studio/videos/:request_id/content": false,
	}
	for _, route := range router.Routes() {
		key := route.Method + " " + route.Path
		if _, ok := want[key]; ok {
			want[key] = true
		}
	}
	for route, found := range want {
		require.Truef(t, found, "missing route %s", route)
	}
}
