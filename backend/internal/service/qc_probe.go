package service

import (
	"context"
	"log/slog"
	"net/url"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

const (
	// QCProbeFallbackNormal keeps normal scheduling when the QC pool is empty/unavailable.
	QCProbeFallbackNormal = "normal"
	// QCProbeFallbackReject returns no-available-accounts when the QC pool cannot serve the request.
	QCProbeFallbackReject = "reject"

	// QCProbePoolScopeGroup intersects the QC pool with the request group's schedulable accounts.
	QCProbePoolScopeGroup = "group"
	// QCProbePoolScopeGlobal loads the QC pool by account ID, ignoring group membership and the
	// manual schedulable flag (still requires active accounts).
	QCProbePoolScopeGlobal = "global"

	QCProbeSourceZtest      = "ztest"
	QCProbeSourceTokensQC   = "tokensqc"
	QCProbeSourceHvoy       = "hvoy"
	QCProbeSourceAPIRanking = "apiranking"
	QCProbeSourceLuguang    = "luguang"
	QCProbeSourceUserAgent  = "user_agent"
)

// QCProbeSourceConfig controls detection for one QC site.
type QCProbeSourceConfig struct {
	Enabled    bool     `json:"enabled"`
	Origins    []string `json:"origins"`
	UserAgents []string `json:"user_agents,omitempty"`
}

// QCProbeRoutingSettings is the admin-configurable QC probe routing policy.
type QCProbeRoutingSettings struct {
	Enabled             bool                           `json:"enabled"`
	Fallback            string                         `json:"fallback"` // normal | reject
	PoolScope           string                         `json:"pool_scope"` // group | global
	AccountIDs          []int64                        `json:"account_ids"`
	Sources             map[string]QCProbeSourceConfig `json:"sources"`
	UserAgentSubstrings []string                       `json:"user_agent_substrings"`
	ApplyPlatforms      []string                       `json:"apply_platforms"`
}

// QCProbeSelection is the per-request decision snapshot stored in context.
type QCProbeSelection struct {
	Active         bool
	Source         string
	Fallback       string
	PoolScope      string
	AccountIDs     []int64
	AccountIDSet   map[int64]struct{}
	ApplyPlatforms map[string]struct{}
}

// DefaultQCProbeRoutingSettings returns MVP defaults (feature off).
func DefaultQCProbeRoutingSettings() *QCProbeRoutingSettings {
	return &QCProbeRoutingSettings{
		Enabled:    false,
		Fallback:   QCProbeFallbackNormal,
		PoolScope:  QCProbePoolScopeGroup,
		AccountIDs: nil,
		Sources: map[string]QCProbeSourceConfig{
			QCProbeSourceZtest: {
				Enabled: true,
				Origins: []string{"https://ztest.ai", "https://www.ztest.ai"},
			},
			QCProbeSourceTokensQC: {
				Enabled: true,
				Origins: []string{"https://tokensqc.com", "https://www.tokensqc.com"},
			},
			QCProbeSourceHvoy: {
				Enabled: true,
				Origins: []string{"https://hvoy.ai", "https://www.hvoy.ai"},
			},
			QCProbeSourceAPIRanking: {
				Enabled: true,
				Origins: []string{"https://apiranking.com", "https://www.apiranking.com"},
			},
			QCProbeSourceLuguang: {
				Enabled: false,
				Origins: nil,
			},
		},
		UserAgentSubstrings: nil,
		ApplyPlatforms: []string{
			PlatformAnthropic,
			PlatformOpenAI,
			PlatformGemini,
			PlatformAntigravity,
			PlatformGrok,
		},
	}
}

// NormalizeQCProbeRoutingSettings cleans and validates settings for storage/use.
func NormalizeQCProbeRoutingSettings(settings *QCProbeRoutingSettings) *QCProbeRoutingSettings {
	out := DefaultQCProbeRoutingSettings()
	if settings == nil {
		return out
	}
	out.Enabled = settings.Enabled
	out.Fallback = normalizeQCProbeFallback(settings.Fallback)
	out.PoolScope = normalizeQCProbePoolScope(settings.PoolScope)

	seenIDs := make(map[int64]struct{}, len(settings.AccountIDs))
	out.AccountIDs = make([]int64, 0, len(settings.AccountIDs))
	for _, id := range settings.AccountIDs {
		if id <= 0 {
			continue
		}
		if _, ok := seenIDs[id]; ok {
			continue
		}
		seenIDs[id] = struct{}{}
		out.AccountIDs = append(out.AccountIDs, id)
	}

	// Merge operator-provided sources onto defaults so partial updates cannot wipe
	// built-in QC site definitions.
	if settings.Sources != nil {
		merged := make(map[string]QCProbeSourceConfig, len(out.Sources)+len(settings.Sources))
		for key, src := range out.Sources {
			merged[key] = src
		}
		for key, src := range settings.Sources {
			name := strings.ToLower(strings.TrimSpace(key))
			if name == "" {
				continue
			}
			merged[name] = QCProbeSourceConfig{
				Enabled:    src.Enabled,
				Origins:    normalizeQCProbeStringList(src.Origins, 32),
				UserAgents: normalizeQCProbeStringList(src.UserAgents, 32),
			}
		}
		out.Sources = merged
	}

	out.UserAgentSubstrings = normalizeQCProbeStringList(settings.UserAgentSubstrings, 64)
	out.ApplyPlatforms = normalizeQCProbePlatforms(settings.ApplyPlatforms)
	return out
}

func normalizeQCProbeFallback(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case QCProbeFallbackReject:
		return QCProbeFallbackReject
	default:
		return QCProbeFallbackNormal
	}
}

