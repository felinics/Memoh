package assembly

import (
	"log/slog"

	mcphealth "github.com/memohai/memoh/domains/agent/internal/health/mcp"
	"github.com/memohai/memoh/internal/healthcheck"
)

// NewHealthChecker constructs the MCP connection health checker.
// cmd must call this constructor rather than importing owner-private health.
func NewHealthChecker(
	log *slog.Logger,
	connections mcphealth.ConnectionLister,
	tools mcphealth.ToolLister,
) healthcheck.Checker {
	return mcphealth.NewChecker(log, connections, tools)
}
