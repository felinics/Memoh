package server

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/httpx"
)

func newHTTPErrorHandler(log *slog.Logger, fallback echo.HTTPErrorHandler) echo.HTTPErrorHandler {
	return func(err error, c echo.Context) {
		if c.Response().Committed {
			return
		}

		problem, ok := apperror.ProblemFrom(err, httpx.RequestID(c))
		if !ok {
			fallback(err, c)
			return
		}

		if problem.Status >= http.StatusInternalServerError {
			attrs := []any{
				slog.String("code", problem.Code),
				slog.String("request_id", problem.RequestID),
			}
			if cause := apperror.CauseOf(err); cause != nil {
				attrs = append(attrs, slog.Any("error", cause))
			}
			log.Error("request failed", attrs...)
		}

		response := c.Response()
		response.Header().Set(echo.HeaderContentType, "application/problem+json")
		response.Header().Set("Content-Language", "en")
		response.WriteHeader(problem.Status)
		if encodeErr := json.NewEncoder(response).Encode(problem); encodeErr != nil {
			log.Error("write problem response failed",
				slog.String("code", problem.Code),
				slog.String("request_id", problem.RequestID),
				slog.Any("error", encodeErr),
			)
		}
	}
}
