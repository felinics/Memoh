package chat

import (
	"errors"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/memohai/memoh/internal/apperror"
)

// requireHTTPStatus accepts either a migrated apperror or a legacy echo.HTTPError
// still returned by httpx helpers outside this shard.
func requireHTTPStatus(t *testing.T, err error, status int) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want HTTP %d", status)
	}
	var httpErr *echo.HTTPError
	if errors.As(err, &httpErr) {
		if httpErr.Code != status {
			t.Fatalf("error = %v, want HTTP %d", err, status)
		}
		return
	}
	if got := apperror.KindOf(err).HTTPStatus(); got != status {
		t.Fatalf("error = %v (status %d), want HTTP %d", err, got, status)
	}
}
