package service

import (
	"context"
	"net/url"
	"strings"
)

const (
	// GatewayFailureReasonFirstOutputTimeout covers both the default first-output
	// deadline and the high-effort override (same error path, different duration).
	GatewayFailureReasonFirstOutputTimeout GatewayFailureReason = "first_output_timeout"
	// GatewayFailureReasonResponseHeaderTimeout marks OpenAI transport waits that
	// expire before any response headers arrive (openai_response_header_timeout).
	GatewayFailureReasonResponseHeaderTimeout GatewayFailureReason = "response_header_timeout"
)

type openAIExcludedUpstreamBaseURLsContextKey struct{}

// WithOpenAIExcludedUpstreamBaseURLs attaches request-scoped upstream base URLs
// that must not be selected during failover for the remainder of the request.
func WithOpenAIExcludedUpstreamBaseURLs(ctx context.Context, urls map[string]struct{}) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(urls) == 0 {
		return ctx
	}
	return context.WithValue(ctx, openAIExcludedUpstreamBaseURLsContextKey{}, urls)
}

func openAIExcludedUpstreamBaseURLsFromContext(ctx context.Context) map[string]struct{} {
	if ctx == nil {
		return nil
	}
	urls, _ := ctx.Value(openAIExcludedUpstreamBaseURLsContextKey{}).(map[string]struct{})
	return urls
}

// IsOpenAIPreOutputTimeout reports whether a failover was caused by a pre-output
// upstream timeout (first semantic output / high-effort first output / response headers).
func (e *UpstreamFailoverError) IsOpenAIPreOutputTimeout() bool {
	if e == nil {
		return false
	}
	switch e.Reason {
	case GatewayFailureReasonFirstOutputTimeout, GatewayFailureReasonResponseHeaderTimeout:
		return true
	default:
		return false
	}
}

// NormalizeAccountUpstreamBaseURLKey returns a comparable key for the account's
// effective upstream base URL used by scheduling failover exclusion.
func NormalizeAccountUpstreamBaseURLKey(account *Account) string {
	if account == nil {
		return ""
	}
	raw := accountUpstreamBaseURLForScheduling(account)
	return normalizeUpstreamBaseURLKey(raw)
}

func accountUpstreamBaseURLForScheduling(account *Account) string {
	if account == nil {
		return ""
	}
	if account.IsGrok() {
		return account.GetGrokBaseURL()
	}
	if account.IsOpenAI() || account.IsOpenAICompatible() {
		switch account.Type {
		case AccountTypeAPIKey:
			return account.GetOpenAIBaseURL()
		case AccountTypeOAuth:
			// OpenAI OAuth Responses traffic is pinned to ChatGPT internal API.
			if stored := strings.TrimSpace(account.GetCredential("base_url")); stored != "" {
				return stored
			}
			return "https://chatgpt.com"
		default:
			if stored := strings.TrimSpace(account.GetCredential("base_url")); stored != "" {
				return stored
			}
			return account.GetOpenAIBaseURL()
		}
	}
	return strings.TrimSpace(account.GetCredential("base_url"))
}

func normalizeUpstreamBaseURLKey(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		// Fall back to a coarse trim for non-URL / partial values.
		return strings.TrimRight(strings.ToLower(raw), "/")
	}
	host := strings.ToLower(parsed.Host)
	path := strings.TrimRight(parsed.EscapedPath(), "/")
	if path == "/" {
		path = ""
	}
	return strings.ToLower(parsed.Scheme) + "://" + host + path
}

// AddExcludedUpstreamBaseURL records the failed account's upstream base URL so
// peers that share it are skipped on subsequent selection for this request.
func AddExcludedUpstreamBaseURL(excluded map[string]struct{}, account *Account) {
	if excluded == nil || account == nil {
		return
	}
	if key := NormalizeAccountUpstreamBaseURLKey(account); key != "" {
		excluded[key] = struct{}{}
	}
}

// MarkOpenAIFailoverAccountExcluded always excludes the failed account ID, and
// when the failure is a pre-output timeout also excludes the same base URL.
func MarkOpenAIFailoverAccountExcluded(
	failedAccountIDs map[int64]struct{},
	excludedBaseURLs map[string]struct{},
	account *Account,
	failoverErr *UpstreamFailoverError,
) {
	if account == nil {
		return
	}
	if failedAccountIDs != nil {
		failedAccountIDs[account.ID] = struct{}{}
	}
	if failoverErr != nil && failoverErr.IsOpenAIPreOutputTimeout() {
		AddExcludedUpstreamBaseURL(excludedBaseURLs, account)
	}
}

func isAccountExcludedByUpstreamBaseURL(account *Account, excluded map[string]struct{}) bool {
	if account == nil || len(excluded) == 0 {
		return false
	}
	key := NormalizeAccountUpstreamBaseURLKey(account)
	if key == "" {
		return false
	}
	_, ok := excluded[key]
	return ok
}

func isOpenAIResponseHeaderTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "timeout awaiting response headers") ||
		strings.Contains(msg, "client.timeout exceeded while awaiting headers") ||
		strings.Contains(msg, "awaiting headers")
}
