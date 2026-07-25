package httpx

import "github.com/labstack/echo/v4"

// ErrorResponse is the stable HTTP error body referenced by swagger annotations.
type ErrorResponse struct {
	Message    string            `json:"message"`
	Code       string            `json:"code,omitempty"`
	Reason     string            `json:"reason,omitempty"`
	HTTPStatus int               `json:"http_status,omitempty"`
	I18nKey    string            `json:"i18n_key,omitempty"`
	Args       map[string]string `json:"args,omitempty"`
}

// NewI18nHTTPError builds an echo HTTP error with a stable i18n-bearing body.
func NewI18nHTTPError(status int, code, i18nKey, message string) *echo.HTTPError {
	return echo.NewHTTPError(status, ErrorResponse{
		Message:    message,
		Code:       code,
		HTTPStatus: status,
		I18nKey:    i18nKey,
		Args:       map[string]string{},
	})
}
