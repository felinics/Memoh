package catalog

import catalogport "github.com/memohai/memoh/domains/model/internal/port/catalog"

// Persistence sentinels and store types for consumers/tests that need the
// narrow catalog persistence surface without importing owner-private ports.
type (
	Store       = catalogport.Store
	Record      = catalogport.Record
	CreateInput = catalogport.CreateInput
	UpdateInput = catalogport.UpdateInput
)
