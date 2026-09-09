package mcpchecker

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/healthcheck"
	"github.com/felinics/memoh/internal/mcp"
)

const (
	checkTypeMCPConnection  = "mcp.connection"
	titleKeyMCPConnection   = "bots.checks.titles.mcpConnection"
	defaultCheckTimeout     = 8 * time.Second
	fallbackConnectionLabel = "MCP"
)

// ConnectionLister lists active MCP connections for a bot.
type ConnectionLister interface {
	ListActiveByBot(ctx context.Context, botID string) ([]mcp.Connection, error)
}

// ToolLister lists tools for a bot session.
type ToolLister interface {
	ListTools(ctx context.Context, session mcp.ToolSessionContext) ([]mcp.ToolDescriptor, error)
}

// Checker evaluates MCP connection health checks.
type Checker struct {
	logger      *slog.Logger
	connections ConnectionLister
	tools       ToolLister
	timeout     time.Duration
}

// NewChecker creates an MCP health checker.
func NewChecker(log *slog.Logger, connections ConnectionLister, tools ToolLister) *Checker {
	if log == nil {
		log = slog.Default()
	}
	return &Checker{
		logger:      log.With(slog.String("checker", "healthcheck_mcp")),
		connections: connections,
		tools:       tools,
		timeout:     defaultCheckTimeout,
	}
}

// ListChecks evaluates all active MCP connections for a bot.
func (c *Checker) ListChecks(ctx context.Context, botID string) []healthcheck.CheckResult {
	botID = strings.TrimSpace(botID)
	if botID == "" {
		return []healthcheck.CheckResult{}
	}
	if c.connections == nil || c.tools == nil {
		if c.logger != nil {
			c.logger.Warn(
				"mcp healthcheck dependencies are unavailable",
				slog.String("bot_id", botID),
				slog.Bool("has_connection_lister", c.connections != nil),
				slog.Bool("has_tool_lister", c.tools != nil),
			)
		}
		return []healthcheck.CheckResult{
			{
				ID:       checkTypeMCPConnection + ".service",
				Type:     checkTypeMCPConnection,
				TitleKey: titleKeyMCPConnection,
				Status:   healthcheck.StatusWarn,
				Summary:  "MCP checker service is not available.",
				Detail:   "connection lister or tool lister is nil",
			},
		}
	}

	items, err := c.connections.ListActiveByBot(ctx, botID)
	if err != nil {
		if c.logger != nil {
			c.logger.Warn(
				"mcp healthcheck list connections failed",
				slog.String("bot_id", botID),
				slog.Any("error", err),
			)
		}
		return []healthcheck.CheckResult{
			{
				ID:       checkTypeMCPConnection + ".list",
				Type:     checkTypeMCPConnection,
				TitleKey: titleKeyMCPConnection,
				Status:   healthcheck.StatusError,
				Summary:  "Failed to list MCP connections.",
				Detail:   err.Error(),
			},
		}
	}
	if len(items) == 0 {
		return []healthcheck.CheckResult{}
	}

	sort.Slice(items, func(i, j int) bool {
		leftName := strings.TrimSpace(items[i].Name)
		rightName := strings.TrimSpace(items[j].Name)
		if leftName == rightName {
			return strings.TrimSpace(items[i].ID) < strings.TrimSpace(items[j].ID)
		}
		return leftName < rightName
	})

	probeCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	tools, err := c.tools.ListTools(probeCtx, mcp.ToolSessionContext{BotID: botID})
	if err != nil {
		if c.logger != nil {
			c.logger.Warn(
				"mcp healthcheck list tools failed",
				slog.String("bot_id", botID),
				slog.Any("error", err),
			)
		}
		checks := make([]healthcheck.CheckResult, 0, len(items))
		for idx, conn := range items {
			checks = append(checks, healthcheck.CheckResult{
				ID:       buildCheckID(conn, idx),
				Type:     checkTypeMCPConnection,
				TitleKey: titleKeyMCPConnection,
				Subtitle: displayConnectionName(conn.Name),
				Status:   healthcheck.StatusError,
				Summary:  fmt.Sprintf("MCP server %q is not reachable.", displayConnectionName(conn.Name)),
				Detail:   err.Error(),
				Metadata: map[string]any{
					"connection_id": strings.TrimSpace(conn.ID),
					"name":          strings.TrimSpace(conn.Name),
					"type":          strings.TrimSpace(conn.Type),
				},
			})
		}
		return checks
	}

	results := make([]healthcheck.CheckResult, 0, len(items))
	for idx, conn := range items {
		connName := displayConnectionName(conn.Name)
		prefix := sanitizeToolPrefix(conn.Name)
		toolCount := 0
		if prefix != "" {
			toolPrefix := prefix + "_"
			for _, tool := range tools {
				if strings.HasPrefix(strings.TrimSpace(tool.Name), toolPrefix) {
					toolCount++
				}
			}
		}

		item := healthcheck.CheckResult{
			ID:       buildCheckID(conn, idx),
			Type:     checkTypeMCPConnection,
			TitleKey: titleKeyMCPConnection,
			Subtitle: connName,
			Status:   healthcheck.StatusWarn,
			Summary:  fmt.Sprintf("MCP server %q is reachable but no tools found.", connName),
			Detail:   "The server responded but exposed no tools for this connection.",
			Metadata: map[string]any{
				"connection_id": strings.TrimSpace(conn.ID),
				"name":          strings.TrimSpace(conn.Name),
				"type":          strings.TrimSpace(conn.Type),
				"tool_count":    toolCount,
			},
		}
		if toolCount > 0 {
			item.Status = healthcheck.StatusOK
			item.Summary = fmt.Sprintf("MCP server %q is healthy (%d tools).", connName, toolCount)
			item.Detail = ""
		}
		results = append(results, item)
	}
	return results
}

func buildCheckID(conn mcp.Connection, idx int) string {
	connectionID := strings.TrimSpace(conn.ID)
	if connectionID != "" {
		return checkTypeMCPConnection + "." + connectionID
	}
	return checkTypeMCPConnection + ".unknown_" + strconv.Itoa(idx+1)
}

func displayConnectionName(raw string) string {
	name := strings.TrimSpace(raw)
	if name == "" {
		return fallbackConnectionLabel
	}
	return name
}

func sanitizeToolPrefix(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return "mcp"
	}
	builder := strings.Builder{}
	for _, ch := range raw {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '_' || ch == '-' {
			builder.WriteRune(ch)
			continue
		}
		builder.WriteRune('_')
	}
	normalized := strings.Trim(builder.String(), "._-")
	if normalized == "" {
		return "mcp"
	}
	return normalized
}
