package handlers

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	mcptools "github.com/felinics/memoh/internal/mcp"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

// MCPStdioRequest represents a request to create an MCP stdio session.
type MCPStdioRequest struct {
	Name    string            `json:"name"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	Cwd     string            `json:"cwd"`
}

// MCPStdioResponse represents the response from creating an MCP stdio session.
type MCPStdioResponse struct {
	ConnectionID string   `json:"connection_id"`
	URL          string   `json:"url"`
	Tools        []string `json:"tools,omitempty"`
}

// mcpStdioClient owns an SDK client session connected to an MCP process running
// inside the bot's workspace container. The go-sdk speaks the protocol (handshake,
// request correlation); this wrapper owns what the SDK cannot see: the process's
// stderr tail and exit code for error attribution, and teardown of the bridge
// exec stream.
//
// The process runs container-side, so the SDK's CommandTransport (local os/exec)
// is unusable here — the session rides on IOTransport over the bridge pipes.
type mcpStdioClient struct {
	session    *sdkmcp.ClientSession
	stderrTail *mcpStderrTail
	// exitCode is -1 until the bridge reports the process's EXIT frame.
	exitCode    atomic.Int32
	streamClose func()

	done      chan struct{}
	closeOnce sync.Once
	onClose   func()

	// inflight maps the EXTERNAL request ID of each in-flight dispatched call
	// to its cancel func, so notifications/cancelled from the proxy client can
	// reach it. Cancelling the call's ctx makes the SDK send the server a
	// spec-compliant cancelled notification carrying the INTERNAL call ID (the
	// SDK transport's call() does this on ctx cancel) — the raw-forward path
	// the pre-migration client used is gone because the SDK owns the transport.
	inflight sync.Map // string(external request ID) → context.CancelFunc
}

// Close shuts the SDK session (which closes the stdio pipes), the bridge exec
// stream, and fires onClose. Idempotent; also invoked by the Wait goroutine when
// the server side dies on its own.
func (c *mcpStdioClient) Close() {
	c.closeOnce.Do(func() {
		close(c.done)
		if c.session != nil {
			_ = c.session.Close()
		}
		if c.streamClose != nil {
			c.streamClose()
		}
		if c.onClose != nil {
			c.onClose()
		}
	})
}

// enrichError turns a bare transport failure (usually io.EOF from a dead
// process) into an actionable message: the exit code when the bridge reported
// one, plus the captured stderr tail. Without this, a container-side
// "command not found" surfaced to users as the single word "EOF".
func (c *mcpStdioClient) enrichError(err error) error {
	if err == nil {
		return nil
	}
	var b strings.Builder
	code := c.exitCode.Load()
	if code >= 0 {
		// Keep the original failure alongside the exit diagnostics: a real
		// protocol error that races the process death (server answers "tool not
		// found", then crashes) must not be swallowed by the exit code. io.EOF
		// itself adds nothing beyond "the process died", so it stays out.
		fmt.Fprintf(&b, "process exited with code %d", code) //nolint:gosec // G705: goes out as a JSON-RPC result via c.JSON — JSON-encoded, never HTML
		if !errors.Is(err, io.EOF) {
			b.WriteString(": ")
			b.WriteString(err.Error())
		}
	} else {
		b.WriteString(err.Error())
	}
	if tail := strings.TrimSpace(c.stderrTail.String()); tail != "" {
		b.WriteString(": ")
		b.WriteString(tail)
	}
	// No diagnostics captured → hand the original error back untouched so
	// errors.Is/As chains keep working.
	if b.String() == err.Error() {
		return err
	}
	return errors.New(b.String())
}

// errMCPMethodNotFound marks an unsupported method on the stdio proxy endpoint
// so the handler can answer -32601 (method not found) instead of -32603.
var errMCPMethodNotFound = errors.New("method not found")

// dispatch answers a raw JSON-RPC request from the external proxy endpoint using
// the typed SDK session. The go-sdk client offers no raw passthrough (its read
// loop owns the transport, so interleaving hand-correlated frames is unsafe),
// so the surface is a method table instead of arbitrary forwarding. It covers
// the FULL standard MCP client surface the SDK speaks — anything the replayed
// initialize can advertise (tools, prompts, resources, completion, logging,
// subscriptions) is callable here; -32601 is reserved for genuinely unknown
// (experimental/custom) methods.
func (c *mcpStdioClient) dispatch(ctx context.Context, req mcptools.JSONRPCRequest) (map[string]any, error) {
	switch strings.TrimSpace(req.Method) {
	case "ping":
		return c.sdkCall(ctx, req, nil, func(ctx context.Context) (any, error) {
			return map[string]any{}, c.session.Ping(ctx, &sdkmcp.PingParams{})
		})
	case "initialize":
		// The session already handshook at connect; replay the stored result.
		result := c.session.InitializeResult()
		if result == nil {
			return jsonrpcResultPayload(req.ID, map[string]any{}), nil
		}
		return jsonrpcResultPayload(req.ID, result), nil
	case "tools/list":
		params := &sdkmcp.ListToolsParams{}
		return c.sdkCall(ctx, req, params, func(ctx context.Context) (any, error) {
			return c.session.ListTools(ctx, params)
		})
	case "tools/call":
		params := &sdkmcp.CallToolParams{}
		return c.sdkCall(ctx, req, params, func(ctx context.Context) (any, error) {
			params.Name = strings.TrimSpace(params.Name)
			return c.session.CallTool(ctx, params)
		})
	case "prompts/list":
		params := &sdkmcp.ListPromptsParams{}
		return c.sdkCall(ctx, req, params, func(ctx context.Context) (any, error) {
			return c.session.ListPrompts(ctx, params)
		})
	case "prompts/get":
		params := &sdkmcp.GetPromptParams{}
		return c.sdkCall(ctx, req, params, func(ctx context.Context) (any, error) {
			return c.session.GetPrompt(ctx, params)
		})
	case "resources/list":
		params := &sdkmcp.ListResourcesParams{}
		return c.sdkCall(ctx, req, params, func(ctx context.Context) (any, error) {
			return c.session.ListResources(ctx, params)
		})
	case "resources/templates/list":
		params := &sdkmcp.ListResourceTemplatesParams{}
		return c.sdkCall(ctx, req, params, func(ctx context.Context) (any, error) {
			return c.session.ListResourceTemplates(ctx, params)
		})
	case "resources/read":
		params := &sdkmcp.ReadResourceParams{}
		return c.sdkCall(ctx, req, params, func(ctx context.Context) (any, error) {
			return c.session.ReadResource(ctx, params)
		})
	case "resources/subscribe":
		params := &sdkmcp.SubscribeParams{}
		return c.sdkCall(ctx, req, params, func(ctx context.Context) (any, error) {
			return map[string]any{}, c.session.Subscribe(ctx, params)
		})
	case "resources/unsubscribe":
		params := &sdkmcp.UnsubscribeParams{}
		return c.sdkCall(ctx, req, params, func(ctx context.Context) (any, error) {
			return map[string]any{}, c.session.Unsubscribe(ctx, params)
		})
	case "completion/complete":
		params := &sdkmcp.CompleteParams{}
		return c.sdkCall(ctx, req, params, func(ctx context.Context) (any, error) {
			return c.session.Complete(ctx, params)
		})
	case "logging/setLevel":
		params := &sdkmcp.SetLoggingLevelParams{} //nolint:staticcheck // SEP-2577: proxy must keep forwarding logging/setLevel for external clients during the deprecation window.
		return c.sdkCall(ctx, req, params, func(ctx context.Context) (any, error) {
			return map[string]any{}, c.session.SetLoggingLevel(ctx, params) //nolint:staticcheck // SEP-2577: passthrough, see above.
		})
	default:
		return nil, errMCPMethodNotFound
	}
}

// sdkCall runs one dispatch case: decode the raw JSON-RPC params into the typed
// SDK params (nil for parameterless methods), invoke, then wrap the result —
// or enrich the failure with process diagnostics (exit code + stderr tail).
func (c *mcpStdioClient) sdkCall(ctx context.Context, req mcptools.JSONRPCRequest, params any, invoke func(context.Context) (any, error)) (map[string]any, error) {
	if params != nil && len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, params); err != nil {
			return nil, fmt.Errorf("invalid %s params: %w", req.Method, err)
		}
	}
	// Register under the external ID BEFORE invoking: notifications/cancelled
	// arrives on a separate HTTP request while this call is still in flight.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if len(req.ID) > 0 {
		key := string(req.ID)
		c.inflight.Store(key, cancel)
		defer c.inflight.Delete(key)
	}
	result, err := invoke(ctx)
	if err != nil {
		// A JSON-RPC error from the downstream server must keep its code — the
		// proxy's callers distinguish invalid params (-32602), missing tools,
		// and server-defined -320xx failures by it. It also needs no exit
		// diagnostics: the server answered, so it is alive by definition.
		var wireErr *jsonrpc.Error
		if errors.As(err, &wireErr) {
			return nil, wireErr
		}
		return nil, c.enrichError(err)
	}
	if result == nil {
		result = map[string]any{}
	}
	return jsonrpcResultPayload(req.ID, result), nil
}

// cancelInFlight handles notifications/cancelled from the proxy client: the
// referenced external request ID resolves to an in-flight dispatched call and
// its ctx is cancelled (the SDK then notifies the server itself, with the
// internal call ID). Unknown or already-finished IDs are no-ops, matching the
// spec's allowance to ignore cancellations for requests that no longer exist.
func (c *mcpStdioClient) cancelInFlight(req mcptools.JSONRPCRequest) {
	var params struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || len(params.RequestID) == 0 {
		return
	}
	if cancel, ok := c.inflight.Load(string(params.RequestID)); ok {
		cancel.(context.CancelFunc)()
	}
}

// jsonrpcResultPayload wraps a typed SDK result into a standard JSON-RPC
// envelope via a JSON round-trip.
func jsonrpcResultPayload(id json.RawMessage, result any) map[string]any {
	var idValue any
	if len(id) > 0 {
		_ = json.Unmarshal(id, &idValue)
	}
	var resultValue any
	if result != nil {
		if raw, err := json.Marshal(result); err == nil {
			_ = json.Unmarshal(raw, &resultValue)
		}
	}
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      idValue,
		"result":  resultValue,
	}
}

type mcpStderrTail struct {
	mu    sync.Mutex
	lines []string
}

func (t *mcpStderrTail) append(line string) {
	if t == nil || line == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, line)
	const maxStderrTailLines = 8
	if len(t.lines) > maxStderrTailLines {
		t.lines = append([]string(nil), t.lines[len(t.lines)-maxStderrTailLines:]...)
	}
}

func (t *mcpStderrTail) String() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.lines, "\n")
}

func startMCPStderrLogger(stderr io.ReadCloser, containerID string, logger *slog.Logger, tail *mcpStderrTail) {
	if stderr == nil {
		return
	}
	go func() {
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			tail.append(line)
			logger.Warn("mcp stderr", slog.String("container_id", containerID), slog.String("message", line))
		}
		if err := scanner.Err(); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) || strings.Contains(err.Error(), "closed pipe") {
				return
			}
			logger.Error("mcp stderr read failed", slog.Any("error", err), slog.String("container_id", containerID))
		}
	}()
}

// buildShellCommand renders the stdio launch line executed via `sh -c` inside the
// workspace container. Command is a single executable token — callers must not
// smuggle arguments into it (they would be escaped into one bogus binary name);
// flags and operands belong in Args.
func buildShellCommand(req MCPStdioRequest) string {
	cmd := strings.TrimSpace(req.Command)
	if cmd == "" {
		return ""
	}
	parts := make([]string, 0, len(req.Args)+1)
	parts = append(parts, escapeShellArg(cmd))
	for _, arg := range req.Args {
		parts = append(parts, escapeShellArg(arg))
	}
	command := strings.Join(parts, " ")

	assignments := []string{}
	for _, pair := range buildEnvPairs(req.Env) {
		// Quote KEY and VALUE separately: quoting the whole "KEY=value" pair
		// makes sh see a quoted word, not an assignment prefix, and the launch
		// dies with "KEY=value: command not found" (exit 127).
		key, value, _ := strings.Cut(pair, "=")
		assignments = append(assignments, key+"="+escapeShellArg(value))
	}
	if len(assignments) > 0 {
		command = strings.Join(assignments, " ") + " " + command
	}
	if strings.TrimSpace(req.Cwd) != "" {
		command = "cd " + escapeShellArg(req.Cwd) + " && " + command
	}
	return command
}

func escapeShellArg(value string) string {
	if value == "" {
		return "''"
	}
	// '#' must force quoting too: as the first char of a bare word it starts a
	// shell comment and silently eats the rest of the line.
	if !strings.ContainsAny(value, " \t\n'\"\\$&;|<>*?()[]{}!`#") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func buildEnvPairs(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		if strings.TrimSpace(k) != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, fmt.Sprintf("%s=%s", k, env[k]))
	}
	return out
}

