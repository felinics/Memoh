package providertemplates

import "errors"

var (
	// ErrNotFound reports that no provider template has the requested ID.
	ErrNotFound = errors.New("provider template not found")
	// ErrDomainInvalid reports a domain that is not a provider template domain.
	ErrDomainInvalid = errors.New("invalid provider template domain")
	// ErrDomainMismatch reports a template whose domain is not the one the
	// caller asked for or can use.
	ErrDomainMismatch = errors.New("provider template domain does not match")
)
