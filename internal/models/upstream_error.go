package models

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/errs"
)

const maxUpstreamSummaryBytes = 2000

var (
	bearerTokenPattern = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`)
	secretTokenPattern = regexp.MustCompile(`\b(sk-|key-|rk-|pk-|AIza|gh[pousr]_|xox[baprs]-)[A-Za-z0-9_-]{6,}`)
)

// UpstreamErrorSummary extracts a sanitized, bounded summary of an upstream
// *sdk.APIError for user-facing error diagnostics. Authentication responses
// (HTTP 401 or sdk.KindAuthentication), transport errors, and synthetic errors
// without diagnostic fields return an empty string.
func UpstreamErrorSummary(err error) string {
	var apiErr *sdk.APIError
	if !errors.As(err, &apiErr) || apiErr == nil {
		return ""
	}
	if apiErr.StatusCode == http.StatusUnauthorized || apiErr.Kind == sdk.KindAuthentication {
		return ""
	}

	label := upstreamErrorLabel(apiErr)
	msg := upstreamErrorMessage(apiErr)
	reqID := strings.TrimSpace(apiErr.RequestID)
	if msg == "" && label == "" && reqID == "" {
		return ""
	}

	summary := formatUpstreamHeader(apiErr.StatusCode, label, msg)
	if reqID != "" {
		if summary != "" {
			summary += "\nProvider Request ID: " + reqID
		} else {
			summary = "Provider Request ID: " + reqID
		}
	}
	return sanitizeUpstreamSummary(summary)
}

func upstreamErrorLabel(apiErr *sdk.APIError) string {
	if code := strings.TrimSpace(apiErr.Code); code != "" {
		return code
	}
	return strings.TrimSpace(apiErr.Type)
}

func upstreamErrorMessage(apiErr *sdk.APIError) string {
	if msg := strings.TrimSpace(apiErr.Message); msg != "" {
		return msg
	}
	if len(apiErr.Body) == 0 || !utf8.Valid(apiErr.Body) {
		return ""
	}
	body := strings.TrimSpace(string(apiErr.Body))
	if body == "" || body == "{}" || body == "[]" || body == "null" {
		return ""
	}
	return body
}

func formatUpstreamHeader(statusCode int, label, msg string) string {
	var prefix string
	switch {
	case statusCode > 0 && label != "":
		prefix = fmt.Sprintf("HTTP %d · %s", statusCode, label)
	case statusCode > 0:
		prefix = fmt.Sprintf("HTTP %d", statusCode)
	case label != "":
		prefix = label
	}
	switch {
	case prefix != "" && msg != "":
		return prefix + ": " + msg
	case msg != "":
		return msg
	default:
		return prefix
	}
}

func sanitizeUpstreamSummary(text string) string {
	sanitized := errs.RedactURLs(strings.TrimSpace(text))
	sanitized = bearerTokenPattern.ReplaceAllString(sanitized, "Bearer [redacted]")
	sanitized = secretTokenPattern.ReplaceAllString(sanitized, "${1}[redacted]")
	if len(sanitized) > maxUpstreamSummaryBytes {
		sanitized = strings.ToValidUTF8(sanitized[:maxUpstreamSummaryBytes], "") + "…"
	}
	return sanitized
}