// ---------- MCP Stdio Handlers ----------

type mcpStdioSession struct {
	id          string
	botID       string
	containerID string
	name        string
	createdAt   time.Time
	// request is retained for the lazy start: the session process spawns on
	// the FIRST proxied message, so an initialize can drive the handshake with
	// the external client's own capabilities (see ensureStdioSession). An
	// eager session would burn the single handshake on memoh's empty one.
	request MCPStdioRequest
	// startMu serializes the lazy start across concurrent first messages.
	startMu sync.Mutex
	session *mcpStdioClient
}

// CreateMCPStdio godoc
// @Summary Create MCP stdio proxy
// @Description Start a stdio MCP process in the bot workspace and expose it as an MCP HTTP endpoint.
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Param payload body MCPStdioRequest true "Stdio MCP payload"
// @Success 200 {object} MCPStdioResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/mcp-stdio [post].
func (h *ContainerdHandler) CreateMCPStdio(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	var req MCPStdioRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if strings.TrimSpace(req.Command) == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "command is required")
	}
	ctx := c.Request().Context()
	if err := h.manager.EnsureRunning(ctx, botID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	containerID, err := h.manager.ContainerID(ctx, botID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "workspace runtime not found for bot")
	}

	connectionID := uuid.NewString()
	record := &mcpStdioSession{
		id:          connectionID,
		botID:       botID,
		containerID: containerID,
		name:        strings.TrimSpace(req.Name),
		createdAt:   time.Now().UTC(),
		request:     req,
	}
	// Eager probe on a THROWAWAY process: it validates the command and fills
	// the response's tool list, then dies. The session process starts lazily
	// on the first proxied message (see mcpStdioSession.request), so create
	// keeps its old contract — fail fast on a bad command, return the tool
	// list — without burning the session's handshake on memoh's capabilities.
	probeSess, err := h.startContainerdMCPCommandSession(ctx, botID, containerID, req, nil, defaultStdioSDKClient())
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	tools := h.probeMCPTools(ctx, probeSess, botID, strings.TrimSpace(req.Name))
	probeSess.Close()
	h.mcpStdioMu.Lock()
	h.mcpStdioSess[connectionID] = record
	h.mcpStdioMu.Unlock()

	return c.JSON(http.StatusOK, MCPStdioResponse{
		ConnectionID: connectionID,
		URL:          fmt.Sprintf("/bots/%s/mcp-stdio/%s", botID, connectionID),
		Tools:        tools,
	})
}

