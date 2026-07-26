package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"

	httpx "github.com/memohai/memoh/domains/api/http"
	"github.com/memohai/memoh/internal/apperror"
)

// newHTTPErrorHandler renders every error this server produces. Echo's default
// handler is not kept as a fallback: a second renderer is a second wire format,
// and having two was the problem the contract set out to fix.
func newHTTPErrorHandler(log *slog.Logger) echo.HTTPErrorHandler {
	return func(err error, c echo.Context) {
		if c.Response().Committed {
			return
		}

		requestID := httpx.RequestID(c)
		problem, suppressed := renderable(err, requestID)

		if problem.Status >= http.StatusInternalServerError {
			attrs := []any{
				slog.String("kind", apperror.KindOf(err).String()),
				slog.String("request_id", problem.RequestID),
			}
			if problem.Code != "" {
				attrs = append(attrs, slog.String("code", problem.Code))
			}
			if op := apperror.OpOf(err); op != "" {
				attrs = append(attrs, slog.String("op", op))
			}
			// suppressed carries the message a legacy handler wanted to send.
			// It is the single most common leak vector in this codebase
			// (echo.NewHTTPError(500, err.Error())), so it goes to the log and
			// nowhere else.
			if suppressed != "" {
				attrs = append(attrs, slog.String("suppressed_message", suppressed))
			}
			if cause := apperror.CauseOf(err); cause != nil {
				attrs = append(attrs, slog.Any("error", cause))
			} else if suppressed == "" {
				attrs = append(attrs, slog.Any("error", err))
			}
			log.Error("request failed", attrs...)
		}

		response := c.Response()
		response.Header().Set(echo.HeaderContentType, "application/problem+json")
		response.Header().Set("Content-Language", "en")
		response.WriteHeader(problem.Status)
		if encodeErr := json.NewEncoder(response).Encode(problem); encodeErr != nil {
			log.Error("write problem response failed",
				slog.String("request_id", problem.RequestID),
				slog.Any("error", encodeErr),
			)
		}
	}
}

// renderable projects any error into a Problem, and returns the
// handler-supplied message that was withheld from it.
//
// Every response goes through the contract, including the echo errors raised
// for the request envelope (405, 413, 415, 426) that Kind deliberately does not
// model. Their status is carried through unchanged rather than derived from a
// Kind, because deriving it would silently turn a 413 into a 400. Their message
// is dropped: the status already says everything those refusals have to say,
// and free text on the wire is what the contract exists to remove.
func renderable(err error, requestID string) (problem apperror.Problem, suppressed string) {
	if _, isContract := apperror.As(err); isContract {
		return apperror.ProblemOf(err, requestID), ""
	}

	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) {
		// Echo renders a bare error as a generic 500. Routing it through the
		// contract instead gives it a request_id and a localizable body.
		return apperror.ProblemOf(err, requestID), ""
	}

	envelope := apperror.OfKind(apperror.KindFromHTTPStatus(httpErr.Code), "http envelope", httpErr.Internal)
	problem = apperror.ProblemOf(envelope, requestID)
	problem.Status = httpErr.Code
	return problem, messageText(httpErr.Message)
}

func messageText(message any) string {
	switch typed := message.(type) {
	case nil:
		return ""
	case string:
		return typed
	case error:
		return typed.Error()
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return ""
		}
		return string(encoded)
	}
}
