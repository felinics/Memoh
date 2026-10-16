package server

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/labstack/echo/v4"
	grpccodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/errs"
)

func TestNewStreamError(t *testing.T) {
	t.Parallel()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name  string
		ctx   context.Context
		err   error
		code  apperror.Code
		fault apperror.Fault
	}{
		{"catalog error", context.Background(), apperror.New(apperror.CodeBotNameTaken, map[string]string{"field": "name", "secret": "x"}), apperror.CodeBotNameTaken, apperror.FaultClient},
		{"http error", context.Background(), echo.NewHTTPError(http.StatusNotFound, "synthetic session not found"), apperror.CodeHTTPNotFound, apperror.FaultClient},
		{"caller canceled", canceled, errors.Join(errors.New("synthetic read"), context.Canceled), apperror.CodeCanceled, apperror.FaultCanceled},
		{"no public error", context.Background(), errors.New("synthetic dial refused"), apperror.CodeInternal, apperror.FaultServer},
		{"client fault without a public error", context.Background(), status.Error(grpccodes.InvalidArgument, "synthetic payload"), apperror.CodeHTTPBadRequest, apperror.FaultClient},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			frame, rendered := NewStreamError(tc.ctx, tc.err, "req-1")
			definition, _ := apperror.Lookup(tc.code)
			if frame.Type != "error" || frame.Code != string(tc.code) || frame.Fault != tc.fault || frame.RequestID != "req-1" {
				t.Fatalf("frame = %+v, want %s %s", frame, tc.code, tc.fault)
			}
			if frame.Detail != definition.Detail || frame.Message != definition.Detail || frame.Args == nil {
				t.Fatalf("frame copy = %+v", frame)
			}
			if _, ok := frame.Args["secret"]; ok {
				t.Fatalf("undeclared arg on the frame: %v", frame.Args)
			}
			// The rendered error is what the result record attributes: an
			// *echo.HTTPError is recorded as the framework code it was answered with.
			if public, _ := errs.Answer(tc.ctx, rendered); apperror.CodeOf(public) != tc.code {
				t.Fatalf("rendered error answers %s, want %s", apperror.CodeOf(public), tc.code)
			}
		})
	}
}
