package http

import (
	"strconv"
	"strings"

	"github.com/memohai/memoh/internal/apperror"
)

// ParseInt32Query parses an optional int32 query parameter.
func ParseInt32Query(raw string, defaultValue int32) (int32, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultValue, nil
	}
	parsed, err := strconv.ParseInt(raw, 10, 32)
	if err != nil {
		return 0, apperror.Invalid("parse int32 query parameter", err)
	}
	value := int32(parsed)
	if value < 0 {
		return 0, nil
	}
	return value, nil
}
