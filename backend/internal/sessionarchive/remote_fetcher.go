package sessionarchive

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type SafeRemoteFetcher struct {
	allowHTTP bool
	client    *http.Client
}

func NewSafeRemoteFetcher(allowHTTP bool) *SafeRemoteFetcher {
	f := &SafeRemoteFetcher{allowHTTP: allowHTTP}
	transport := &http.Transport{
		// Do not use environment proxies: a proxy could resolve the target to a
		// private address and bypass the dial-time SSRF check below.
		Proxy:                 nil,
		DialContext:           f.safeDialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
	}
	f.client = &http.Client{
		Transport: transport,
		Timeout:   60 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("too many redirects")
			}
			return f.validateURL(req.URL)
		},
	}
	return f
}

func (f *SafeRemoteFetcher) Fetch(ctx context.Context, rawURL string, maxBytes int64) ([]byte, string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrUnsafeRemoteURL, err)
	}
	if err := f.validateURL(u); err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("remote media returned HTTP %d", resp.StatusCode)
	}
	if maxBytes <= 0 {
		maxBytes = 64 << 20
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(data)) > maxBytes {
		return nil, "", ErrPartTooLarge
	}
	mediaType := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	if mediaType == "" {
		mediaType = http.DetectContentType(data)
	}
	return data, mediaType, nil
}

func (f *SafeRemoteFetcher) validateURL(u *url.URL) error {
	if u == nil || u.User != nil || strings.TrimSpace(u.Hostname()) == "" {
		return ErrUnsafeRemoteURL
	}
	if !strings.EqualFold(u.Scheme, "https") && !(f.allowHTTP && strings.EqualFold(u.Scheme, "http")) {
		return ErrUnsafeRemoteURL
	}
	ips, err := net.LookupIP(u.Hostname())
	if err != nil || len(ips) == 0 {
		return ErrUnsafeRemoteURL
	}
	for _, ip := range ips {
		if !isPublicIP(ip) {
			return ErrUnsafeRemoteURL
		}
	}
	return nil
}

func (f *SafeRemoteFetcher) safeDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if !isPublicIP(ip) {
			continue
		}
		return (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
	}
	return nil, ErrUnsafeRemoteURL
}

func isPublicIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		return !(v4[0] == 10 || v4[0] == 127 || (v4[0] == 169 && v4[1] == 254) || (v4[0] == 172 && v4[1] >= 16 && v4[1] <= 31) || (v4[0] == 192 && v4[1] == 168) || (v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127) || v4[0] == 0)
	}
	return !(ip.IsPrivate() || strings.HasPrefix(ip.String(), "fc") || strings.HasPrefix(ip.String(), "fd"))
}