// HandleMCPStdio godoc
// @Summary MCP stdio proxy (JSON-RPC)
// @Description Proxies MCP JSON-RPC requests to a stdio MCP process in the workspace.
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Param connection_id path string true "Connection ID"
// @Param payload body object true "JSON-RPC request"
// @Success 200 {object} object "JSON-RPC response: {jsonrpc,id,result|error}"
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/mcp-stdio/{connection_id} [post].
// ensureStdioSession lazily starts the session process on the first proxied
// message. An initialize starts it with a client built from the message's own
// capabilities/clientInfo (the server then negotiates against the REAL client);
// anything else starts it with memoh's default identity. Concurrent first
// messages serialize on the record's startMu.
func (h *ContainerdHandler) ensureStdioSession(ctx context.Context, record *mcpStdioSession, req *mcptools.JSONRPCRequest) (*mcpStdioClient, error) {
	record.startMu.Lock()
	defer record.startMu.Unlock()
	if record.session != nil {
		return record.session, nil
	}
	client := defaultStdioSDKClient()
	if req != nil && strings.TrimSpace(req.Method) == "initialize" {
		client = sdkClientForInitialize(req.Params)
	}
	sess, err := h.startContainerdMCPCommandSession(ctx, record.botID, record.containerID, record.request, func() {
		h.mcpStdioMu.Lock()
		if current, ok := h.mcpStdioSess[record.id]; ok && current == record {
			delete(h.mcpStdioSess, record.id)
		}
		h.mcpStdioMu.Unlock()
	}, client)
	if err != nil {
		return nil, err
	}
	record.session = sess
	return sess, nil
}

