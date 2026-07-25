package execution

import executionport "github.com/memohai/memoh/domains/model/internal/port/execution"

// Provider-facing execution types. Persistence adapters speak the owner-private
// port; public consumers use these names.
type (
	ProviderDescriptor = executionport.ProviderDescriptor
	Provider           = executionport.Provider
	ProviderSource     = executionport.ProviderSource
)

// ErrProviderNotFound is returned when an execution provider lookup misses.
var ErrProviderNotFound = executionport.ErrProviderNotFound

// SanitizeError redacts credential material while preserving error identity.
func SanitizeError(err error, secrets ...string) error {
	return executionport.SanitizeError(err, secrets...)
}
