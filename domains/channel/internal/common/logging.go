// Package common provides shared utilities for channel adapters.
package common

import (
	"strings"

	"github.com/memohai/memoh/internal/textutil"
)

// SummarizeText returns a truncated preview of the text, limited to 120 characters.
func SummarizeText(text string) string {
	value := strings.TrimSpace(text)
	if value == "" {
		return ""
	}
	const limit = 120
	return textutil.TruncateRunesWithSuffix(value, limit, "...")
}
