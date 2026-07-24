//go:build unit

package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	testSmartUACodexTUI = "codex-tui/0.144.5 (Windows 10.0.26100; x86_64) WindowsTerminal (codex-tui; 0.144.5)"
	testSmartUADesktop  = "Codex Desktop/0.145.0 (Mac OS 26.5.2; arm64) unknown (Codex Desktop; 26.715.21425)"
	testSmartUAFallback = "codex-tui/0.144.5 (Red Hat Enterprise Linux 8.8.0; x86_64) vscode/1.104.3 (codex-tui; 0.144.5)"
)

func testSmartUAConfigured() string {
	return testSmartUACodexTUI + " | " + testSmartUADesktop + " | " + testSmartUAFallback
}

func TestResolveSmartUserAgent(t *testing.T) {
	configured := testSmartUAConfigured()

	t.Run("empty configured", func(t *testing.T) {
		require.Equal(t, "", ResolveSmartUserAgent("", "codex-tui/0.1.0"))
		require.Equal(t, "", ResolveSmartUserAgent("   ", "codex-tui/0.1.0"))
	})

	t.Run("legacy single value unchanged", func(t *testing.T) {
		require.Equal(t, "my-agent/1.0", ResolveSmartUserAgent("my-agent/1.0", "codex-tui/0.1.0"))
		require.Equal(t, "my-agent/1.0", ResolveSmartUserAgent("  my-agent/1.0  ", "curl/8.0"))
	})

	t.Run("three slots map by client", func(t *testing.T) {
		require.Equal(t, testSmartUACodexTUI, ResolveSmartUserAgent(configured, "codex-tui/0.144.5 (Linux; x86_64) xterm"))
		require.Equal(t, testSmartUADesktop, ResolveSmartUserAgent(configured, "Codex Desktop/0.145.0 (Mac OS 26.5.2; arm64)"))
		require.Equal(t, testSmartUAFallback, ResolveSmartUserAgent(configured, "curl/8.0"))
		require.Equal(t, testSmartUAFallback, ResolveSmartUserAgent(configured, "Mozilla/5.0 (Windows NT 10.0; Win64; x64)"))
		require.Equal(t, testSmartUAFallback, ResolveSmartUserAgent(configured, "codex_cli_rs/0.144.0"))
		require.Equal(t, testSmartUAFallback, ResolveSmartUserAgent(configured, ""))
	})

	t.Run("case insensitive match", func(t *testing.T) {
		require.Equal(t, testSmartUACodexTUI, ResolveSmartUserAgent(configured, "CODEX-TUI/0.1.0"))
		require.Equal(t, testSmartUADesktop, ResolveSmartUserAgent(configured, "codex desktop/0.1.0"))
	})

	t.Run("codex-tui has priority over desktop marker", func(t *testing.T) {
		// Unrealistic dual-marker UA; codex-tui wins by design.
		require.Equal(t, testSmartUACodexTUI, ResolveSmartUserAgent(configured, "codex-tui/1.0 Codex Desktop/1.0"))
	})

	t.Run("empty segments dropped", func(t *testing.T) {
		messy := " | " + testSmartUACodexTUI + " |  | " + testSmartUADesktop + " | " + testSmartUAFallback + " | "
		require.Equal(t, testSmartUACodexTUI, ResolveSmartUserAgent(messy, "codex-tui/0.1.0"))
		require.Equal(t, testSmartUADesktop, ResolveSmartUserAgent(messy, "Codex Desktop/0.1.0"))
		require.Equal(t, testSmartUAFallback, ResolveSmartUserAgent(messy, "curl/8.0"))
	})

	t.Run("two slots fallback uses last", func(t *testing.T) {
		two := testSmartUACodexTUI + " | " + testSmartUADesktop
		require.Equal(t, testSmartUACodexTUI, ResolveSmartUserAgent(two, "codex-tui/0.1.0"))
		require.Equal(t, testSmartUADesktop, ResolveSmartUserAgent(two, "Codex Desktop/0.1.0"))
		require.Equal(t, testSmartUADesktop, ResolveSmartUserAgent(two, "curl/8.0"))
		require.Equal(t, testSmartUADesktop, ResolveSmartUserAgent(two, ""))
	})

	t.Run("only pipes yields empty", func(t *testing.T) {
		require.Equal(t, "", ResolveSmartUserAgent(" | | ", "codex-tui/0.1.0"))
	})
}

func TestResolveOpenAIUserAgent(t *testing.T) {
	acc := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"user_agent": testSmartUAConfigured(),
		},
	}
	require.Equal(t, testSmartUACodexTUI, acc.ResolveOpenAIUserAgent("codex-tui/0.144.5"))
	require.Equal(t, testSmartUADesktop, acc.ResolveOpenAIUserAgent("Codex Desktop/0.145.0"))
	require.Equal(t, testSmartUAFallback, acc.ResolveOpenAIUserAgent("curl/8.0"))
	require.Equal(t, testSmartUAFallback, acc.ResolveOpenAIUserAgent(""))

	// Non-OpenAI accounts keep empty custom UA.
	require.Equal(t, "", (&Account{Platform: PlatformAnthropic}).ResolveOpenAIUserAgent("codex-tui/0.1.0"))
}

func TestApplyHeaderOverrides_SmartUserAgent(t *testing.T) {
	acc := headerOverrideTestAccount(PlatformOpenAI, AccountTypeAPIKey, map[string]any{
		credKeyHeaderOverrideEnabled: true,
		credKeyHeaderOverrides: map[string]any{
			"user-agent": testSmartUAConfigured(),
			"x-app":      "cli",
		},
	})

	t.Run("maps codex-tui client", func(t *testing.T) {
		h := make(http.Header)
		h.Set("User-Agent", "codex-tui/0.144.5 (Linux; x86_64) xterm (codex-tui; 0.144.5)")
		h.Set("x-app", "original")
		acc.ApplyHeaderOverrides(h)
		require.Equal(t, testSmartUACodexTUI, h.Get("User-Agent"))
		// x-app is written with non-canonical wire casing; assert via map key.
		require.Equal(t, []string{"cli"}, h["x-app"])
	})

	t.Run("maps Codex Desktop client", func(t *testing.T) {
		h := make(http.Header)
		h.Set("User-Agent", "Codex Desktop/0.145.0 (Mac OS 26.5.2; arm64)")
		acc.ApplyHeaderOverrides(h)
		require.Equal(t, testSmartUADesktop, h.Get("User-Agent"))
	})

	t.Run("maps other client to fallback", func(t *testing.T) {
		h := make(http.Header)
		h.Set("User-Agent", "curl/8.0")
		acc.ApplyHeaderOverrides(h)
		require.Equal(t, testSmartUAFallback, h.Get("User-Agent"))
	})

	t.Run("empty client uses fallback", func(t *testing.T) {
		h := make(http.Header)
		acc.ApplyHeaderOverrides(h)
		require.Equal(t, testSmartUAFallback, h.Get("User-Agent"))
	})
}
