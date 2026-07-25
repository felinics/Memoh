package email

import "errors"

// ErrNotFound is returned when an email provider, binding, outbox, or OAuth
// record cannot be found.
var ErrNotFound = errors.New("email record not found")
