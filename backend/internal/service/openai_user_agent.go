package service

import "strings"

const (
	openAISmartUserAgentWindowsOS = "(Windows 10.0.26100; x86_64)"
	openAISmartUserAgentMacOS     = "(Mac OS 26.5.2; arm64)"
	openAISmartUserAgentLinuxOS   = "(Red Hat Enterprise Linux 8.8.0; x86_64)"
	openAISmartUserAgentVSCode    = "vscode/1.104.3"
	openAICodexDesktopBuild       = "26.623.101652"
)

func resolveOpenAIUpstreamUserAgent(account *Account, clientUserAgent string) string {
	if account == nil || !account.IsOpenAI() {
		return ""
	}
	if account.IsOpenAISmartUserAgentEnabled() {
		return normalizeOpenAISmartUserAgent(clientUserAgent)
	}
	return strings.TrimSpace(account.GetOpenAIUserAgent())
}

func normalizeOpenAISmartUserAgent(clientUserAgent string) string {
	ua := strings.TrimSpace(clientUserAgent)
	if strings.HasPrefix(ua, "Codex Desktop/") {
		version := extractOpenAISmartUserAgentVersion(ua, "Codex Desktop/")
		if version == "" {
			version = codexCLIVersion
		}
		buildSuffix := extractOpenAISmartCodexDesktopBuildSuffix(ua)
		if buildSuffix == "" {
			buildSuffix = openAICodexDesktopBuild
		}
		return "Codex Desktop/" + version + " " + detectOpenAISmartUserAgentOS(ua) +
			" unknown (Codex Desktop; " + buildSuffix + ")"
	}
	if strings.HasPrefix(ua, "codex-tui/") {
		return buildOpenAISmartCodexTUIUserAgent(ua, detectOpenAISmartUserAgentOS(ua))
	}
	return codexCLIUserAgent
}

func buildOpenAISmartCodexTUIUserAgent(userAgent string, osSegment string) string {
	version := extractOpenAISmartUserAgentVersion(userAgent, "codex-tui/")
	if version == "" {
		version = codexCLIVersion
	}
	terminal := extractOpenAISmartCodexTUITerminal(userAgent)
	if terminal == "" {
		terminal = "WindowsTerminal"
		if osSegment != openAISmartUserAgentWindowsOS {
			terminal = openAISmartUserAgentVSCode
		}
	}
	return "codex-tui/" + version + " " + osSegment + " " + terminal +
		" (codex-tui; " + version + ")"
}

func extractOpenAISmartUserAgentVersion(userAgent string, prefix string) string {
	ua := strings.TrimSpace(userAgent)
	if !strings.HasPrefix(ua, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(ua, prefix)
	end := strings.IndexAny(rest, " \t")
	if end < 0 {
		return strings.TrimSpace(rest)
	}
	return strings.TrimSpace(rest[:end])
}

func extractOpenAISmartCodexTUITerminal(userAgent string) string {
	afterOS := strings.Index(userAgent, ") ")
	if afterOS < 0 {
		return ""
	}
	rest := strings.TrimSpace(userAgent[afterOS+2:])
	if rest == "" {
		return ""
	}
	suffix := strings.LastIndex(rest, " (codex-tui;")
	if suffix < 0 {
		return rest
	}
	return strings.TrimSpace(rest[:suffix])
}

func extractOpenAISmartCodexDesktopBuildSuffix(userAgent string) string {
	const marker = "(Codex Desktop;"
	start := strings.LastIndex(userAgent, marker)
	if start < 0 {
		return ""
	}
	rest := strings.TrimSpace(userAgent[start+len(marker):])
	end := strings.Index(rest, ")")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:end])
}

func detectOpenAISmartUserAgentOS(userAgent string) string {
	lower := strings.ToLower(userAgent)
	switch {
	case strings.Contains(lower, "windows"):
		return openAISmartUserAgentWindowsOS
	case strings.Contains(lower, "mac os") ||
		strings.Contains(lower, "macos") ||
		strings.Contains(lower, "darwin") ||
		strings.Contains(lower, "macintosh"):
		return openAISmartUserAgentMacOS
	case strings.Contains(lower, "linux") ||
		strings.Contains(lower, "ubuntu") ||
		strings.Contains(lower, "debian") ||
		strings.Contains(lower, "red hat") ||
		strings.Contains(lower, "rhel") ||
		strings.Contains(lower, "fedora") ||
		strings.Contains(lower, "centos"):
		return openAISmartUserAgentLinuxOS
	default:
		return openAISmartUserAgentWindowsOS
	}
}