func normalizeQCProbePoolScope(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case QCProbePoolScopeGlobal:
		return QCProbePoolScopeGlobal
	default:
		return QCProbePoolScopeGroup
	}
}

func normalizeQCProbeStringList(values []string, max int) []string {
	if max <= 0 {
		max = 32
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		key := strings.ToLower(v)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, v)
		if len(out) >= max {
			break
		}
	}
	return out
}

func normalizeQCProbePlatforms(values []string) []string {
	if len(values) == 0 {
		return append([]string(nil), DefaultQCProbeRoutingSettings().ApplyPlatforms...)
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		p := strings.ToLower(strings.TrimSpace(raw))
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	if len(out) == 0 {
		return append([]string(nil), DefaultQCProbeRoutingSettings().ApplyPlatforms...)
	}
	return out
}

// DetectQCProbeRequest decides whether a request is from a known QC site.
func DetectQCProbeRequest(origin, referer, userAgent string, settings *QCProbeRoutingSettings) QCProbeSelection {
	settings = NormalizeQCProbeRoutingSettings(settings)
	selection := QCProbeSelection{
		Active:         false,
		Fallback:       settings.Fallback,
		PoolScope:      settings.PoolScope,
		AccountIDs:     append([]int64(nil), settings.AccountIDs...),
		AccountIDSet:   make(map[int64]struct{}, len(settings.AccountIDs)),
		ApplyPlatforms: make(map[string]struct{}, len(settings.ApplyPlatforms)),
	}
	for _, id := range settings.AccountIDs {
		selection.AccountIDSet[id] = struct{}{}
	}
	for _, p := range settings.ApplyPlatforms {
		selection.ApplyPlatforms[p] = struct{}{}
	}
	if !settings.Enabled {
		return selection
	}

	originHost := extractQCProbeHost(origin)
	refererHost := extractQCProbeHost(referer)
	ua := strings.TrimSpace(userAgent)

	// Prefer source-specific Origin/Referer/UA matches.
	sourceOrder := []string{
		QCProbeSourceZtest,
		QCProbeSourceTokensQC,
		QCProbeSourceHvoy,
		QCProbeSourceAPIRanking,
		QCProbeSourceLuguang,
	}
	for _, source := range sourceOrder {
		src, ok := settings.Sources[source]
		if !ok || !src.Enabled {
			continue
		}
		if hostMatchesQCProbeOrigins(originHost, src.Origins) || hostMatchesQCProbeOrigins(refererHost, src.Origins) {
			selection.Active = true
			selection.Source = source
			return selection
		}
		if uaMatchesQCProbeSubstrings(ua, src.UserAgents) {
			selection.Active = true
			selection.Source = source
			return selection
		}
	}

	// Global UA fallback (operator-defined).
	if uaMatchesQCProbeSubstrings(ua, settings.UserAgentSubstrings) {
		selection.Active = true
		selection.Source = QCProbeSourceUserAgent
		return selection
	}

	// Also scan any extra custom sources not in the default order.
	for source, src := range settings.Sources {
		if !src.Enabled {
			continue
		}
		switch source {
		case QCProbeSourceZtest, QCProbeSourceTokensQC, QCProbeSourceHvoy, QCProbeSourceAPIRanking, QCProbeSourceLuguang:
			continue
		}
		if hostMatchesQCProbeOrigins(originHost, src.Origins) || hostMatchesQCProbeOrigins(refererHost, src.Origins) ||
			uaMatchesQCProbeSubstrings(ua, src.UserAgents) {
			selection.Active = true
			selection.Source = source
			return selection
		}
	}

	return selection
}

func extractQCProbeHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// Origin is usually scheme://host[:port]; Referer is a full URL.
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return ""
		}
		return strings.ToLower(strings.TrimSpace(u.Hostname()))
	}
	// Bare host or host:port
	if strings.Contains(raw, "/") {
		u, err := url.Parse("https://" + raw)
		if err != nil {
			return ""
		}
		return strings.ToLower(strings.TrimSpace(u.Hostname()))
	}
	host := raw
	if i := strings.Index(host, ":"); i >= 0 {
		host = host[:i]
	}
	return strings.ToLower(strings.TrimSpace(host))
}

