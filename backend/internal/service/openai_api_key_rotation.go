package service

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

const (
	OpenAIAPIKeyListCredentialKey        = "api_key_list"
	openAIAPIKeyRotationFailureThreshold = 3
	openAIAPIKeyRotationMaxFallbackKeys  = 20
)

// OpenAIAPIKeyRotationResult is returned by the shared rotation store after a
// qualifying post-output response.failed event.
type OpenAIAPIKeyRotationResult struct {
	ActiveIndex  int
	FailureCount int
	Generation   int64
	Rotated      bool
	Recorded     bool
}

type OpenAIAPIKeyRotationSelection struct {
	ActiveIndex int
	Generation  int64
}

// OpenAIAPIKeyRotationStore keeps key selection consistent across instances.
// Implementations must ignore reports from an index that is no longer active.
type OpenAIAPIKeyRotationStore interface {
	ResolveOpenAIAPIKeyIndex(ctx context.Context, accountID int64, poolFingerprint string, poolSize int) (OpenAIAPIKeyRotationSelection, error)
	RecordOpenAIAPIKeyStreamFailure(ctx context.Context, accountID int64, poolFingerprint string, attemptedIndex int, attemptedGeneration int64, poolSize, threshold int) (OpenAIAPIKeyRotationResult, error)
	ResetOpenAIAPIKeyStreamFailures(ctx context.Context, accountID int64, poolFingerprint string, attemptedIndex int, attemptedGeneration int64) (previousFailures int, reset bool, err error)
}

type openAIAPIKeyRotationAttempt struct {
	AccountID       int64
	PoolFingerprint string
	Index           int
	Generation      int64
	PoolSize        int
}

type openAIAPIKeyRotationTracker struct {
	mu      sync.RWMutex
	attempt *openAIAPIKeyRotationAttempt
}

type openAIAPIKeyRotationTrackerContextKey struct{}

func withOpenAIAPIKeyRotationTracker(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, openAIAPIKeyRotationTrackerContextKey{}, &openAIAPIKeyRotationTracker{})
}

func openAIAPIKeyRotationTrackerFromContext(ctx context.Context) *openAIAPIKeyRotationTracker {
	if ctx == nil {
		return nil
	}
	tracker, _ := ctx.Value(openAIAPIKeyRotationTrackerContextKey{}).(*openAIAPIKeyRotationTracker)
	return tracker
}

func (t *openAIAPIKeyRotationTracker) set(attempt openAIAPIKeyRotationAttempt) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.attempt = &attempt
}

func (t *openAIAPIKeyRotationTracker) get() (openAIAPIKeyRotationAttempt, bool) {
	if t == nil {
		return openAIAPIKeyRotationAttempt{}, false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.attempt == nil {
		return openAIAPIKeyRotationAttempt{}, false
	}
	return *t.attempt, true
}

// NormalizeOpenAIAPIKeyListCredentials validates and canonicalizes the
// account-level fallback list. An explicit empty array clears the saved list.
func NormalizeOpenAIAPIKeyListCredentials(platform, accountType string, credentials map[string]any) error {
	if credentials == nil {
		return nil
	}
	raw, exists := credentials[OpenAIAPIKeyListCredentialKey]
	if !exists {
		return nil
	}
	if platform != PlatformOpenAI || accountType != AccountTypeAPIKey {
		return infraerrors.BadRequest(
			"OPENAI_API_KEY_LIST_UNSUPPORTED",
			"api_key_list is only supported for OpenAI API-key accounts",
		)
	}

	values, err := parseOpenAIAPIKeyList(raw)
	if err != nil {
		return infraerrors.BadRequest("INVALID_OPENAI_API_KEY_LIST", err.Error())
	}
	primary := strings.TrimSpace(stringCredentialValue(credentials["api_key"]))
	seen := make(map[string]struct{}, len(values)+1)
	if primary != "" {
		seen[primary] = struct{}{}
	}
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
		if len(normalized) > openAIAPIKeyRotationMaxFallbackKeys {
			return infraerrors.BadRequest(
				"OPENAI_API_KEY_LIST_TOO_LARGE",
				fmt.Sprintf("api_key_list supports at most %d fallback keys", openAIAPIKeyRotationMaxFallbackKeys),
			)
		}
	}
	credentials[OpenAIAPIKeyListCredentialKey] = normalized
	return nil
}

func parseOpenAIAPIKeyList(raw any) ([]string, error) {
	switch values := raw.(type) {
	case nil:
		return []string{}, nil
	case []string:
		return append([]string(nil), values...), nil
	case []any:
		out := make([]string, 0, len(values))
		for i, value := range values {
			key, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("api_key_list[%d] must be a string", i)
			}
			out = append(out, key)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("api_key_list must be an array of strings")
	}
}

func stringCredentialValue(raw any) string {
	value, _ := raw.(string)
	return value
}

