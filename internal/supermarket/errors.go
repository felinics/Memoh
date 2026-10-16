package supermarket

import (
	"errors"
	"fmt"
)

// Errors the Installer reports an App it could not fetch or install with.
// Callers match them with errors.Is; the cause stays in the chain.
var (
	// ErrAppNotFound is an App or release the registry does not publish.
	ErrAppNotFound = errors.New("registry App was not found")
	// ErrRegistryUnavailable is a registry that could not be reached or did
	// not answer.
	ErrRegistryUnavailable = errors.New("registry is unavailable")
	// ErrAppInvalid is an App release, descriptor or Artifact that does not
	// satisfy the registry protocol.
	ErrAppInvalid = errors.New("registry App is invalid")
	// ErrInstallFailed is a valid App that could not be written into the
	// workspace.
	ErrInstallFailed = errors.New("registry App could not be installed")

	// ErrRevisionInvalid, ErrRegistryIDInvalid and ErrAppIDInvalid mean the
	// request named an identifier that is not well formed. They are the
	// caller's input, not a registry answer.
	ErrRevisionInvalid   = errors.New("revision is invalid")
	ErrRegistryIDInvalid = errors.New("registry_id is invalid")
	ErrAppIDInvalid      = errors.New("app_id is invalid")
)

func invalidApp(err error) error {
	return fmt.Errorf("%w: %w", ErrAppInvalid, err)
}

func installFailed(err error) error {
	return fmt.Errorf("%w: %w", ErrInstallFailed, err)
}
