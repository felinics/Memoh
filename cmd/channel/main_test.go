package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"go.uber.org/fx"
	"google.golang.org/grpc"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"
)

// TestFXOptionsValidate proves the channel boundary's dependency set is
// explicit and closed (spec §7.3 assembly-closure verification).
func TestFXOptionsValidate(t *testing.T) {
	if err := fx.ValidateApp(options()); err != nil {
		t.Fatal(err)
	}
}

func TestChannelReadinessRequiresServerStorageService(t *testing.T) {
	tests := []struct {
		name       string
		status     grpc_health_v1.HealthCheckResponse_ServingStatus
		err        error
		wantStatus int
	}{
		{name: "serving", status: grpc_health_v1.HealthCheckResponse_SERVING, wantStatus: http.StatusOK},
		{name: "service unknown", status: grpc_health_v1.HealthCheckResponse_SERVICE_UNKNOWN, wantStatus: http.StatusServiceUnavailable},
		{name: "rpc unavailable", err: errors.New("server unavailable"), wantStatus: http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := echo.New()
			(&healthHandler{serverHealth: fakeServerHealth{status: tt.status, err: tt.err}}).Register(e)
			recorder := httptest.NewRecorder()
			e.ServeHTTP(recorder, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/ready", nil))
			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, tt.wantStatus)
			}
		})
	}
}

func TestChannelLivenessDoesNotDependOnServer(t *testing.T) {
	e := echo.New()
	(&healthHandler{serverHealth: fakeServerHealth{err: errors.New("server unavailable")}}).Register(e)
	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/ping", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
}

type fakeServerHealth struct {
	status grpc_health_v1.HealthCheckResponse_ServingStatus
	err    error
}

func (f fakeServerHealth) Check(context.Context, *grpc_health_v1.HealthCheckRequest, ...grpc.CallOption) (*grpc_health_v1.HealthCheckResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &grpc_health_v1.HealthCheckResponse{Status: f.status}, nil
}

func (fakeServerHealth) List(context.Context, *grpc_health_v1.HealthListRequest, ...grpc.CallOption) (*grpc_health_v1.HealthListResponse, error) {
	return nil, errors.New("not implemented")
}

func (fakeServerHealth) Watch(context.Context, *grpc_health_v1.HealthCheckRequest, ...grpc.CallOption) (grpc.ServerStreamingClient[grpc_health_v1.HealthCheckResponse], error) {
	return nil, errors.New("not implemented")
}
