package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type openAIFirstOutputProxyRepoStub struct {
	ProxyRepository
	proxies []Proxy
}

func (r openAIFirstOutputProxyRepoStub) ListActive(context.Context) ([]Proxy, error) {
	return append([]Proxy(nil), r.proxies...), nil
}

type openAIFirstOutputProxyLatencyStub struct {
	ProxyLatencyCache
	latencies map[int64]*ProxyLatencyInfo
}

func (c openAIFirstOutputProxyLatencyStub) GetProxyLatencies(context.Context, []int64) (map[int64]*ProxyLatencyInfo, error) {
	return c.latencies, nil
}

func TestSelectOpenAIFirstOutputRetryRoutePrefersUnusedHealthyProxy(t *testing.T) {
	fast := int64(10)
	medium := int64(20)
	bad := int64(1)
	expired := time.Now().Add(-time.Minute)
	svc := &OpenAIGatewayService{
		proxyRepo: openAIFirstOutputProxyRepoStub{proxies: []Proxy{
			{ID: 1, Name: "failed-fast", Protocol: "http", Host: "127.0.0.1", Port: 9001, Status: StatusActive},
			{ID: 2, Name: "unused", Protocol: "http", Host: "127.0.0.1", Port: 9002, Status: StatusActive},
			{ID: 3, Name: "used-fast", Protocol: "http", Host: "127.0.0.1", Port: 9003, Status: StatusActive},
			{ID: 4, Name: "expired", Protocol: "http", Host: "127.0.0.1", Port: 9004, Status: StatusActive, ExpiresAt: &expired},
		}},
		proxyLatencyCache: openAIFirstOutputProxyLatencyStub{latencies: map[int64]*ProxyLatencyInfo{
			1: {Success: false, LatencyMs: &bad},
			2: {Success: true, LatencyMs: &medium, QualityStatus: "healthy"},
			3: {Success: true, LatencyMs: &fast, QualityStatus: "healthy"},
		}},
	}

	route, err := svc.SelectOpenAIFirstOutputRetryRoute(context.Background(), nil, 2, map[int64]struct{}{3: {}})
	require.NoError(t, err)
	require.Equal(t, int64(2), route.ProxyID)
	require.Equal(t, "http://127.0.0.1:9002", route.ProxyURL)
	require.True(t, route.FreshTransport)
	require.Equal(t, 3, route.Attempt)
}
