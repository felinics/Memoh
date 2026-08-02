package mcp

import "strings"

// RedactedHeaderValue is returned by management APIs in place of sensitive
// connection header values. Update restores this sentinel from the stored
// connection so opening and saving the form cannot erase credentials.
const RedactedHeaderValue = "__MEMOH_REDACTED__"

// RedactConnectionSecrets returns a detached copy safe for ordinary management
// responses. Explicit export remains the credential-transfer boundary and uses
// the stored connection directly.
func RedactConnectionSecrets(connection Connection) Connection {
	connection.Config = cloneConnectionConfig(connection.Config)
	switch headers := connection.Config["headers"].(type) {
	case map[string]any:
		for name := range headers {
			if isSensitiveHeader(name) {
				headers[name] = RedactedHeaderValue
			}
		}
	case map[string]string:
		for name := range headers {
			if isSensitiveHeader(name) {
				headers[name] = RedactedHeaderValue
			}
		}
	}
	return connection
}

func RedactConnectionList(items []Connection) []Connection {
	redacted := make([]Connection, len(items))
	for index := range items {
		redacted[index] = RedactConnectionSecrets(items[index])
	}
	return redacted
}

func cloneConnectionConfig(config map[string]any) map[string]any {
	if config == nil {
		return nil
	}
	cloned := make(map[string]any, len(config))
	for key, value := range config {
		if headers, ok := value.(map[string]any); ok && key == "headers" {
			headerCopy := make(map[string]any, len(headers))
			for name, headerValue := range headers {
				headerCopy[name] = headerValue
			}
			cloned[key] = headerCopy
			continue
		}
		if headers, ok := value.(map[string]string); ok && key == "headers" {
			headerCopy := make(map[string]string, len(headers))
			for name, headerValue := range headers {
				headerCopy[name] = headerValue
			}
			cloned[key] = headerCopy
			continue
		}
		cloned[key] = value
	}
	return cloned
}

func isSensitiveHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "authorization", "proxy-authorization", "x-telegram-bot-token":
		return true
	default:
		return false
	}
}

func hasRedactedHeader(headers map[string]string) bool {
	for _, value := range headers {
		if value == RedactedHeaderValue {
			return true
		}
	}
	return false
}

func restoreRedactedHeaders(headers map[string]string, existing Connection) (map[string]string, error) {
	if !hasRedactedHeader(headers) {
		return headers, nil
	}
	stored := map[string]string{}
	switch raw := existing.Config["headers"].(type) {
	case map[string]any:
		for name, value := range raw {
			if text, ok := value.(string); ok {
				stored[strings.ToLower(strings.TrimSpace(name))] = text
			}
		}
	case map[string]string:
		for name, value := range raw {
			stored[strings.ToLower(strings.TrimSpace(name))] = value
		}
	}
	restored := make(map[string]string, len(headers))
	for name, value := range headers {
		if value != RedactedHeaderValue {
			restored[name] = value
			continue
		}
		value, ok := stored[strings.ToLower(strings.TrimSpace(name))]
		if !ok {
			return nil, &redactedHeaderRestoreError{name: name}
		}
		restored[name] = value
	}
	return restored, nil
}

type redactedHeaderRestoreError struct {
	name string
}

func (e *redactedHeaderRestoreError) Error() string {
	return "redacted MCP header cannot be restored: " + strings.TrimSpace(e.name)
}
