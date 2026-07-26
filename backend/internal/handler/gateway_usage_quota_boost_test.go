package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUsageUnrestrictedReportsBoostedDailyLimitAndRemaining(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dailyLimit := 10.0
	group := &service.Group{
		ID:               7,
		SubscriptionType: service.SubscriptionTypeSubscription,
		DailyLimitUSD:    &dailyLimit,
	}
	now := timezone.Now()
	subscription := &service.UserSubscription{
		Group:                  group,
		DailyUsageUSD:          15,
		QuotaBoostMonthlyLimit: 1,
		QuotaBoostActivatedAt:  &now,
	}

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/usage", nil)
	c.Set(string(middleware2.ContextKeySubscription), subscription)

	(&GatewayHandler{}).usageUnrestricted(
		c,
		context.Background(),
		&service.APIKey{Group: group},
		middleware2.AuthSubject{},
		nil,
		nil,
		nil,
	)

	require.Equal(t, http.StatusOK, recorder.Code)
	var body struct {
		Remaining    float64 `json:"remaining"`
		Subscription struct {
			DailyLimitUSD float64 `json:"daily_limit_usd"`
		} `json:"subscription"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.Equal(t, 5.0, body.Remaining)
	require.Equal(t, 20.0, body.Subscription.DailyLimitUSD)
}
