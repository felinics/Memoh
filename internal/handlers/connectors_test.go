package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	connectsdk "github.com/memohai/connect-it/sdk/go"

	"github.com/memohai/memoh/internal/apperror"
	"github.com/memohai/memoh/internal/connectors"
)

func TestConnectorHTTPErrorUsesStablePublicContract(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     error
		code      apperror.Code
		status    int
		causeKept bool
	}{
		{
			name:      "invalid input",
			input:     fmt.Errorf("SECRET validation detail: %w", connectors.ErrInvalidInput),
			code:      apperror.CodeConnectorRequestInvalid,
			status:    http.StatusBadRequest,
			causeKept: true,
		},
		{
			name:   "not configured",
			input:  connectors.ErrNotConfigured,
			code:   apperror.CodeConnectorNotConfigured,
			status: http.StatusServiceUnavailable,
		},
		{
			name:   "missing binding",
			input:  pgx.ErrNoRows,
			code:   apperror.CodeConnectorNotFound,
			status: http.StatusNotFound,
		},
		{
			name: "upstream rejected request",
			input: &connectsdk.APIError{
				StatusCode: http.StatusUnprocessableEntity,
				Message:    "SECRET credential detail",
			},
			code:      apperror.CodeConnectorRequestRejected,
			status:    http.StatusBadRequest,
			causeKept: true,
		},
		{
			name: "upstream missing connection",
			input: &connectsdk.APIError{
				StatusCode: http.StatusNotFound,
				Message:    "SECRET connection identifier",
			},
			code:      apperror.CodeConnectorNotFound,
			status:    http.StatusNotFound,
			causeKept: true,
		},
		{
			name: "upstream conflict",
			input: &connectsdk.APIError{
				StatusCode: http.StatusConflict,
				Message:    "SECRET conflict detail",
			},
			code:      apperror.CodeConnectorConflict,
			status:    http.StatusConflict,
			causeKept: true,
		},
		{
			name: "upstream failure",
			input: &connectsdk.APIError{
				StatusCode: http.StatusInternalServerError,
				Message:    "SECRET upstream response",
			},
			code:      apperror.CodeConnectorUpstreamUnavailable,
			status:    http.StatusBadGateway,
			causeKept: true,
		},
		{
			name: "transport failure",
			input: fmt.Errorf(
				"%w: %w",
				connectors.ErrUpstreamUnavailable,
				errors.New("dial tcp SECRET.internal: connection refused"),
			),
			code:      apperror.CodeConnectorUpstreamUnavailable,
			status:    http.StatusBadGateway,
			causeKept: true,
		},
		{
			name:      "local operation failure",
			input:     errors.New("database SECRET.internal: connection refused"),
			code:      apperror.CodeConnectorOperationFailed,
			status:    http.StatusInternalServerError,
			causeKept: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := connectorHTTPError(tt.input)
			if got := apperror.CodeOf(err); got != tt.code {
				t.Fatalf("CodeOf() = %q, want %q", got, tt.code)
			}
			if got := apperror.CauseOf(err); (got != nil) != tt.causeKept {
				t.Fatalf("CauseOf() = %v, causeKept = %v", got, tt.causeKept)
			}

			problem, ok := apperror.ProblemFrom(err, "req-connectors")
			if !ok {
				t.Fatal("ProblemFrom() did not recognize connector error")
			}
			if problem.Status != tt.status {
				t.Fatalf("status = %d, want %d", problem.Status, tt.status)
			}
			if problem.Code != string(tt.code) {
				t.Fatalf("code = %q, want %q", problem.Code, tt.code)
			}
			if strings.Contains(problem.Detail, "SECRET") {
				t.Fatalf("private upstream detail leaked: %q", problem.Detail)
			}
		})
	}
}
