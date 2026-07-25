package template

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type TemplateFinder interface {
	FindTemplate(context.Context, string) (CatalogTemplate, error)
}

func Resolve(ctx context.Context, store TemplateFinder, id string, expectedDomain Domain) (CatalogTemplate, error) {
	if store == nil {
		return CatalogTemplate{}, errors.New("provider template catalog store is required")
	}
	row, err := store.FindTemplate(ctx, strings.TrimSpace(id))
	if err != nil {
		if errors.Is(err, ErrTemplateNotFound) {
			return CatalogTemplate{}, ErrTemplateNotFound
		}
		return CatalogTemplate{}, fmt.Errorf("get provider template: %w", err)
	}
	if expectedDomain != "" && row.Domain != string(expectedDomain) {
		return CatalogTemplate{}, ErrDomainMismatch
	}
	return row, nil
}

func DecodeConfig(raw []byte) map[string]any {
	return decodeMap(raw)
}

func MergeConfig(defaults map[string]any, incoming map[string]any) map[string]any {
	merged := make(map[string]any, len(defaults)+len(incoming))
	for key, value := range defaults {
		merged[key] = value
	}
	for key, value := range incoming {
		merged[key] = value
	}
	return merged
}

func MergeMetadata(row CatalogTemplate, incoming map[string]any) map[string]any {
	metadata := DecodeConfig(row.Metadata)
	for key, value := range incoming {
		metadata[key] = value
	}
	metadata["template"] = map[string]any{
		"id":     row.ID,
		"key":    row.Key,
		"domain": row.Domain,
		"source": row.Source,
	}
	return metadata
}

func Marshal(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal provider template value: %w", err)
	}
	return raw, nil
}
