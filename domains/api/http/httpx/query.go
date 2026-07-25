package httpx

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
)

// ParseInt32Query parses an optional int32 query parameter.
func ParseInt32Query(raw string, defaultValue int32) (int32, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultValue, nil
	}
	parsed, err := strconv.ParseInt(raw, 10, 32)
	if err != nil {
		return 0, echo.NewHTTPError(http.StatusBadRequest, "invalid integer query parameter")
	}
	value := int32(parsed)
	if value < 0 {
		return 0, nil
	}
	return value, nil
}