func hostMatchesQCProbeOrigins(host string, origins []string) bool {
	if host == "" {
		return false
	}
	for _, origin := range origins {
		if extractQCProbeHost(origin) == host {
			return true
		}
	}
	return false
}

func uaMatchesQCProbeSubstrings(ua string, substrings []string) bool {
	if ua == "" || len(substrings) == 0 {
		return false
	}
	lower := strings.ToLower(ua)
	for _, sub := range substrings {
		sub = strings.ToLower(strings.TrimSpace(sub))
		if sub == "" {
			continue
		}
		if strings.Contains(lower, sub) {
			return true
		}
	}
	return false
}

// WithQCProbeSelection stores the QC decision on the request context.
func WithQCProbeSelection(ctx context.Context, selection QCProbeSelection) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = context.WithValue(ctx, ctxkey.IsQCProbeClient, selection.Active)
	ctx = context.WithValue(ctx, ctxkey.QCProbeSource, selection.Source)
	ctx = context.WithValue(ctx, ctxkey.QCProbeSelection, selection)
	return ctx
}

// QCProbeSelectionFromContext returns the QC decision snapshot.
func QCProbeSelectionFromContext(ctx context.Context) (QCProbeSelection, bool) {
	if ctx == nil {
		return QCProbeSelection{}, false
	}
	selection, ok := ctx.Value(ctxkey.QCProbeSelection).(QCProbeSelection)
	return selection, ok
}

// IsQCProbeClient reports whether the request was classified as QC probe traffic.
func IsQCProbeClient(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	if v, ok := ctx.Value(ctxkey.IsQCProbeClient).(bool); ok {
		return v
	}
	return false
}

// QCProbeSourceFromContext returns the matched QC source id.
func QCProbeSourceFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(ctxkey.QCProbeSource).(string); ok {
		return v
	}
	return ""
}

func (s QCProbeSelection) appliesToPlatform(platform string) bool {
	if !s.Active {
		return false
	}
	platform = strings.ToLower(strings.TrimSpace(platform))
	if platform == "" {
		return true
	}
	if len(s.ApplyPlatforms) == 0 {
		return true
	}
	_, ok := s.ApplyPlatforms[platform]
	return ok
}

func (s QCProbeSelection) hasAccountPool() bool {
	return len(s.AccountIDSet) > 0
}

func (s QCProbeSelection) isGlobalScope() bool {
	return normalizeQCProbePoolScope(s.PoolScope) == QCProbePoolScopeGlobal
}

func (s QCProbeSelection) allowsAccountID(id int64) bool {
	if !s.Active || !s.hasAccountPool() {
		return true
	}
	_, ok := s.AccountIDSet[id]
	return ok
}

// QCProbeAccountLoader loads accounts by ID for global QC pool scope.
type QCProbeAccountLoader func(ctx context.Context, ids []int64) ([]Account, error)

// ResolveAccountsForQCProbe applies QC pool policy to a candidate list.
//   - group scope: intersect with the provided group-schedulable candidates
//   - global scope: load pool accounts by ID (ignore group + manual unschedulable)
// Returns (accounts, reject).
func ResolveAccountsForQCProbe(ctx context.Context, platform string, accounts []Account, loadPool QCProbeAccountLoader) ([]Account, bool) {
	selection, ok := QCProbeSelectionFromContext(ctx)
	if !ok || !selection.Active || !selection.appliesToPlatform(platform) {
		return accounts, false
	}
	if selection.isGlobalScope() {
		return resolveGlobalQCProbeAccounts(ctx, platform, accounts, selection, loadPool)
	}
	return filterGroupQCProbeAccounts(platform, accounts, selection)
}

