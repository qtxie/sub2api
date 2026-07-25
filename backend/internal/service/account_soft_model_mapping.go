package service

import (
	"strings"
)

const softModelMappingCredentialKey = "soft_model_mapping"

// GetSoftModelMapping returns account-level soft model fallbacks.
// Soft mapping never rewrites the first upstream attempt; targets are only
// used after the requested model fails same-account model-fallback criteria.
//
// Stored shape in credentials.soft_model_mapping:
//
//	{"gpt-5.5": "gpt-5.4"}
//	{"gpt-5.5": ["gpt-5.4", "gpt-4o"]}
func (a *Account) GetSoftModelMapping() map[string][]string {
	if a == nil || a.Credentials == nil {
		return nil
	}
	return parseSoftModelMapping(a.Credentials[softModelMappingCredentialKey])
}

// GetSoftModelFallbacks returns ordered soft fallback models for a request
// model. The requested model itself is never included.
func (a *Account) GetSoftModelFallbacks(requestedModel string) []string {
	requestedModel = strings.TrimSpace(requestedModel)
	if a == nil || requestedModel == "" {
		return nil
	}
	mapping := a.GetSoftModelMapping()
	if len(mapping) == 0 {
		return nil
	}
	if targets := mapping[requestedModel]; len(targets) > 0 {
		return filterSoftModelFallbacks(requestedModel, targets)
	}
	normalized := normalizeRequestedModelForLookup(a.Platform, requestedModel)
	if normalized != "" && normalized != requestedModel {
		if targets := mapping[normalized]; len(targets) > 0 {
			return filterSoftModelFallbacks(requestedModel, targets)
		}
	}
	return nil
}

// HasSoftModelFallbacks reports whether the account defines soft fallbacks for
// the requested model.
func (a *Account) HasSoftModelFallbacks(requestedModel string) bool {
	return len(a.GetSoftModelFallbacks(requestedModel)) > 0
}

// HasSoftModelMappingKey reports whether the requested model is a configured
// soft-mapping source. Used so accounts with a hard whitelist still admit
// soft-mapped request models.
func (a *Account) HasSoftModelMappingKey(requestedModel string) bool {
	requestedModel = strings.TrimSpace(requestedModel)
	if a == nil || requestedModel == "" {
		return false
	}
	mapping := a.GetSoftModelMapping()
	if len(mapping) == 0 {
		return false
	}
	if _, ok := mapping[requestedModel]; ok {
		return true
	}
	normalized := normalizeRequestedModelForLookup(a.Platform, requestedModel)
	if normalized != "" && normalized != requestedModel {
		_, ok := mapping[normalized]
		return ok
	}
	return false
}

func parseSoftModelMapping(raw any) map[string][]string {
	switch mapping := raw.(type) {
	case map[string]any:
		if len(mapping) == 0 {
			return nil
		}
		result := make(map[string][]string, len(mapping))
		for key, value := range mapping {
			from := strings.TrimSpace(key)
			if from == "" {
				continue
			}
			targets := parseSoftModelMappingTargets(value)
			if len(targets) == 0 {
				continue
			}
			result[from] = targets
		}
		if len(result) == 0 {
			return nil
		}
		return result
	case map[string]string:
		if len(mapping) == 0 {
			return nil
		}
		result := make(map[string][]string, len(mapping))
		for key, value := range mapping {
			from := strings.TrimSpace(key)
			to := strings.TrimSpace(value)
			if from == "" || to == "" {
				continue
			}
			result[from] = []string{to}
		}
		if len(result) == 0 {
			return nil
		}
		return result
	case map[string][]string:
		if len(mapping) == 0 {
			return nil
		}
		result := make(map[string][]string, len(mapping))
		for key, values := range mapping {
			from := strings.TrimSpace(key)
			if from == "" {
				continue
			}
			targets := filterSoftModelFallbacks(from, values)
			if len(targets) == 0 {
				continue
			}
			result[from] = targets
		}
		if len(result) == 0 {
			return nil
		}
		return result
	default:
		return nil
	}
}

func parseSoftModelMappingTargets(raw any) []string {
	switch value := raw.(type) {
	case string:
		value = strings.TrimSpace(value)
		if value == "" {
			return nil
		}
		return []string{value}
	case []string:
		return filterSoftModelFallbacks("", value)
	case []any:
		targets := make([]string, 0, len(value))
		for _, item := range value {
			str, ok := item.(string)
			if !ok {
				continue
			}
			str = strings.TrimSpace(str)
			if str == "" {
				continue
			}
			targets = append(targets, str)
		}
		return filterSoftModelFallbacks("", targets)
	default:
		return nil
	}
}

func filterSoftModelFallbacks(requestedModel string, targets []string) []string {
	if len(targets) == 0 {
		return nil
	}
	requestedModel = strings.TrimSpace(requestedModel)
	out := make([]string, 0, len(targets))
	seen := make(map[string]struct{}, len(targets))
	if requestedModel != "" {
		seen[requestedModel] = struct{}{}
	}
	for _, target := range targets {
		target = strings.TrimSpace(target)
		if target == "" {
			continue
		}
		if _, ok := seen[target]; ok {
			continue
		}
		seen[target] = struct{}{}
		out = append(out, target)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
