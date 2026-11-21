package client

import (
	"errors"
	"strings"

	acp "github.com/coder/acp-go-sdk"
)

// AgentError returns the JSON-RPC error the Agent answered with, when err
// carries one, and what the Agent said in it. Agents put the generic JSON-RPC
// text in message and the real reason in data.message, so that one wins.
func AgentError(err error) (*acp.RequestError, string, bool) {
	var reqErr *acp.RequestError
	if !errors.As(err, &reqErr) {
		return nil, "", false
	}
	text := strings.TrimSpace(reqErr.Message)
	switch data := reqErr.Data.(type) {
	case string:
		text = firstText(data, text)
	case map[string]any:
		detail, _ := data["message"].(string)
		text = firstText(detail, text)
	}
	return reqErr, text, true
}

func firstText(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
