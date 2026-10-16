package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/i18n"
	"github.com/felinics/memoh/internal/rpc"
	"github.com/felinics/memoh/internal/server"
)

// The three cases where the error on the chain is not the answer are answered
// alike by every exit: an HTTP Problem, a WebSocket error frame and an IM
// reply all come from errs.Answer.
func TestEveryExitAnswersAlike(t *testing.T) {
	refusal, err := status.New(codes.PermissionDenied, "agent not enabled").WithDetails(&errdetails.ErrorInfo{
		Reason:   string(apperror.CodeACPAgentNotEnabled),
		Metadata: map[string]string{rpc.MetadataFault: string(apperror.FaultClient)},
	})
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name  string
		ctx   context.Context
		err   error
		code  apperror.Code
		fault apperror.Fault
	}{
		{"remote refusal not forwarded", context.Background(), fmt.Errorf("call runtime: %w", rpc.Decode(refusal.Err(), nil, nil)), apperror.CodeInternal, apperror.FaultServer},
		{"forwarded remote refusal", context.Background(), rpc.Forward(rpc.Decode(refusal.Err(), nil, nil)), apperror.CodeACPAgentNotEnabled, apperror.FaultClient},
		{"caller canceled", canceled, fmt.Errorf("stream turn: %w", context.Canceled), apperror.CodeCanceled, apperror.FaultCanceled},
		{"code outside the catalog", context.Background(), apperror.Wrap("synthetic.unregistered", errors.New("synthetic cause"), nil), apperror.CodeInternal, apperror.FaultServer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// HTTP
			e := echo.New()
			req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(tc.ctx)
			rec := httptest.NewRecorder()
			server.NewHTTPErrorHandler(slog.New(slog.DiscardHandler))(tc.err, e.NewContext(req, rec))
			var problem server.Problem
			if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
				t.Fatalf("decode problem: %v: %s", err, rec.Body.String())
			}
			if problem.Code != string(tc.code) || problem.Fault != tc.fault {
				t.Errorf("HTTP = %s %s, want %s %s", problem.Code, problem.Fault, tc.code, tc.fault)
			}

			// WebSocket
			frame := decodeWSTestEvent(t, func(w *wsWriter) {
				_ = sendWSErrorFromError(tc.ctx, w, wsTurn("invocation-1", "session-1"), tc.err)
			})
			if frame["code"] != string(tc.code) || frame["fault"] != string(tc.fault) {
				t.Errorf("WS = %v %v, want %s %s", frame["code"], frame["fault"], tc.code, tc.fault)
			}

			// IM: a canceled caller gets no reply.
			event, replied := channel.ErrorEvent(tc.ctx, i18n.New("en"), tc.err)
			if replied == (tc.fault == apperror.FaultCanceled) || (replied && event.ErrorCode != string(tc.code)) {
				t.Errorf("IM = %v %q, want %s", replied, event.ErrorCode, tc.code)
			}
		})
	}
}
