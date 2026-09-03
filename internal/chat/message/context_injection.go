package message

import (
	"encoding/json"
	"strings"
)

const (
	// ContextInjectionMetadataKey marks a persisted user-role row that the
	// runtime injected into the model context rather than a person sending it.
	ContextInjectionMetadataKey = "context_injection"
	// ContextInjectionSteering is a message the user sent mid-turn.
	ContextInjectionSteering = "steering"
	// ContextInjectionPrepared is context appended before a model request
	// (background summaries, hook context, media the model asked to read).
	ContextInjectionPrepared = "prepared"
)

type ContextInjectionMetadata struct {
	Kind string `json:"kind"`
}

func ContextInjectionFromMetadata(metadata map[string]any) *ContextInjectionMetadata {
	if len(metadata) == 0 {
		return nil
	}
	value, ok := metadata[ContextInjectionMetadataKey]
	if !ok || value == nil {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var injection ContextInjectionMetadata
	if err := json.Unmarshal(data, &injection); err != nil {
		return nil
	}
	injection.Kind = strings.TrimSpace(injection.Kind)
	if injection.Kind == "" {
		return nil
	}
	return &injection
}
