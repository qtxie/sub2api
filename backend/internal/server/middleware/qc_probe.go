package middleware

import (
	"log/slog"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// QCProbeRouting detects AI QC-site probe traffic and stores the routing decision
// on the request context for account selection.
func QCProbeRouting(settingService *service.SettingService) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c == nil || c.Request == nil {
			c.Next()
			return
		}

		settings := service.DefaultQCProbeRoutingSettings()
		if settingService != nil {
			settings = settingService.GetQCProbeRoutingSettings(c.Request.Context())
		}

		selection := service.DetectQCProbeRequest(
			c.GetHeader("Origin"),
			c.GetHeader("Referer"),
			c.GetHeader("User-Agent"),
			settings,
		)
		ctx := service.WithQCProbeSelection(c.Request.Context(), selection)
		c.Request = c.Request.WithContext(ctx)

		if selection.Active {
			slog.Info("qc_probe.detected",
				"source", selection.Source,
				"fallback", selection.Fallback,
				"account_pool", len(selection.AccountIDs),
				"path", c.Request.URL.Path,
			)
		}
		c.Next()
	}
}
