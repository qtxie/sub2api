package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

type cachedQCProbeRoutingSettings struct {
	value     *QCProbeRoutingSettings
	expiresAt int64
}

const qcProbeRoutingCacheTTL = 60 * time.Second
const qcProbeRoutingErrorTTL = 5 * time.Second
const qcProbeRoutingDBTimeout = 5 * time.Second

// GetQCProbeRoutingSettings returns cached QC probe routing settings for hot paths.
func (s *SettingService) GetQCProbeRoutingSettings(ctx context.Context) *QCProbeRoutingSettings {
	if s == nil {
		return DefaultQCProbeRoutingSettings()
	}
	if cached, ok := s.qcProbeRoutingCache.Load().(*cachedQCProbeRoutingSettings); ok && cached != nil {
		if time.Now().UnixNano() < cached.expiresAt && cached.value != nil {
			return cached.value
		}
	}

	result, _, _ := s.qcProbeRoutingSF.Do("qc_probe_routing", func() (any, error) {
		if cached, ok := s.qcProbeRoutingCache.Load().(*cachedQCProbeRoutingSettings); ok && cached != nil {
			if time.Now().UnixNano() < cached.expiresAt && cached.value != nil {
				return cached, nil
			}
		}

		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), qcProbeRoutingDBTimeout)
		defer cancel()

		value, err := s.settingRepo.GetValue(dbCtx, SettingKeyQCProbeRouting)
		if err != nil {
			if !errors.Is(err, ErrSettingNotFound) {
				defaults := DefaultQCProbeRoutingSettings()
				s.qcProbeRoutingCache.Store(&cachedQCProbeRoutingSettings{
					value:     defaults,
					expiresAt: time.Now().Add(qcProbeRoutingErrorTTL).UnixNano(),
				})
				return &cachedQCProbeRoutingSettings{value: defaults}, nil
			}
			defaults := DefaultQCProbeRoutingSettings()
			s.qcProbeRoutingCache.Store(&cachedQCProbeRoutingSettings{
				value:     defaults,
				expiresAt: time.Now().Add(qcProbeRoutingCacheTTL).UnixNano(),
			})
			return &cachedQCProbeRoutingSettings{value: defaults}, nil
		}

		settings := DefaultQCProbeRoutingSettings()
		if value != "" {
			var parsed QCProbeRoutingSettings
			if err := json.Unmarshal([]byte(value), &parsed); err != nil {
				slog.Warn("qc_probe.routing_settings_unmarshal_failed", "error", err)
			} else {
				settings = NormalizeQCProbeRoutingSettings(&parsed)
			}
		}
		cached := &cachedQCProbeRoutingSettings{
			value:     settings,
			expiresAt: time.Now().Add(qcProbeRoutingCacheTTL).UnixNano(),
		}
		s.qcProbeRoutingCache.Store(cached)
		return cached, nil
	})

	if cached, ok := result.(*cachedQCProbeRoutingSettings); ok && cached != nil && cached.value != nil {
		return cached.value
	}
	return DefaultQCProbeRoutingSettings()
}

// SetQCProbeRoutingSettings persists and invalidates the QC probe routing cache.
func (s *SettingService) SetQCProbeRoutingSettings(ctx context.Context, settings *QCProbeRoutingSettings) error {
	if s == nil {
		return fmt.Errorf("setting service is nil")
	}
	normalized := NormalizeQCProbeRoutingSettings(settings)
	data, err := json.Marshal(normalized)
	if err != nil {
		return fmt.Errorf("marshal qc probe routing settings: %w", err)
	}
	if err := s.settingRepo.Set(ctx, SettingKeyQCProbeRouting, string(data)); err != nil {
		return err
	}
	s.qcProbeRoutingCache.Store(&cachedQCProbeRoutingSettings{
		value:     normalized,
		expiresAt: time.Now().Add(qcProbeRoutingCacheTTL).UnixNano(),
	})
	if s.onUpdate != nil {
		s.onUpdate()
	}
	return nil
}
