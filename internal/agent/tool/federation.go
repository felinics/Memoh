package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"sync"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/toolexec"
	"github.com/felinics/memoh/internal/mcp"
)

// FederationProvider adapts a mcp.ToolSource (federated MCP connections)
// into the ToolProvider interface so the agent can load external MCP tools
// alongside built-in tools.
type FederationProvider struct {
	source mcp.ToolSource
	logger *slog.Logger
	// onToolsetChange, when set, is invoked once per observed toolset change
	// of a session (spec: 工具集变了记 toolset-change 翻篇). lastHashes holds
	// the last successfully BOOKED toolset hash per (bot, session): when the
	// hook fails, the old baseline stays so the next turn retries the booking
	// instead of silently absorbing the change. The map is process-local, so
	// a restart re-baselines silently and relies on the prefix-hash drift
	// alarm as the backstop.
	onToolsetChange func(ctx context.Context, sessionID string) error
	mu              sync.Mutex
	lastHashes      map[string]string
}

// FederationProviderOption customizes a FederationProvider.
type FederationProviderOption func(*FederationProvider)

// WithToolsetChangeHook registers a callback fired when a session's
// federated toolset changes between turns. The callback must be cheap and
// must not fail the turn; wiring it to a Context Epoch advance books the
// change. A callback error keeps the old baseline so the booking retries on
// the next turn.
func WithToolsetChangeHook(hook func(ctx context.Context, sessionID string) error) FederationProviderOption {
	return func(f *FederationProvider) {
		f.onToolsetChange = hook
		if hook != nil {
			f.lastHashes = map[string]string{}
		}
	}
}

func NewFederationProvider(log *slog.Logger, source mcp.ToolSource, opts ...FederationProviderOption) *FederationProvider {
	if log == nil {
		log = slog.Default()
	}
	provider := &FederationProvider{
		source: source,
		logger: log.With(slog.String("tool", "federation")),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(provider)
		}
	}
	return provider
}

func (*FederationProvider) ProviderLabel() string { return "mcp" }

// ToolNotice relays the source's own explanation of missing tools, so a
// failed listing reaches the model instead of looking like an empty source.
func (f *FederationProvider) ToolNotice(ctx context.Context, session SessionContext) string {
	diagnostics, ok := f.source.(mcp.ToolSourceDiagnostics)
	if !ok {
		return ""
	}
	lines := diagnostics.ToolDiagnostics(ctx, toMCPSession(session))
	kept := lines[:0]
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n\n")
}

func (f *FederationProvider) Tools(ctx context.Context, session SessionContext) ([]toolexec.Tool, error) {
	if f.source == nil {
		return nil, nil
	}
	mcpSession := toMCPSession(session)
	descriptors, err := f.source.ListTools(ctx, mcpSession)
	if err != nil {
		// Not fatal for the turn: the agent keeps its other tools. The gap is
		// reported to the model through ToolNotice instead of being hidden.
		// Sources may still return a best-effort listing alongside the error
		// (e.g. the federation source's per-connection last-good snapshot);
		// those tools remain usable, only the uncovered gap is reported.
		f.logger.WarnContext(ctx, "federation list tools degraded", slog.Any("error", err))
		if len(descriptors) == 0 {
			return nil, nil
		}
		// A degraded listing is never tracked for toolset changes: the gap is
		// an outage, not a toolset change, and fallback-filled bytes are
		// identical to the last successful listing anyway. Booking here would
		// flap the epoch on every failure/recovery cycle.
	} else {
		f.trackToolsetChange(ctx, mcpSession, descriptors)
	}
	tools := make([]toolexec.Tool, 0, len(descriptors))
	for _, desc := range descriptors {
		name := strings.TrimSpace(desc.Name)
		if name == "" || IsBuiltInToolName(name) {
			continue
		}
		desc := desc
		src := f.source
		sess := mcpSession
		schema, err := toolexec.ResolveSchema(desc.InputSchema)
		if err != nil {
			// A tool advertised without its parameters cannot be called
			// correctly; leave it out of this turn rather than mislead the model.
			f.logger.Warn("federation tool schema is not usable; tool skipped", slog.String("tool", desc.Name), slog.Any("error", err))
			continue
		}
		tools = append(tools, toolexec.Tool{
			Name:        desc.Name,
			Description: desc.Description,
			Parameters:  schema,
			Execute: func(ctx *toolexec.ToolExecContext, input sdk.ToolArguments) (sdk.ToolOutput, error) {
				args := inputAsMap(input)
				result, err := src.CallTool(ctx.Context, sess, desc.Name, args)
				if err != nil {
					return sdk.ToolOutput{}, err
				}
				return toolexec.OutputFromValue(normalizeMCPResult(result)), nil
			},
		})
	}
	return tools, nil
}