// GetOpenAIAPIKeyPool returns the default key followed by configured fallback
// keys. It remains defensive because credentials can also be imported directly.
func (a *Account) GetOpenAIAPIKeyPool() []string {
	if a == nil || !a.IsOpenAIApiKey() {
		return nil
	}
	primary := strings.TrimSpace(a.GetCredential("api_key"))
	if primary == "" {
		return nil
	}
	pool := []string{primary}
	seen := map[string]struct{}{primary: {}}
	values, err := parseOpenAIAPIKeyList(a.Credentials[OpenAIAPIKeyListCredentialKey])
	if err != nil {
		return pool
	}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		pool = append(pool, value)
		if len(pool) > openAIAPIKeyRotationMaxFallbackKeys {
			break
		}
	}
	return pool
}

func openAIAPIKeyPoolFingerprint(pool []string) string {
	h := sha256.New()
	var size [8]byte
	for _, key := range pool {
		binary.BigEndian.PutUint64(size[:], uint64(len(key)))
		_, _ = h.Write(size[:])
		_, _ = h.Write([]byte(key))
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

func (s *OpenAIGatewayService) resolveOpenAIAPIKey(ctx context.Context, account *Account) string {
	pool := account.GetOpenAIAPIKeyPool()
	if len(pool) == 0 {
		return ""
	}
	fingerprint := openAIAPIKeyPoolFingerprint(pool)
	index := 0
	generation := int64(0)
	if len(pool) > 1 {
		if store, ok := s.cache.(OpenAIAPIKeyRotationStore); ok {
			resolved, err := store.ResolveOpenAIAPIKeyIndex(ctx, account.ID, fingerprint, len(pool))
			if err != nil {
				logger.FromContext(ctx).Warn("openai.api_key_rotation_resolve_failed",
					zap.Int64("account_id", account.ID),
					zap.Error(err),
				)
			} else if resolved.ActiveIndex >= 0 && resolved.ActiveIndex < len(pool) {
				index = resolved.ActiveIndex
				generation = resolved.Generation
			}
		}
	}
	if tracker := openAIAPIKeyRotationTrackerFromContext(ctx); tracker != nil {
		tracker.set(openAIAPIKeyRotationAttempt{
			AccountID:       account.ID,
			PoolFingerprint: fingerprint,
			Index:           index,
			Generation:      generation,
			PoolSize:        len(pool),
		})
	}
	return pool[index]
}

func (s *OpenAIGatewayService) recordOpenAIAPIKeyCommittedStreamFailure(ctx context.Context, account *Account) {
	if s == nil || account == nil {
		return
	}
	tracker := openAIAPIKeyRotationTrackerFromContext(ctx)
	attempt, ok := tracker.get()
	if !ok || attempt.AccountID != account.ID || attempt.PoolSize <= 1 {
		return
	}
	store, ok := s.cache.(OpenAIAPIKeyRotationStore)
	if !ok {
		return
	}
	result, err := store.RecordOpenAIAPIKeyStreamFailure(
		ctx,
		attempt.AccountID,
		attempt.PoolFingerprint,
		attempt.Index,
		attempt.Generation,
		attempt.PoolSize,
		openAIAPIKeyRotationFailureThreshold,
	)
	if err != nil {
		logger.FromContext(ctx).Warn("openai.api_key_rotation_failure_record_failed",
			zap.Int64("account_id", account.ID),
			zap.Int("attempted_index", attempt.Index),
			zap.Error(err),
		)
		return
	}
	if !result.Recorded {
		return
	}
	fields := []zap.Field{
		zap.Int64("account_id", account.ID),
		zap.Int("attempted_index", attempt.Index),
		zap.Int("active_index", result.ActiveIndex),
		zap.Int64("generation", result.Generation),
		zap.Int("consecutive_failures", result.FailureCount),
		zap.Bool("rotated", result.Rotated),
	}
	if result.Rotated {
		logger.FromContext(ctx).Warn("openai.api_key_rotated_after_committed_stream_failures", fields...)
	} else {
		logger.FromContext(ctx).Info("openai.api_key_committed_stream_failure_recorded", fields...)
	}
}

func (s *OpenAIGatewayService) resetOpenAIAPIKeyCommittedStreamFailures(ctx context.Context, account *Account) {
	if s == nil || account == nil {
		return
	}
	tracker := openAIAPIKeyRotationTrackerFromContext(ctx)
	attempt, ok := tracker.get()
	if !ok || attempt.AccountID != account.ID || attempt.PoolSize <= 1 {
		return
	}
	store, ok := s.cache.(OpenAIAPIKeyRotationStore)
	if !ok {
		return
	}
	previous, reset, err := store.ResetOpenAIAPIKeyStreamFailures(
		ctx,
		attempt.AccountID,
		attempt.PoolFingerprint,
		attempt.Index,
		attempt.Generation,
	)
	if err != nil {
		logger.FromContext(ctx).Warn("openai.api_key_rotation_success_reset_failed",
			zap.Int64("account_id", account.ID),
			zap.Int("attempted_index", attempt.Index),
			zap.Error(err),
		)
		return
	}
	if reset && previous > 0 {
		logger.FromContext(ctx).Info("openai.api_key_rotation_failure_streak_reset",
			zap.Int64("account_id", account.ID),
			zap.Int("active_index", attempt.Index),
			zap.Int("previous_consecutive_failures", previous),
		)
	}
}
