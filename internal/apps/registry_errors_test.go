package apps

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/supermarket"
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

func TestFailureShowsARegistryErrorByItsDetail(t *testing.T) {
	err := fail("publish Skills", fmt.Errorf("download Registry Skill Artifact: %w: %w", supermarket.ErrAppInvalid, errors.New("SHA-256 verification failed")))
	var f *failure
	if !errors.As(err, &f) {
		t.Fatalf("fail() = %T, want *failure", err)
	}
	if got := f.Public(); got != "publish Skills: The App is invalid." {
		t.Fatalf("Public() = %q, want the step and the registry detail", got)
	}
	if strings.Contains(publicMessage(err), "SHA-256") {
		t.Fatalf("publicMessage(%v) repeats the cause", err)
	}
}
