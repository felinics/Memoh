package apps

import (
	"context"
	"errors"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/connectors"
	"github.com/felinics/memoh/internal/workspacedeps"
)

// failure is a failed App step. Its text keeps the step and the full cause
// for logs and callers; what the user sees is the catalog code publicCode
// derives from the cause.
type failure struct {
	step  string
	cause error
}

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

// errDiscoveryFailed marks a workspace discovery that reported a problem; its
// text is the discovery message and stays in the log.
var errDiscoveryFailed = errors.New("workspace discovery failed")

// sentinelCodes maps the errors the apps service and its collaborators report
// to the catalog code a user sees.
var sentinelCodes = []struct {
	err  error
	code apperror.Code
}{
	{ErrInvalidRequest, apperror.CodeAppRequestInvalid},
	{ErrConnectorNotReferenced, apperror.CodeAppRequestInvalid},
	{ErrDependenciesUnavailable, apperror.CodeAppDependenciesUnavailable},
	{ErrNotInstalled, apperror.CodeAppNotFound},
	{errDiscoveryFailed, apperror.CodeWorkspaceDependencyDiscoveryFailed},
	{workspacedeps.ErrDependencyNotFound, apperror.CodeWorkspaceDependencyNotFound},
	{workspacedeps.ErrPlatformUnsupported, apperror.CodeWorkspaceDependencyPlatformUnsupported},
	{workspacedeps.ErrBusy, apperror.CodeWorkspaceDependencyBusy},
	{workspacedeps.ErrWorkspaceNotRunning, apperror.CodeWorkspaceDependencyWorkspaceNotRunning},
	{workspacedeps.ErrWorkspaceMissing, apperror.CodeWorkspaceDependencyWorkspaceMissing},
	{workspacedeps.ErrRollbackUnavailable, apperror.CodeWorkspaceDependencyRollbackUnavailable},
	{workspacedeps.ErrActionUnsupported, apperror.CodeWorkspaceDependencyActionUnsupported},
	{workspacedeps.ErrOperationUncertain, apperror.CodeWorkspaceDependencyOperationUnknown},
	{workspacedeps.ErrCatalogUnavailable, apperror.CodeWorkspaceDependencyCatalogUnavailable},
	{workspacedeps.ErrDefinitionInvalid, apperror.CodeWorkspaceDependencyDefinitionInvalid},
	{workspacedeps.ErrDefinitionUnavailable, apperror.CodeWorkspaceDependencyDefinitionUnavailable},
}

// publicCode is the catalog code that describes err to a user and is stored
// in last_error_code. Anything it does not recognize is app.operation_failed;
// the cause belongs in the log.
func publicCode(err error) apperror.Code {
	if code := apperror.CodeOf(RegistryError(err)); code != "" {
		return code
	}
	for _, s := range sentinelCodes {
		if errors.Is(err, s.err) {
			return s.code
		}
	}
	if code := connectors.CodeOf(err); code != "" {
		return code
	}
	switch {
	case errors.Is(err, context.Canceled):
		return apperror.CodeCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return apperror.CodeHTTPGatewayTimeout
	}
	return apperror.CodeAppOperationFailed
}
