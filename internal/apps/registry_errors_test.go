package apps

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	connectsdk "github.com/felinics/connect-it/sdk/go"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/connectors"
	"github.com/felinics/memoh/internal/supermarket"
	"github.com/felinics/memoh/internal/workspacedeps"
)

func TestRegistryErrorTranslatesEachInstallerError(t *testing.T) {
	upstream := errors.New("GET https://supermarket.example/api/artifacts: connection reset")
	cases := []struct {
		err  error
		want apperror.Code
	}{
		{supermarket.ErrAppNotFound, apperror.CodeRegistryAppNotFound},
		{fmt.Errorf("fetch Registry App: %w: %w", supermarket.ErrRegistryUnavailable, upstream), apperror.CodeRegistryUnavailable},
		{fail("fetch App release", fmt.Errorf("%w: %w", supermarket.ErrAppInvalid, upstream)), apperror.CodeRegistryAppInvalid},
		{fmt.Errorf("stage Skills: %w", fmt.Errorf("%w: %w", supermarket.ErrInstallFailed, upstream)), apperror.CodeRegistryAppInstallFailed},
	}
	for _, tc := range cases {
		got := RegistryError(tc.err)
		if apperror.CodeOf(got) != tc.want || !errors.Is(apperror.CauseOf(got), tc.err) {
			t.Fatalf("RegistryError(%v) = %s caused by %v, want %s caused by the error", tc.err, apperror.CodeOf(got), apperror.CauseOf(got), tc.want)
		}
	}
}

func TestRegistryErrorLeavesOtherErrorsAlone(t *testing.T) {
	other := errors.New("apps: record installation: connection reset")
	if got := RegistryError(other); got != other { //nolint:errorlint // Identity: an untranslated error is returned as it is.
		t.Fatalf("RegistryError(%v) = %v, want it unchanged", other, got)
	}
	if RegistryError(nil) != nil {
		t.Fatal("RegistryError(nil) is not nil")
	}
}

func TestFailureIsPublishedAsItsRegistryCode(t *testing.T) {
	err := fail("publish Skills", fmt.Errorf("download Registry Skill Artifact: %w: %w", supermarket.ErrAppInvalid, errors.New("SHA-256 verification failed")))
	if got := publicCode(err); got != apperror.CodeRegistryAppInvalid {
		t.Fatalf("publicCode(%v) = %s, want %s", err, got, apperror.CodeRegistryAppInvalid)
	}
}

func TestPublicCodeClassifiesCausesWithoutTheirText(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want apperror.Code
	}{
		{"sentinel", fail("remove dependency node", workspacedeps.ErrBusy), apperror.CodeWorkspaceDependencyBusy},
		{"dependencies service missing", ErrDependenciesUnavailable, apperror.CodeAppDependenciesUnavailable},
		{"canceled", fmt.Errorf("step: %w", context.Canceled), apperror.CodeCanceled},
		{"unknown", errors.New("bridge dial 10.0.0.8 password=secret"), apperror.CodeAppOperationFailed},
		{"upstream rejected the request", &connectsdk.APIError{StatusCode: http.StatusBadRequest, Message: "payload rejected"}, apperror.CodeConnectorRequestRejected},
		{"upstream missing object", &connectsdk.APIError{StatusCode: http.StatusNotFound, Message: "no such connection"}, apperror.CodeConnectorNotFound},
		{"upstream failure", &connectsdk.APIError{StatusCode: http.StatusInternalServerError, Message: "500"}, apperror.CodeConnectorUpstreamUnavailable},
		{"transport failure", fmt.Errorf("call connect-it: %w", connectors.ErrUpstreamUnavailable), apperror.CodeConnectorUpstreamUnavailable},
	} {
		if got := publicCode(tc.err); got != tc.want {
			t.Errorf("%s: publicCode = %s, want %s", tc.name, got, tc.want)
		}
	}
}
