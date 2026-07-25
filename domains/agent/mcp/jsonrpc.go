package mcp

import (
	"encoding/json"
	"strings"
)

func IsNotification(req JSONRPCRequest) bool {
	return len(req.ID) == 0 && strings.HasPrefix(req.Method, "notifications/")
}

func JSONRPCErrorResponse(id json.RawMessage, code int, message string) JSONRPCResponse {
	return JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &JSONRPCError{Code: code, Message: message},
	}
}
