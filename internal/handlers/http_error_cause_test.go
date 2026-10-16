package handlers

import (
	"errors"
	"net/http"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/workdir"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

// A status-only echo.HTTPError answers with the generic code for its status;
// the cause has to stay on the error for the result record.
func TestStatusOnlyHTTPErrorsKeepCause(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		cause  error
		status int
	}{
		{"fs forbidden", fsHTTPError(bridge.ErrForbidden), bridge.ErrForbidden, http.StatusForbidden},
		{"workdir picker forbidden", workdirDirectoriesHTTPError(workdir.ErrPathForbidden), workdir.ErrPathForbidden, http.StatusForbidden},
		{"workdir duplicate", workdirHTTPError(workdir.ErrDuplicatePath), workdir.ErrDuplicatePath, http.StatusConflict},
		{"grant exists", (*BotUserAccessHandler)(nil).mapGrantError(bots.ErrGrantExists), bots.ErrGrantExists, http.StatusConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var he *echo.HTTPError
			if !errors.As(tc.err, &he) {
				t.Fatalf("got %T, want *echo.HTTPError", tc.err)
			}
			if he.Code != tc.status {
				t.Fatalf("status = %d, want %d", he.Code, tc.status)
			}
			if !errors.Is(he.Internal, tc.cause) {
				t.Fatalf("internal = %v, want cause %v", he.Internal, tc.cause)
			}
			if he.Message != http.StatusText(tc.status) {
				t.Fatalf("message = %v, want the status text only", he.Message)
			}
		})
	}
}
