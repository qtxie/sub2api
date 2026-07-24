package service

import "strings"

// smartUserAgentSeparator splits a multi-slot account user-agent override.
// Format (order fixed):
//
//	codex-tui UA | Codex Desktop UA | fallback UA
//
// Matching uses the client's original User-Agent only (case-insensitive contains).
const smartUserAgentSeparator = "|"

const (
	smartUserAgentMarkerCodexTUI     = "codex-tui"
	smartUserAgentMarkerCodexDesktop = "codex desktop"
)

// ResolveSmartUserAgent picks one User-Agent from a pipe-separated override list
// based on the client's original User-Agent.
//
// Rules:
//   - no "|" / single segment → return that value unchanged (legacy behavior)
//   - client UA contains "codex-tui" (case-insensitive) → 1st segment
//   - else client UA contains "Codex Desktop" (case-insensitive) → 2nd segment when present
//   - else → 3rd segment when present, otherwise the last segment
//
// Empty segments around "|" are dropped. Empty configured value returns "".
func ResolveSmartUserAgent(configured, clientUA string) string {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return ""
	}
	if !strings.Contains(configured, smartUserAgentSeparator) {
		return configured
	}

	rawParts := strings.Split(configured, smartUserAgentSeparator)
	parts := make([]string, 0, len(rawParts))
	for _, part := range rawParts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}

	client := strings.ToLower(strings.TrimSpace(clientUA))
	if strings.Contains(client, smartUserAgentMarkerCodexTUI) {
		return parts[0]
	}
	if strings.Contains(client, smartUserAgentMarkerCodexDesktop) {
		if len(parts) >= 2 {
			return parts[1]
		}
		return parts[len(parts)-1]
	}
	if len(parts) >= 3 {
		return parts[2]
	}
	return parts[len(parts)-1]
}