func (h *ContainerdHandler) HandleMCPStdio(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	connectionID := strings.TrimSpace(c.Param("connection_id"))
	if connectionID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "connection_id is required")
	}
	h.mcpStdioMu.Lock()
	record := h.mcpStdioSess[connectionID]
	h.mcpStdioMu.Unlock()
	if record == nil || record.botID != botID {
		return echo.NewHTTPError(http.StatusNotFound, "mcp connection not found")
	}

	var req mcptools.JSONRPCRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if req.JSONRPC != "" && req.JSONRPC != "2.0" {
		return c.JSON(http.StatusOK, mcptools.JSONRPCErrorResponse(req.ID, -32600, "invalid jsonrpc version"))
	}
	if strings.TrimSpace(req.Method) == "" {
		return c.JSON(http.StatusOK, mcptools.JSONRPCErrorResponse(req.ID, -32601, "method not found"))
	}

	sess, err := h.ensureStdioSession(c.Request().Context(), record, &req)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	select {
	case <-sess.done:
		return echo.NewHTTPError(http.StatusNotFound, "mcp connection closed")
	default:
	}

	if mcptools.IsNotification(req) {
		// The SDK client owns the protocol and offers no generic notify to
		// forward. cancelled is the one notification with load-bearing
		// semantics, so it gets a real path: resolving the in-flight call and
		// cancelling it makes the SDK notify the server itself. Everything
		// else (progress, custom notifications) is still acknowledged and
		// dropped — a known fidelity gap, now narrowed to messages no standard
		// server depends on.
		if strings.TrimSpace(req.Method) == "notifications/cancelled" {
			sess.cancelInFlight(req)
		} else {
			h.logger.Debug("mcp stdio notification dropped",
				slog.String("connection_id", connectionID),
				slog.String("method", req.Method),
			)
		}
		return c.NoContent(http.StatusAccepted)
	}
	payload, err := sess.dispatch(c.Request().Context(), req)
	if err != nil {
		code := int64(-32603)
		var wireErr *jsonrpc.Error
		switch {
		case errors.Is(err, errMCPMethodNotFound):
			code = -32601
		case errors.As(err, &wireErr):
			// Forward the downstream server's code verbatim.
			code = wireErr.Code
		}
		return c.JSON(http.StatusOK, mcptools.JSONRPCErrorResponse(req.ID, int(code), err.Error()))
	}
	return c.JSON(http.StatusOK, payload)
}

