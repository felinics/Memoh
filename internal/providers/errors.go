package providers

import "errors"

// ErrNameTaken reports that another provider already has the requested name.
var ErrNameTaken = errors.New("provider name is already taken")
