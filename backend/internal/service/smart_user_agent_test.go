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

	t.Run("three slots map by client and OS", func(t *testing.T) {
		// 客户端名与 OS 家族都命中才选槽：Windows 的 codex-tui → 第一槽。
		require.Equal(t, testSmartUACodexTUI, ResolveSmartUserAgent(configured, "codex-tui/0.144.5 (Windows 10.0.26100; x86_64) WindowsTerminal (codex-tui; 0.144.5)"))
		// Linux 家族的 codex-tui（Ubuntu）跳过 Windows 槽，命中 RHEL 第三槽。
		require.Equal(t, testSmartUAFallback, ResolveSmartUserAgent(configured, "codex-tui/0.144.5 (Linux; x86_64) xterm"))
		require.Equal(t, testSmartUAFallback, ResolveSmartUserAgent(configured, "codex-tui/0.148.0 (Ubuntu 24.04) terminal (codex-tui; 0.148.0)"))
		// 配置中没有 Mac 的 codex-tui 槽：客户端名优先，退回同家族第一槽而非兜底槽。
		require.Equal(t, testSmartUACodexTUI, ResolveSmartUserAgent(configured, "codex-tui/0.144.5 (Mac OS X 15.1.0; arm64) iTerm.app (codex-tui; 0.144.5)"))
		require.Equal(t, testSmartUADesktop, ResolveSmartUserAgent(configured, "Codex Desktop/0.145.0 (Mac OS 26.5.2; arm64)"))
		require.Equal(t, testSmartUAFallback, ResolveSmartUserAgent(configured, "curl/8.0"))
		require.Equal(t, testSmartUAFallback, ResolveSmartUserAgent(configured, "Mozilla/5.0 (Windows NT 10.0; Win64; x64)"))
		require.Equal(t, testSmartUAFallback, ResolveSmartUserAgent(configured, "codex_cli_rs/0.144.0"))
		require.Equal(t, testSmartUAFallback, ResolveSmartUserAgent(configured, ""))
	})

	t.Run("client family has priority over OS and fallback", func(t *testing.T) {
		// 用户实例：Ubuntu 客户端必须取最后一槽（Linux 家族），而不是第一槽（Windows）。
		windows := "codex-tui/0.148.0 (Windows 10.0.26100; x86_64) WindowsTerminal (codex-tui; 0.148.0)"
		desktop := "Codex Desktop/0.148.0-alpha.9 (Mac OS 26.5.2; arm64) unknown (Codex Desktop; 26.810.52044)"
		rhel := "codex-tui/0.148.0 (Red Hat Enterprise Linux 8.8.0; x86_64) vscode/1.104.3 (codex-tui; 0.148.0)"
		configured := windows + " | " + desktop + " | " + rhel
		require.Equal(t, rhel, ResolveSmartUserAgent(configured, "codex-tui/0.148.0 (Ubuntu 24.04) terminal (codex-tui; 0.148.0)"))
		require.Equal(t, windows, ResolveSmartUserAgent(configured, windows))
		require.Equal(t, desktop, ResolveSmartUserAgent(configured, "Codex Desktop/0.148.0 (Mac OS 26.5.2; arm64) unknown (Codex Desktop; 26.810.52044)"))
		// 配置只有 Mac 的 Codex Desktop 槽：Windows 的 Codex Desktop 客户端仍取
		// 家族命中的第二槽，而不是错拿其他家族的兜底槽。
		require.Equal(t, desktop, ResolveSmartUserAgent(configured, "Codex Desktop/0.148.0-alpha.9 (Windows; arm64) unknown (Codex Desktop; 26.810.52044)"))
	})

	t.Run("slot without OS info matches any client OS (legacy wildcard)", func(t *testing.T) {
		noOS := "codex-tui/0.1.0 | other-agent/1.0"
		require.Equal(t, "codex-tui/0.1.0", ResolveSmartUserAgent(noOS, "codex-tui/0.1.0 (Mac OS X 15.1.0; arm64)"))
		// 客户端 UA 无 OS 段时同样通配：按客户端名命中第一槽（见 case insensitive match）。
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

	t.Run("maps codex-tui client by OS", func(t *testing.T) {
		h := make(http.Header)
		h.Set("User-Agent", "codex-tui/0.144.5 (Windows 10.0.26100; x86_64) WindowsTerminal (codex-tui; 0.144.5)")
		h.Set("x-app", "original")
		acc.ApplyHeaderOverrides(h)
		require.Equal(t, testSmartUACodexTUI, h.Get("User-Agent"))
		// x-app is written with non-canonical wire casing; assert via map key.
		require.Equal(t, []string{"cli"}, h["x-app"])

		// Linux 家族客户端命中 RHEL 兜底槽，而不是 Windows 第一槽。
		h = make(http.Header)
		h.Set("User-Agent", "codex-tui/0.144.5 (Linux; x86_64) xterm (codex-tui; 0.144.5)")
		acc.ApplyHeaderOverrides(h)
		require.Equal(t, testSmartUAFallback, h.Get("User-Agent"))
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
