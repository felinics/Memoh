package agent

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/memohai/memoh/domains/agent/mcp"
)

func TestMCPConnectionServiceError(t *testing.T) {
	tests := []struct {
		name           string
		err            error
		fallbackStatus int
		wantStatus     int
		wantMessage    string
	}{
		{
			name:           "not found",
			err:            fmt.Errorf("get connection: %w", mcp.ErrNotFound),
			fallbackStatus: http.StatusInternalServerError,
			wantStatus:     http.StatusNotFound,
			wantMessage:    "mcp connection not found",
		},
		{
			name:           "bad request fallback",
			err:            errors.New("name is required"),
			fallbackStatus: http.StatusBadRequest,
			wantStatus:     http.StatusBadRequest,
			wantMessage:    "name is required",
		},
		{
			name:           "internal error fallback",
			err:            errors.New("storage unavailable"),
			fallbackStatus: http.StatusInternalServerError,
			wantStatus:     http.StatusInternalServerError,
			wantMessage:    "storage unavailable",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var httpErr *echo.HTTPError
			if !errors.As(mcpConnectionServiceError(test.err, test.fallbackStatus), &httpErr) {
				t.Fatal("mcpConnectionServiceError() did not return *echo.HTTPError")
			}
			if httpErr.Code != test.wantStatus || httpErr.Message != test.wantMessage {
				t.Fatalf("mcpConnectionServiceError() = (%d, %v), want (%d, %q)", httpErr.Code, httpErr.Message, test.wantStatus, test.wantMessage)
			}
		})
	}
}
