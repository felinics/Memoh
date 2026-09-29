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
		answer := answerFor(ctx, err)
		c.Set(resultErrorKey, answer.err)
		problem, _ := apperror.ProblemFrom(answer.public, httpx.RequestID(c))
		problem.Fault = string(answer.fault)
		problem.TraceID = traceID(ctx)
		writeProblem(ctx, log, c, problem)
	}
}

// PublicError is the public error a transport answers err with, and the error
// its result record attributes, chosen by the rule the HTTP error handler
// applies. A WebSocket handler renders its error frames from it, so a request
// is answered with the same code whichever transport carried it.
func PublicError(ctx context.Context, err error) (public *apperror.Error, recorded error) {
	answer := answerFor(ctx, err)
	return answer.public, answer.err
}

// answer is what the boundary makes of the error a request ended with.
type answer struct {
	// err is the error the result record attributes. It differs from the
	// returned error only for an *echo.HTTPError, which is attributed as the
	// framework code it is answered with.
	err error
	// public is the catalog error the Problem is rendered from.
	public *apperror.Error
	fault  errs.Fault
}

// answerFor chooses the public error for err: a cancellation by the caller
// is canceled; an *echo.HTTPError is the framework code for its status; a
// public error in the chain is answered as it is; anything else, including a
// remote failure attributed to this process, is internal.
func answerFor(ctx context.Context, err error) answer {
	report := errs.Analyze(ctx, err)
	if report.Fault == errs.FaultCanceled {
		return answer{err: err, public: apperror.Wrap(apperror.CodeCanceled, err, nil), fault: report.Fault}
	}
	var httpErr *echo.HTTPError
	if errors.As(err, &httpErr) {
		public := apperror.Wrap(frameworkCode(httpErr.Code), err, nil)
		return answer{err: public, public: public, fault: errs.Analyze(ctx, public).Fault}
	}
	var public *apperror.Error
	if report.Public != nil && errors.As(report.Public.Err, &public) {
		return answer{err: err, public: public, fault: report.Fault}
	}
	return answer{err: err, public: apperror.Wrap(apperror.CodeInternal, err, nil), fault: report.Fault}
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

func writeProblem(ctx context.Context, log *slog.Logger, c echo.Context, problem apperror.Problem) {
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