// FilterAccountsForQCProbe is the group-scope filter used by tests and callers that already
// hold the candidate list. Prefer ResolveAccountsForQCProbe when global scope is supported.
func FilterAccountsForQCProbe(ctx context.Context, platform string, accounts []Account) ([]Account, bool) {
	return ResolveAccountsForQCProbe(ctx, platform, accounts, nil)
}

func filterGroupQCProbeAccounts(platform string, accounts []Account, selection QCProbeSelection) ([]Account, bool) {
	if !selection.hasAccountPool() {
		if selection.Fallback == QCProbeFallbackReject {
			slog.Info("qc_probe.routing_reject_empty_pool",
				"source", selection.Source,
				"platform", platform,
				"pool_scope", selection.PoolScope,
				"fallback", selection.Fallback)
			return nil, true
		}
		return accounts, false
	}

	filtered := make([]Account, 0, len(accounts))
	for i := range accounts {
		if selection.allowsAccountID(accounts[i].ID) {
			filtered = append(filtered, accounts[i])
		}
	}
	if len(filtered) > 0 {
		slog.Info("qc_probe.routing_applied",
			"source", selection.Source,
			"platform", platform,
			"pool_scope", selection.PoolScope,
			"pool_size", len(selection.AccountIDSet),
			"matched", len(filtered),
			"total", len(accounts))
		return filtered, false
	}
	if selection.Fallback == QCProbeFallbackReject {
		slog.Info("qc_probe.routing_reject_no_eligible",
			"source", selection.Source,
			"platform", platform,
			"pool_scope", selection.PoolScope,
			"pool_size", len(selection.AccountIDSet),
			"total", len(accounts))
		return nil, true
	}
	slog.Info("qc_probe.routing_fallback_normal",
		"source", selection.Source,
		"platform", platform,
		"pool_scope", selection.PoolScope,
		"pool_size", len(selection.AccountIDSet),
		"total", len(accounts))
	return accounts, false
}

func resolveGlobalQCProbeAccounts(
	ctx context.Context,
	platform string,
	accounts []Account,
	selection QCProbeSelection,
	loadPool QCProbeAccountLoader,
) ([]Account, bool) {
	if !selection.hasAccountPool() {
		if selection.Fallback == QCProbeFallbackReject {
			slog.Info("qc_probe.routing_reject_empty_pool",
				"source", selection.Source,
				"platform", platform,
				"pool_scope", selection.PoolScope,
				"fallback", selection.Fallback)
			return nil, true
		}
		return accounts, false
	}
	if loadPool == nil {
		// No loader: fall back to group-scope intersection so callers without repo access
		// still behave safely instead of inventing accounts.
		slog.Warn("qc_probe.global_scope_missing_loader",
			"source", selection.Source,
			"platform", platform)
		return filterGroupQCProbeAccounts(platform, accounts, selection)
	}

	loaded, err := loadPool(ctx, append([]int64(nil), selection.AccountIDs...))
	if err != nil {
		slog.Warn("qc_probe.global_pool_load_failed",
			"source", selection.Source,
			"platform", platform,
			"error", err)
		if selection.Fallback == QCProbeFallbackReject {
			return nil, true
		}
		return accounts, false
	}

	filtered := make([]Account, 0, len(loaded))
	for i := range loaded {
		acc := prepareGlobalQCProbeAccount(loaded[i], platform)
		if acc == nil {
			continue
		}
		filtered = append(filtered, *acc)
	}
	if len(filtered) > 0 {
		slog.Info("qc_probe.routing_applied",
			"source", selection.Source,
			"platform", platform,
			"pool_scope", selection.PoolScope,
			"pool_size", len(selection.AccountIDSet),
			"matched", len(filtered),
			"total", len(accounts))
		return filtered, false
	}
	if selection.Fallback == QCProbeFallbackReject {
		slog.Info("qc_probe.routing_reject_no_eligible",
			"source", selection.Source,
			"platform", platform,
			"pool_scope", selection.PoolScope,
			"pool_size", len(selection.AccountIDSet),
			"total", len(accounts))
		return nil, true
	}
	slog.Info("qc_probe.routing_fallback_normal",
		"source", selection.Source,
		"platform", platform,
		"pool_scope", selection.PoolScope,
		"pool_size", len(selection.AccountIDSet),
		"total", len(accounts))
	return accounts, false
}

// prepareGlobalQCProbeAccount returns a usable pool account for QC global scope.
// Ignores manual Schedulable=false and group membership; still requires active status
// and platform compatibility. Clears only the manual schedulable gate so temporary
// rate-limit / overload cooldowns remain effective via IsSchedulable().
// Accounts that remain unschedulable after the manual-gate override are excluded so
// fallback=normal can still fall through when the burn pool cannot actually serve.
func prepareGlobalQCProbeAccount(acc Account, platform string) *Account {
	if !qcProbeAccountMatchesPlatform(acc, platform) {
		return nil
	}
	acc.Schedulable = true
	if !acc.IsSchedulable() {
		return nil
	}
	return &acc
}