// trackToolsetChange compares the session's federated toolset with the last
// one observed this process and fires the change hook on a mismatch. It runs
// only on successful listings — a failed refresh (tools served from the
// source's fallback or dropped) must never book an advance, because the
// rendered tool documentation did not change.
func (f *FederationProvider) trackToolsetChange(ctx context.Context, session mcp.ToolSessionContext, descriptors []mcp.ToolDescriptor) {
	if f.onToolsetChange == nil {
		return
	}
	sessionID := strings.TrimSpace(session.SessionID)
	if sessionID == "" {
		return
	}
	hash := toolsetHash(descriptors)
	key := strings.TrimSpace(session.BotID) + "|" + sessionID
	f.mu.Lock()
	previous, seen := f.lastHashes[key]
	f.mu.Unlock()
	if !seen {
		// The first observation only establishes the baseline.
		f.mu.Lock()
		f.lastHashes[key] = hash
		f.mu.Unlock()
		return
	}
	if previous == hash {
		return
	}
	// A change must be booked before it becomes the baseline: only a
	// successful hook moves the fingerprint, so a failed booking retries on
	// the next turn.
	if err := f.onToolsetChange(ctx, sessionID); err != nil {
		f.logger.WarnContext(ctx, "toolset change booking failed; will retry", slog.String("session_id", sessionID), slog.Any("error", err))
		return
	}
	f.mu.Lock()
	f.lastHashes[key] = hash
	f.mu.Unlock()
}

// toolsetHash fingerprints the advertised toolset: names, descriptions, and
// input schemas. json.Marshal sorts map keys, so equal toolsets hash equal
// regardless of object key order.
func toolsetHash(descriptors []mcp.ToolDescriptor) string {
	type toolPrint struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Schema      any    `json:"schema,omitempty"`
	}
	prints := make([]toolPrint, 0, len(descriptors))
	for _, desc := range descriptors {
		prints = append(prints, toolPrint{Name: strings.TrimSpace(desc.Name), Description: desc.Description, Schema: desc.InputSchema})
	}
	sort.Slice(prints, func(i, j int) bool { return prints[i].Name < prints[j].Name })
	raw, err := json.Marshal(prints)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func normalizeMCPResult(result map[string]any) any {
	if result == nil {
		return map[string]any{"ok": true}
	}
	if isErr, ok := result["isError"].(bool); ok && isErr {
		if structured, ok := result["structuredContent"].(map[string]any); ok &&
			structured["error"] == "reauth_required" {
			// Keep the actionable Connect-It signal at the top level so the
			// agent can tell the user to reauthorize the bot connector.
			return structured
		}
		return result
	}
	if sc, ok := result["structuredContent"]; ok && sc != nil {
		return sc
	}
	if content, ok := result["content"]; ok {
		if items, ok := content.([]map[string]any); ok && len(items) == 1 {
			if text, ok := items[0]["text"].(string); ok {
				var parsed any
				if json.Unmarshal([]byte(text), &parsed) == nil {
					return parsed
				}
				return text
			}
		}
	}
	return result
}
