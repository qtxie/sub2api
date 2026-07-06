package service

import "testing"

func TestNormalizeOpenAISmartUserAgent(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "codex tui windows",
			in:   "codex-tui/0.142.5 (Windows 10.0.26100; x86_64) WindowsTerminal (codex-tui; 0.142.5)",
			want: "codex-tui/0.142.5 (Windows 10.0.26100; x86_64) WindowsTerminal (codex-tui; 0.142.5)",
		},
		{
			name: "codex tui linux",
			in:   "codex-tui/0.142.5 (Red Hat Enterprise Linux 8.8.0; x86_64) vscode/1.104.3 (codex-tui; 0.142.5)",
			want: "codex-tui/0.142.5 (Red Hat Enterprise Linux 8.8.0; x86_64) vscode/1.104.3 (codex-tui; 0.142.5)",
		},
		{
			name: "codex tui mac version normalized",
			in:   "codex-tui/0.142.4 (Mac OS 26.5.2; arm64) vscode/1.106.3 (codex-tui; 0.142.4)",
			want: "codex-tui/0.142.4 (Mac OS 26.5.2; arm64) vscode/1.106.3 (codex-tui; 0.142.4)",
		},
		{
			name: "codex desktop mac",
			in:   "Codex Desktop/0.142.4 (Mac OS 26.5.2; arm64) unknown (Codex Desktop; 26.616.71553)",
			want: "Codex Desktop/0.142.4 (Mac OS 26.5.2; arm64) unknown (Codex Desktop; 26.616.71553)",
		},
		{
			name: "other client forced to codex tui",
			in:   "curl/8.0",
			want: "codex-tui/0.142.5 (Windows 10.0.26100; x86_64) WindowsTerminal (codex-tui; 0.142.5)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeOpenAISmartUserAgent(tt.in); got != tt.want {
				t.Fatalf("normalizeOpenAISmartUserAgent() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveOpenAIUpstreamUserAgent(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Credentials: map[string]any{
			"user_agent": "manual-agent/1.0",
		},
	}

	if got := resolveOpenAIUpstreamUserAgent(account, "curl/8.0"); got != "manual-agent/1.0" {
		t.Fatalf("manual resolver = %q, want manual-agent/1.0", got)
	}

	account.Credentials["smart_user_agent_enabled"] = true
	if got := resolveOpenAIUpstreamUserAgent(account, "curl/8.0"); got != codexCLIUserAgent {
		t.Fatalf("smart resolver = %q, want %q", got, codexCLIUserAgent)
	}

	account.Credentials["smart_user_agent_enabled"] = false
	if got := resolveOpenAIUpstreamUserAgent(account, "curl/8.0"); got != "manual-agent/1.0" {
		t.Fatalf("disabled smart resolver = %q, want manual-agent/1.0", got)
	}
}
