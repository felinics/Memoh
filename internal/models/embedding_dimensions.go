package models

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/db"
)

// ErrValidation marks model writes rejected because of the submitted payload,
// so handlers can answer 400 instead of treating user input as a server fault.
var ErrValidation = errors.New("validation failed")

// embeddingDimensionsProbeTimeout bounds the extra embedding call a save may
// trigger. It is shorter than the model test probe because the user is waiting
// on a form submit, and a slow provider still leaves the manual field as a way
// forward.
const embeddingDimensionsProbeTimeout = 20 * time.Second

// fillEmbeddingDimensions detects the vector length of an embedding model saved
// without one. Dimensions must equal what the provider actually returns — the
// memory index is created with that width — so a guessed default would only
// move the failure from this form to the first memory write. Probing uses the
// same one-input embedding request as provider model import.
func (s *Service) fillEmbeddingDimensions(ctx context.Context, model *Model) error {
	if model.Type != ModelTypeEmbedding {
		return nil
	}
	if model.Config.Dimensions != nil && *model.Config.Dimensions > 0 {
		return nil
	}
	// Leave malformed payloads to Validate so they keep their original message.
	if model.ModelID == "" || model.ProviderID == "" {
		return nil
	}
	providerID, err := db.ParseUUID(model.ProviderID)
	if err != nil {
		return nil
	}
	provider, err := s.queries.GetProviderByID(ctx, providerID)
	if err != nil {
		return fmt.Errorf("get provider: %w", err)
	}
	creds, err := s.resolveModelCredentials(ctx, provider)
	if err != nil {
		return fmt.Errorf("%w: could not detect embedding dimensions automatically, enter them manually: %w", ErrValidation, err)
	}

	probeCtx, cancel := context.WithTimeout(ctx, embeddingDimensionsProbeTimeout)
	defer cancel()
	baseURL := strings.TrimRight(providerConfigString(provider.Config, "base_url"), "/")
	dim, err := InferEmbeddingDimensions(probeCtx, provider.ClientType, baseURL, creds.APIKey, model.ModelID, embeddingDimensionsProbeTimeout, nil)
	if err != nil {
		return fmt.Errorf("%w: could not detect embedding dimensions automatically, enter them manually: %w", ErrValidation, err)
	}
	model.Config.Dimensions = &dim
	return nil
}
