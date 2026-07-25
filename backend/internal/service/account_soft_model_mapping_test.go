package service

import (
	"reflect"
	"sort"
	"testing"
)

func TestParseSoftModelMappingSupportsStringAndList(t *testing.T) {
	account := &Account{
		Credentials: map[string]any{
			"soft_model_mapping": map[string]any{
				"gpt-5.5": "gpt-5.4",
				"opus":    []any{"sonnet", "haiku", "sonnet", ""},
			},
		},
	}
	if got, want := account.GetSoftModelFallbacks("gpt-5.5"), []string{"gpt-5.4"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("string soft map = %v, want %v", got, want)
	}
	if got, want := account.GetSoftModelFallbacks("opus"), []string{"sonnet", "haiku"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("list soft map = %v, want %v", got, want)
	}
	if account.HasSoftModelFallbacks("missing") {
		t.Fatal("missing soft map key should report no fallbacks")
	}
}

func TestSoftModelMappingSkipsSelfTarget(t *testing.T) {
	account := &Account{
		Credentials: map[string]any{
			"soft_model_mapping": map[string]any{
				"gpt-5.5": []any{"gpt-5.5", "gpt-5.4"},
			},
		},
	}
	if got, want := account.GetSoftModelFallbacks("gpt-5.5"), []string{"gpt-5.4"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("self targets should be filtered: got %v want %v", got, want)
	}
}

func TestSoftModelMappingSourcesListsKeys(t *testing.T) {
	account := &Account{
		Credentials: map[string]any{
			"soft_model_mapping": map[string]any{
				"gpt-5.5": "gpt-5.4",
				"opus":    []any{"sonnet"},
			},
		},
	}
	got := account.SoftModelMappingSources()
	sort.Strings(got)
	if want := []string{"gpt-5.5", "opus"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SoftModelMappingSources() = %v, want %v", got, want)
	}
}
