package message

import "testing"

func TestContextInjectionFromMetadata(t *testing.T) {
	t.Parallel()

	if got := ContextInjectionFromMetadata(map[string]any{ContextInjectionMetadataKey: ContextInjectionMetadata{Kind: ContextInjectionSteering}}); got == nil || got.Kind != ContextInjectionSteering {
		t.Fatalf("typed decode = %#v", got)
	}
	if got := ContextInjectionFromMetadata(map[string]any{ContextInjectionMetadataKey: map[string]any{"kind": "prepared"}}); got == nil || got.Kind != ContextInjectionPrepared {
		t.Fatalf("json decode = %#v", got)
	}
	if got := ContextInjectionFromMetadata(map[string]any{ContextInjectionMetadataKey: map[string]any{"kind": ""}}); got != nil {
		t.Fatalf("blank kind decoded: %#v", got)
	}
	if got := ContextInjectionFromMetadata(nil); got != nil {
		t.Fatalf("nil metadata decoded: %#v", got)
	}
}
