package system

import (
	"github.com/labstack/echo/v4"

	httpx "github.com/memohai/memoh/domains/api/http/httpx"
)

// ErrorResponse keeps swagger annotations in this package stable.
type ErrorResponse = httpx.ErrorResponse

func newI18nHTTPError(status int, code, i18nKey, message string) *echo.HTTPError {
	return httpx.NewI18nHTTPError(status, code, i18nKey, message)
}
