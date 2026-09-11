package apps

import (
	"context"
	"errors"
	"fmt"

	connectsdk "github.com/felinics/connect-it/sdk/go"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/connectors"
	"github.com/felinics/memoh/internal/supermarket"
	"github.com/felinics/memoh/internal/workspacedeps"
)

// failure is a failed App step. Error keeps the full cause for logs and
// callers; Public is the text that may be persisted in last_error and shown
// to users, which never repeats text from the database, the workspace bridge
// or a third-party service.
type failure struct {
	step  string
	cause error
}

// fail returns a failure for step. A nil cause records a message this
// package authored, which is public as written.
func fail(step string, cause error) error {
	return &failure{step: step, cause: cause}
}

func (f *failure) Error() string {
	if f.cause == nil {
		return "apps: " + f.step
	}
	return "apps: " + f.step + ": " + f.cause.Error()
}

func (f *failure) Unwrap() error { return f.cause }

// Public returns the user-safe description of the failure.
func (f *failure) Public() string {
	if f.cause == nil {
		return f.step
	}
	return f.step + ": " + publicCause(f.cause)
}

// publicMessage returns the user-safe text for an operation error.
func publicMessage(err error) string {
	var f *failure
	if errors.As(err, &f) {
		return f.Public()
	}
	return publicCause(err)
}

// publicSentinels are errors whose text is written for users.
var publicSentinels = []error{
	ErrInvalidRequest, ErrConnectorNotReferenced, ErrDependenciesUnavailable, ErrNotInstalled,
	connectors.ErrInvalidInput, connectors.ErrNotConfigured, connectors.ErrUpstreamUnavailable,
	workspacedeps.ErrDependencyNotFound, workspacedeps.ErrPlatformUnsupported, workspacedeps.ErrBusy,
	workspacedeps.ErrWorkspaceNotRunning, workspacedeps.ErrWorkspaceMissing, workspacedeps.ErrRemoteOffline,
	workspacedeps.ErrRollbackUnavailable, workspacedeps.ErrActionUnsupported, workspacedeps.ErrOperationUncertain,
	workspacedeps.ErrCatalogUnavailable, workspacedeps.ErrDefinitionInvalid, workspacedeps.ErrDefinitionUnavailable,
}

// publicCause classifies an error from another package: catalog errors keep
// their public detail, sentinels their message and upstream HTTP failures
// their status. Anything else is reported generically and belongs in the log.
func publicCause(err error) string {
	if err == nil {
		return ""
	}
	var nested *failure
	if errors.As(err, &nested) {
		return nested.Public()
	}
	if public, ok := apperror.PublicFrom(err, ""); ok {
		return public.Detail
	}
	for _, sentinel := range publicSentinels {
		if errors.Is(err, sentinel) {
			return sentinel.Error()
		}
	}
	var apiErr *connectsdk.APIError
	if errors.As(err, &apiErr) {
		return fmt.Sprintf("Connect-It returned HTTP %d", apiErr.StatusCode)
	}
	var statusErr *supermarket.StatusError
	if errors.As(err, &statusErr) {
		return fmt.Sprintf("Supermarket returned HTTP %d", statusErr.Status)
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "the operation was cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "the operation timed out"
	}
	return "internal error; see the Server log"
}
