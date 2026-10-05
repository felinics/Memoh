package handlers

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/apperror"
	supermarketclient "github.com/felinics/memoh/internal/supermarket"
)

func TestAppsHandlerAnswersAnInstallerErrorWithItsRegistryCode(t *testing.T) {
	upstream := errors.New("PRIVATE upstream diagnostic")
	cases := []struct {
		err  error
		code apperror.Code
	}{
		{fmt.Errorf("install App: %w", fmt.Errorf("fetch Registry App: %w: %w", supermarketclient.ErrRegistryUnavailable, upstream)), apperror.CodeRegistryUnavailable},
		{fmt.Errorf("update App: %w", fmt.Errorf("%w: %w", supermarketclient.ErrInstallFailed, upstream)), apperror.CodeRegistryAppInstallFailed},
	}
	for _, tc := range cases {
		got := (&AppsHandler{}).httpError(tc.err)
		if apperror.CodeOf(got) != tc.code || !errors.Is(apperror.CauseOf(got), tc.err) {
			t.Fatalf("httpError(%v) = %s caused by %v, want %s caused by the error", tc.err, apperror.CodeOf(got), apperror.CauseOf(got), tc.code)
		}
		definition, _ := apperror.Lookup(tc.code)
		event := newAppErrorEvent(got, "req-app")
		if event.Code != string(tc.code) || event.Detail != definition.Detail || event.Message != definition.Detail || strings.Contains(event.Message, "PRIVATE") {
			t.Fatalf("event = %#v, want %s with detail %q", event, tc.code, definition.Detail)
		}
	}
}
