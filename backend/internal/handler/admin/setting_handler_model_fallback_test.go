package admin

import (
	"reflect"
	"testing"
)

func TestResolveFallbackModelsUpdate(t *testing.T) {
	legacy := "legacy-model"
	list := []string{"model-b", "model-c"}
	previous := []string{"previous-model"}

	if got := resolveFallbackModelsUpdate(&list, &legacy, previous); !reflect.DeepEqual(got, list) {
		t.Fatalf("explicit list = %v, want %v", got, list)
	}
	if got, want := resolveFallbackModelsUpdate(nil, &legacy, previous), []string{"legacy-model"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("legacy setting = %v, want %v", got, want)
	}
	if got := resolveFallbackModelsUpdate(nil, nil, previous); !reflect.DeepEqual(got, previous) {
		t.Fatalf("omitted settings = %v, want previous %v", got, previous)
	}

	empty := ""
	if got := resolveFallbackModelsUpdate(nil, &empty, previous); len(got) != 0 {
		t.Fatalf("explicit empty legacy setting = %v, want empty list", got)
	}
}
