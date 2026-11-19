package models

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	sdk "github.com/felinics/twilight/sdk"
)

const maxUpstreamSummaryBytes = 2000

// UpstreamErrorSummary extracts a length-bounded verbatim diagnostic summary
// for user-facing error details in self-hosted Memoh. Upstream *sdk.APIError
// responses (including authentication errors) are formatted from their status,
// code/type, message (with UTF-8 body fallback when Message is empty), and
// provider request ID; synthetic *sdk.APIError values with no diagnostic fields
// return an empty string. Transport and Memoh internal errors return err.Error().
func UpstreamErrorSummary(err error) string {
	if err == nil {
		return ""
	}
	var apiErr *sdk.APIError
	if errors.As(err, &apiErr) && apiErr != nil {
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
		return boundUpstreamSummary(summary)
	}
	return boundUpstreamSummary(err.Error())
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

func boundUpstreamSummary(text string) string {
	bounded := strings.TrimSpace(text)
	if len(bounded) > maxUpstreamSummaryBytes {
		bounded = strings.ToValidUTF8(bounded[:maxUpstreamSummaryBytes], "") + "…"
	}
	return bounded
}
