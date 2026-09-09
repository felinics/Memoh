package workspacedeps

import (
	"context"
	"regexp"
	"strings"
	"unicode"
)

var (
	dependencyErrorCredentials = regexp.MustCompile(`(?i)(authorization[=: ]+(?:bearer[ ]+)?|(?:[a-z0-9_]*(?:token|secret|password|passwd|api_key|apikey|credential)[a-z0-9_]*)[=:][ ]*)[^\s,;]+`)
	dependencyURLCredentials   = regexp.MustCompile(`(https?://)[^/@\s]+:[^/@\s]+@`)
)

// SafeErrorDetail exposes bounded diagnostics on Manage-only endpoints. Strip
// terminal controls and common credential forms even for older persisted rows.
func SafeErrorDetail(message string) string {
	message = dependencyErrorCredentials.ReplaceAllString(message, "${1}[redacted]")
	message = dependencyURLCredentials.ReplaceAllString(message, "${1}[redacted]@")
	message = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, message)
	return truncateMessage(message)
}

func (s *Service) errorDetail(ctx context.Context, message string) string {
	if s.scriptEnv != nil {
		for _, entry := range s.scriptEnv(ctx) {
			key, value, ok := strings.Cut(entry, "=")
			if ok && value != "" && isSecretEnvKey(key) {
				message = strings.ReplaceAll(message, value, "[redacted]")
			}
		}
	}
	return SafeErrorDetail(message)
}