// defaultStdioSDKClient is the fixed client identity memoh presents when it is
// the real client itself (create-time probes, federation gateway sessions).
func defaultStdioSDKClient() *sdkmcp.Client {
	return sdkmcp.NewClient(&sdkmcp.Implementation{Name: "memoh", Version: "1.0.0"}, nil)
}

// sdkClientForInitialize builds the SDK client for a lazily-started proxy
// session from the EXTERNAL client's own initialize params, so the container
// server sees the real client's capabilities and clientInfo instead of memoh's
// fixed empty handshake. The pre-migration client raw-forwarded the external
// initialize as a spec-invalid SECOND initialize on an already-handshaken
// session; advertising at the single handshake is the spec-valid form of the
// same intent. Capabilities the proxy cannot fulfill (sampling, elicitation —
// server→client requests have no channel back to the HTTP client) stay
// advertised: the SDK answers them with method-not-found, an explicit error
// instead of the old silent hang.
func sdkClientForInitialize(rawParams json.RawMessage) *sdkmcp.Client {
	var params sdkmcp.InitializeParams
	if err := json.Unmarshal(rawParams, &params); err != nil {
		return defaultStdioSDKClient()
	}
	impl := params.ClientInfo
	if impl == nil || strings.TrimSpace(impl.Name) == "" {
		impl = &sdkmcp.Implementation{Name: "memoh", Version: "1.0.0"}
	}
	opts := &sdkmcp.ClientOptions{}
	if caps := params.Capabilities; caps != nil {
		// #607: the SDK's deprecated Roots field is ignored at handshake —
		// only RootsV2 actually advertises roots. Decode the wire "roots"
		// member straight into RootCapabilities rather than reading the
		// deprecated field.
		var wire struct {
			Capabilities map[string]json.RawMessage `json:"capabilities"`
		}
		if err := json.Unmarshal(rawParams, &wire); err == nil {
			if rawRoots, hasRoots := wire.Capabilities["roots"]; hasRoots && caps.RootsV2 == nil { //nolint:staticcheck // SEP-2577: proxy re-advertises the external client's roots capability verbatim during the deprecation window.
				roots := &sdkmcp.RootCapabilities{} //nolint:staticcheck // SEP-2577: passthrough, see above.
				if err := json.Unmarshal(rawRoots, roots); err == nil {
					caps.RootsV2 = roots //nolint:staticcheck // SEP-2577: passthrough, see above.
				}
			}
		}
		opts.Capabilities = caps
	}
	return sdkmcp.NewClient(impl, opts)
}

