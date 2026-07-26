// Package logging provides bounded, privacy-conscious log previews for channel adapters.
package logging

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
