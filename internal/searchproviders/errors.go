package searchproviders

import "errors"

var (
	// ErrInvalidProvider reports a provider type that is not supported.
	ErrInvalidProvider = errors.New("invalid provider")
	// ErrTypeConflict reports that a search provider of the requested type
	// is already configured.
	ErrTypeConflict = errors.New("search provider type is already configured")
	// ErrNameTaken reports that another search provider already has the
	// requested name.
	ErrNameTaken = errors.New("search provider name is already taken")
)
