package runtime

import (
	"errors"
	"net/http"
	"testing"

	"github.com/labstack/echo/v4"

	userruntime "github.com/memohai/memoh/domains/runtime/client"
)

func TestRuntimeHTTPErrorUsesDomainErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code int
	}{
		{name: "not found", err: userruntime.ErrRuntimeNotFound, code: http.StatusNotFound},
		{name: "duplicate name", err: userruntime.ErrRuntimeNameTaken, code: http.StatusConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runtimeHTTPError(nil, tt.err)
			var httpErr *echo.HTTPError
			if !errors.As(err, &httpErr) || httpErr.Code != tt.code {
				t.Fatalf("runtimeHTTPError() = %#v, want HTTP %d", err, tt.code)
			}
		})
	}
}
