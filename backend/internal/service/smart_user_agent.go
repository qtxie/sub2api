package service

import "strings"

// smartUserAgentSeparator splits a multi-slot account user-agent override.
// Each slot is a full Codex-form User-Agent; slot selection matches the
// client's original User-Agent against each slot's client identity and OS
// family (first match wins, last slot is the fallback).
const smartUserAgentSeparator = "|"

// smartUserAgentOS 是槽位/客户端 UA 的操作系统家族。unknown 充当通配：
// 任一侧解析不出 OS 时只按客户端名匹配，兼容无 OS 信息的旧配置与 UA。
type smartUserAgentOS int

const (
	smartUserAgentOSUnknown smartUserAgentOS = iota
	smartUserAgentOSWindows
	smartUserAgentOSMacOS
	smartUserAgentOSLinux
)

// smartUserAgentOSMarkers 在 UA 的 OS 段中按序做小写包含匹配。Linux 家族必须
// 列出常见发行版名：`Ubuntu 24.04`、`Red Hat Enterprise Linux` 之外的大多数
// 发行版字符串并不含 "linux" 字样。
var smartUserAgentOSMarkers = []struct {
	family  smartUserAgentOS
	markers []string
}{
	{smartUserAgentOSWindows, []string{"windows"}},
	{smartUserAgentOSMacOS, []string{"mac os", "macos", "macintosh", "darwin"}},
	{smartUserAgentOSLinux, []string{
		"linux", "ubuntu", "debian", "red hat", "rhel", "fedora", "centos",
		"arch", "alpine", "opensuse", "suse", "manjaro", "gentoo", "pop!_os",
	}},
}

// detectSmartUserAgentOS 解析 UA 的操作系统家族：优先取首个括号组（codex 形态
// `{client}/{ver} ({OS} {ver}; {arch}) ...` 的 OS 段，可避开终端名里的 OS 字样，
// 如 Linux 上的 WindowsTerminal），无括号时扫描整串。
func detectSmartUserAgentOS(ua string) smartUserAgentOS {
	v := strings.ToLower(strings.TrimSpace(ua))
	if v == "" {
		return smartUserAgentOSUnknown
	}
	seg := v
	if open := strings.IndexByte(v, '('); open >= 0 {
		rest := v[open+1:]
		if close := strings.IndexByte(rest, ')'); close >= 0 {
			seg = rest[:close]
		}
	}
	for _, m := range smartUserAgentOSMarkers {
		for _, marker := range m.markers {
			if strings.Contains(seg, marker) {
				return m.family
			}
		}
	}
	return smartUserAgentOSUnknown
}

// smartUserAgentSlotClientName 取槽位 UA 的客户端名：首个 '/' 之前的段
// （无 '/' 时取整串），小写化后作为该槽的身份标记。
func smartUserAgentSlotClientName(slot string) string {
	v := strings.ToLower(strings.TrimSpace(slot))
	if v == "" {
		return ""
	}
	if slash := strings.IndexByte(v, '/'); slash > 0 {
		v = strings.TrimSpace(v[:slash])
	}
	return v
}

// ResolveSmartUserAgent picks one User-Agent from a pipe-separated override list
// based on the client's original User-Agent.
//
// 每个槽位是一条完整 UA，携带两把匹配钥匙：客户端名（UA 首段）与 OS 家族
// （首个括号组）。客户端名匹配为大小写不敏感的子串匹配（兼容
// CODEX_INTERNAL_ORIGINATOR_OVERRIDE 等在 UA 尾部括号组携带真实 clientInfo.name 的形态）。
//
// 两级匹配，客户端名是高优先级的强身份，OS 只在同家族多槽间消歧：
//  1. 客户端名命中且 OS 家族一致（任一侧解析不出 OS 视为通配，兼容无 OS 信息的
//     旧配置）→ 首个这样的槽位生效。同一客户端家族可在不同 OS 的槽位间精确分流，
//     如 Ubuntu 的 codex-tui 客户端命中 `.. (Red Hat ..)` 槽而非 `.. (Windows ..)` 槽；
//  2. 客户端名命中但无任何 OS 精确匹配 → 取同家族的第一槽。Codex Desktop 客户端
//     即使跑在配置未覆盖的 OS 上（如配置只有 Mac 槽），也必须拿到 Codex Desktop
//     槽的 UA，而不是错拿其他家族的兜底槽；
//  3. 无任何客户端名命中 → 最后一槽（兜底槽）。
//
// Rules:
//   - no "|" / single segment → return that value unchanged (legacy behavior)
//   - first slot matching by client name AND OS family wins
//   - else first slot matching by client name alone wins
//   - else the last non-empty segment (fallback slot)
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
	clientOS := detectSmartUserAgentOS(clientUA)
	familyFallback := ""
	for _, slot := range parts {
		name := smartUserAgentSlotClientName(slot)
		if name == "" {
			continue
		}
		if !strings.Contains(client, name) {
			continue
		}
		if familyFallback == "" {
			familyFallback = slot
		}
		if slotOS := detectSmartUserAgentOS(slot); slotOS == smartUserAgentOSUnknown ||
			clientOS == smartUserAgentOSUnknown || slotOS == clientOS {
			return slot
		}
	}
	if familyFallback != "" {
		return familyFallback
	}
	return parts[len(parts)-1]
}