// connectStdioClient performs the MCP initialize handshake over the given process
// pipes and returns the ready session. Split from
// startContainerdMCPCommandSession so tests can exercise the protocol path over
// plain in-memory pipes instead of a container.
func connectStdioClient(ctx context.Context, stdin io.WriteCloser, stdout io.ReadCloser, sdkClient *sdkmcp.Client) (*sdkmcp.ClientSession, error) {
	return sdkClient.Connect(ctx, &sdkmcp.IOTransport{Reader: stdout, Writer: stdin}, nil)
}

// onClose is registered on the client BEFORE connect/Wait can fire Close: a
// process that dies during the handshake must still run the registry cleanup,
// otherwise closeOnce is consumed with a nil callback and the entry leaks.
// The client argument chooses the identity presented at the handshake:
// defaultStdioSDKClient() when memoh is the real client, or
// sdkClientForInitialize(...) for lazily-started proxy sessions.
func (h *ContainerdHandler) startContainerdMCPCommandSession(ctx context.Context, botID, containerID string, req MCPStdioRequest, onClose func(), sdkClient *sdkmcp.Client) (*mcpStdioClient, error) {
	// Get gRPC client for the bot container via manager
	client, err := h.manager.MCPClient(ctx, botID)
	if err != nil {
		return nil, fmt.Errorf("get workspace runtime client: %w", err)
	}

	command := buildShellCommand(req)

	// timeout -1 disables the bridge-side timer: 0 falls back to its 30s default
	// and SIGKILLs the process, which murdered every long-lived MCP server. These
	// processes are session-scoped daemons; their lifetime is owned by Close().
	execStream, err := client.ExecStream(ctx, command, strings.TrimSpace(req.Cwd), -1)
	if err != nil {
		return nil, err
	}

	// Create pipes for stdin/stdout/stderr
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()

	sess := &mcpStdioClient{
		stderrTail:  &mcpStderrTail{},
		done:        make(chan struct{}),
		streamClose: func() { _ = execStream.Close() },
		onClose:     onClose,
	}
	sess.exitCode.Store(-1)

	// Forward stdin to the bridge stream
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := stdinR.Read(buf)
			if n > 0 {
				_ = execStream.SendStdin(buf[:n])
			}
			if err != nil {
				break
			}
		}
		_ = stdinR.Close()
	}()

	// Demux bridge stdout/stderr into the pipes. The EXIT frame carries the
	// process exit code — capture it before dropping the pipes so later failures
	// can say WHY the server died instead of surfacing a bare EOF.
	go func() {
		for {
			output, err := execStream.Recv()
			if err != nil {
				if !errors.Is(err, io.EOF) {
					h.logger.Debug("exec stream recv done", slog.Any("error", err))
				}
				_ = stdoutW.Close()
				_ = stderrW.Close()
				return
			}
			switch output.GetStream() {
			case pb.ExecOutput_STDOUT:
				_, _ = stdoutW.Write(output.GetData())
			case pb.ExecOutput_STDERR:
				_, _ = stderrW.Write(output.GetData())
			case pb.ExecOutput_EXIT:
				sess.exitCode.Store(output.GetExitCode())
				_ = stdoutW.Close()
				_ = stderrW.Close()
				return
			}
		}
	}()

	startMCPStderrLogger(stderrR, containerID, h.logger, sess.stderrTail)

	session, err := connectStdioClient(ctx, stdinW, stdoutR, sdkClient)
	if err != nil {
		// Handshake failed — most commonly the process is not an MCP server and
		// already exited (e.g. command not found). Report that, not a bare EOF.
		err = sess.enrichError(err)
		sess.Close()
		return nil, err
	}
	sess.session = session
	go func() {
		_ = session.Wait()
		// Server side ended on its own — run the same teardown as an explicit Close.
		sess.Close()
	}()
	return sess, nil
}

func (h *ContainerdHandler) probeMCPTools(ctx context.Context, sess *mcpStdioClient, botID, name string) []string {
	if sess == nil || sess.session == nil {
		return nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	result, err := sess.session.ListTools(probeCtx, &sdkmcp.ListToolsParams{})
	if err != nil {
		h.logger.Warn("mcp stdio tools probe failed",
			slog.String("bot_id", botID),
			slog.String("name", name),
			slog.Any("error", sess.enrichError(err)),
		)
		return nil
	}
	tools := make([]string, 0, len(result.Tools))
	for _, tool := range result.Tools {
		if tool == nil {
			continue
		}
		if n := strings.TrimSpace(tool.Name); n != "" {
			tools = append(tools, n)
		}
	}
	sort.Strings(tools)
	if len(tools) == 0 {
		h.logger.Warn("mcp stdio tools empty",
			slog.String("bot_id", botID),
			slog.String("name", name),
		)
	} else {
		h.logger.Info("mcp stdio tools loaded",
			slog.String("bot_id", botID),
			slog.String("name", name),
			slog.Int("count", len(tools)),
		)
	}
	return tools
}
