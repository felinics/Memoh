package http

import (
	"os"
	"strings"
)

// ListenAddr is the HTTP server bind address after optional env override.
type ListenAddr string

// ResolveListenAddr returns the configured listen address, overridden by
// HTTP_ADDR when that environment variable is set.
func ResolveListenAddr(configured string) ListenAddr {
	if value := strings.TrimSpace(os.Getenv("HTTP_ADDR")); value != "" {
		return ListenAddr(value)
	}
	return ListenAddr(configured)
}

func (a ListenAddr) String() string { return string(a) }
