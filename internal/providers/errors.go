package providers

import "errors"

// ErrNameTaken reports that another provider already has the requested name.
var ErrNameTaken = errors.New("provider name is already taken")

// ErrOAuthUnsupported reports that the provider's client type has no OAuth
// sign-in, or no device authorization for the operation asked.
var ErrOAuthUnsupported = errors.New("provider does not support oauth")

// ErrOAuthStateInvalid reports an OAuth callback whose state matches no
// pending sign-in.
var ErrOAuthStateInvalid = errors.New("oauth state is unknown or expired")

// ErrProviderNotFound reports an OAuth operation on a provider that does not
// exist.
var ErrProviderNotFound = errors.New("provider not found")