func qcProbeAccountMatchesPlatform(acc Account, platform string) bool {
	platform = strings.ToLower(strings.TrimSpace(platform))
	if platform == "" {
		return true
	}
	ap := strings.ToLower(strings.TrimSpace(acc.Platform))
	if ap == platform {
		return true
	}
	// Mixed Anthropic/Gemini scheduling may include antigravity accounts.
	if (platform == PlatformAnthropic || platform == PlatformGemini) && ap == PlatformAntigravity {
		return true
	}
	// OpenAI-compatible scheduling treats openai platform accounts only here;
	// grok stays on its own platform string.
	return false
}

// RestrictAccountIDsForQCProbe intersects routing/sticky candidate IDs with the QC pool.
// Fallback semantics:
//   - no shortlist: force QC pool
//   - empty intersection + group/normal: keep original ids
//   - empty intersection + global: force QC pool (pin burn list)
//   - empty intersection + reject: return empty shortlist
func RestrictAccountIDsForQCProbe(ctx context.Context, platform string, ids []int64) []int64 {
	selection, ok := QCProbeSelectionFromContext(ctx)
	if !ok || !selection.Active || !selection.appliesToPlatform(platform) || !selection.hasAccountPool() {
		return ids
	}
	forcePool := func() []int64 {
		out := make([]int64, 0, len(selection.AccountIDs))
		out = append(out, selection.AccountIDs...)
		return out
	}
	if len(ids) == 0 {
		return forcePool()
	}
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if selection.allowsAccountID(id) {
			out = append(out, id)
		}
	}
	if len(out) > 0 {
		return out
	}
	if selection.Fallback == QCProbeFallbackReject {
		return out
	}
	if selection.isGlobalScope() {
		// Global scope always pins to the burn pool rather than unrestricted routing.
		return forcePool()
	}
	// group + fallback normal: preserve original shortlist
	return ids
}

// IsAccountAllowedByQCProbe reports whether sticky/direct account id may be used.
func IsAccountAllowedByQCProbe(ctx context.Context, platform string, accountID int64) bool {
	selection, ok := QCProbeSelectionFromContext(ctx)
	if !ok || !selection.Active || !selection.appliesToPlatform(platform) || !selection.hasAccountPool() {
		return true
	}
	return selection.allowsAccountID(accountID)
}

// IsQCProbeGlobalPoolAccount reports whether this account is a configured global-scope
// QC pool member for the request platform (used to bypass group membership checks).
func IsQCProbeGlobalPoolAccount(ctx context.Context, platform string, accountID int64) bool {
	selection, ok := QCProbeSelectionFromContext(ctx)
	if !ok || !selection.Active || !selection.isGlobalScope() || !selection.appliesToPlatform(platform) || !selection.hasAccountPool() {
		return false
	}
	return selection.allowsAccountID(accountID)
}

// applyQCProbeGlobalPoolAccountOverrides relaxes manual unschedulable for global QC pool
// accounts so sticky/direct loads match ResolveAccountsForQCProbe behavior.
func applyQCProbeGlobalPoolAccountOverrides(ctx context.Context, account *Account) {
	if account == nil {
		return
	}
	if IsQCProbeGlobalPoolAccount(ctx, account.Platform, account.ID) {
		account.Schedulable = true
	}
}

// LoadQCProbeAccountsByIDs adapts AccountRepository.GetByIDs for ResolveAccountsForQCProbe.
func LoadQCProbeAccountsByIDs(repo interface {
	GetByIDs(ctx context.Context, ids []int64) ([]*Account, error)
}) QCProbeAccountLoader {
	if repo == nil {
		return nil
	}
	return func(ctx context.Context, ids []int64) ([]Account, error) {
		if len(ids) == 0 {
			return nil, nil
		}
		rows, err := repo.GetByIDs(ctx, ids)
		if err != nil {
			return nil, err
		}
		out := make([]Account, 0, len(rows))
		// Preserve configured pool order.
		byID := make(map[int64]*Account, len(rows))
		for _, row := range rows {
			if row != nil {
				byID[row.ID] = row
			}
		}
		for _, id := range ids {
			if acc, ok := byID[id]; ok && acc != nil {
				out = append(out, *acc)
			}
		}
		return out, nil
	}
}
