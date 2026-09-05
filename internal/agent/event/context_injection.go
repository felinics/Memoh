package event

import "strings"

const (
	// ContextInjectionMetadataKey stamps a text part of a user message the
	// runtime appended to the model context on its own; the value is the kind.
	ContextInjectionMetadataKey = "context_injection"
	// ContextInjectionSteering is a message the user sent mid-turn.
	ContextInjectionSteering = "steering"
)

// ContextInjectionKind returns the stamped kind of a message part, or "".
func ContextInjectionKind(providerMetadata map[string]any) string {
	kind, _ := providerMetadata[ContextInjectionMetadataKey].(string)
	return strings.TrimSpace(kind)
}
