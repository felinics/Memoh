package providertemplates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/errs"
)

// Resolve reads the template with the given ID. An ID that does not parse
// or names no template is ErrNotFound; a template outside expectedDomain,
// when one is given, is ErrDomainMismatch.
func Resolve(ctx context.Context, queries dbstore.Queries, id string, expectedDomain Domain) (sqlc.TemplateProviderTemplate, error) {
	pgID, err := db.ParseUUID(strings.TrimSpace(id))
	if err != nil {
		return sqlc.TemplateProviderTemplate{}, fmt.Errorf("%w: %w", ErrNotFound, err)
	}
	row, err := queries.GetProviderTemplateByID(ctx, pgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, db.ErrNotFound) {
			return sqlc.TemplateProviderTemplate{}, ErrNotFound
		}
		return sqlc.TemplateProviderTemplate{}, errs.Wrap(err, "get provider template")
	}
	if expectedDomain != "" && row.Domain != string(expectedDomain) {
		return sqlc.TemplateProviderTemplate{}, ErrDomainMismatch
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

func MergeMetadata(template sqlc.TemplateProviderTemplate, incoming map[string]any) map[string]any {
	metadata := DecodeConfig(template.Metadata)
	for key, value := range incoming {
		metadata[key] = value
	}
	metadata["template"] = map[string]any{
		"id":     template.ID.String(),
		"key":    template.Key,
		"domain": template.Domain,
		"source": template.Source,
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
