package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"
	"go.opentelemetry.io/otel/trace"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/errlog"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/httpx"
)

// resultErrorKey holds the error a request was answered with, so that the
// result record attributes the same error the response was rendered from.
const resultErrorKey = "memoh.result_error"

// NewHTTPErrorHandler renders every error a request ends with as a Problem.
// It does not log the error: the access log writes the one result record for
// the request. An HTTP shell installs it together with AccessLog.
func NewHTTPErrorHandler(log *slog.Logger) echo.HTTPErrorHandler {
	return func(err error, c echo.Context) {
		if c.Response().Committed {
			return
		}
		ctx := c.Request().Context()
		err = transportError(err)
		public, fault := errs.Answer(ctx, err)
		c.Set(resultErrorKey, err)
		problem, _ := ProblemFrom(public, httpx.RequestID(c))
		problem.Fault = fault
		problem.TraceID = traceID(ctx)
		writeProblem(ctx, log, c, problem)
	}
}

// transportError is err as a boundary answers and records it: an
// *echo.HTTPError, which the transport produces itself, is the framework code
// for its status with err as the cause; any other error is unchanged. The
// cause keeps a cancellation on the chain, so a canceled request is still
// answered canceled.
func transportError(err error) error {
	var httpErr *echo.HTTPError
	if errors.As(err, &httpErr) {
		return apperror.Wrap(frameworkCode(httpErr.Code), err, nil)
	}
	return err
}

// frameworkCodes are the codes for the statuses Echo and the handlers answer
// with an *echo.HTTPError.
var frameworkCodes = map[int]apperror.Code{
	http.StatusBadRequest:            apperror.CodeHTTPBadRequest,
	http.StatusUnauthorized:          apperror.CodeHTTPUnauthorized,
	http.StatusForbidden:             apperror.CodeHTTPForbidden,
	http.StatusNotFound:              apperror.CodeHTTPNotFound,
	http.StatusMethodNotAllowed:      apperror.CodeHTTPMethodNotAllowed,
	http.StatusConflict:              apperror.CodeHTTPConflict,
	http.StatusRequestEntityTooLarge: apperror.CodeHTTPPayloadTooLarge,
	http.StatusUnsupportedMediaType:  apperror.CodeHTTPUnsupportedMediaType,
	http.StatusUpgradeRequired:       apperror.CodeHTTPUpgradeRequired,
	http.StatusTooManyRequests:       apperror.CodeHTTPTooManyRequests,
	http.StatusNotImplemented:        apperror.CodeHTTPNotImplemented,
	http.StatusBadGateway:            apperror.CodeHTTPBadGateway,
	http.StatusServiceUnavailable:    apperror.CodeHTTPServiceUnavailable,
	http.StatusGatewayTimeout:        apperror.CodeHTTPGatewayTimeout,
}

// frameworkCode maps a status to its framework code. A client status without
// one is answered as a bad request, and a server status without one as
// internal, so the class of the status, and with it the fault, is kept.
func frameworkCode(status int) apperror.Code {
	if code, ok := frameworkCodes[status]; ok {
		return code
	}
	if status >= http.StatusBadRequest && status < http.StatusInternalServerError {
		return apperror.CodeHTTPBadRequest
	}
	return apperror.CodeInternal
}

// traceID is the id of the trace the request belongs to, or empty when the
// request is not traced.
func traceID(ctx context.Context) string {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.HasTraceID() {
		return ""
	}
	return sc.TraceID().String()
}

func writeProblem(ctx context.Context, log *slog.Logger, c echo.Context, problem Problem) {
	response := c.Response()
	response.Header().Set(echo.HeaderContentType, "application/problem+json")
	response.Header().Set("Content-Language", "en")
	response.WriteHeader(problem.Status)
	if c.Request().Method == http.MethodHead {
		return
	}
	if err := json.NewEncoder(response).Encode(problem); err != nil {
		logWriteFailure(ctx, log, err)
	}
}

// logWriteFailure records a response body that could not be written. The
// status is already sent and the request's result record follows, so this is
// an event rather than a second result.
func logWriteFailure(ctx context.Context, log *slog.Logger, err error) {
	result := errlog.Event(ctx, "http.write_response", errs.Wrap(err, "write error response"), errlog.Options{})
	log.LogAttrs(ctx, result.Level, "write error response failed", result.Attrs()...)
}
