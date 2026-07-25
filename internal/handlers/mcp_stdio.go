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
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	mcptools "github.com/memohai/memoh/internal/mcp"
	pb "github.com/memohai/memoh/internal/workspace/bridgepb"
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
	if code := c.exitCode.Load(); code >= 0 {
		fmt.Fprintf(&b, "process exited with code %d", code)
	} else {
		b.WriteString(err.Error())
	}
	if tail := strings.TrimSpace(c.stderrTail.String()); tail != "" {
		b.WriteString(": ")
		b.WriteString(tail)
	}
	// No diagnostics captured → hand the original error back untouched so
	// errors.Is/As chains keep working.
	if code := c.exitCode.Load(); code < 0 && b.String() == err.Error() {
		return err
	}
	if b.Len() == 0 {
		return err
	}
	return errors.New(b.String())
}

// errMCPMethodNotFound marks an unsupported method on the stdio proxy endpoint
// so the handler can answer -32601 (method not found) instead of -32603.
var errMCPMethodNotFound = errors.New("method not found")

// dispatch answers a raw JSON-RPC request from the external proxy endpoint using
// the typed SDK session. The go-sdk client offers no raw passthrough, so the
// surface is a bounded method table instead of arbitrary forwarding.
func (c *mcpStdioClient) dispatch(ctx context.Context, req mcptools.JSONRPCRequest) (map[string]any, error) {
	switch strings.TrimSpace(req.Method) {
	case "ping":
		if err := c.session.Ping(ctx, &sdkmcp.PingParams{}); err != nil {
			return nil, c.enrichError(err)
		}
		return jsonrpcResultPayload(req.ID, map[string]any{}), nil
	case "initialize":
		// The session already handshook at connect; replay the stored result.
		result := c.session.InitializeResult()
		if result == nil {
			return jsonrpcResultPayload(req.ID, map[string]any{}), nil
		}
		return jsonrpcResultPayload(req.ID, result), nil
	case "tools/list":
		result, err := c.session.ListTools(ctx, &sdkmcp.ListToolsParams{})
		if err != nil {
			return nil, c.enrichError(err)
		}
		return jsonrpcResultPayload(req.ID, result), nil
	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				return nil, fmt.Errorf("invalid tools/call params: %w", err)
			}
		}
		result, err := c.session.CallTool(ctx, &sdkmcp.CallToolParams{
			Name:      strings.TrimSpace(params.Name),
			Arguments: params.Arguments,
		})
		if err != nil {
			return nil, c.enrichError(err)
		}
		return jsonrpcResultPayload(req.ID, result), nil
	default:
		return nil, errMCPMethodNotFound
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
		assignments = append(assignments, escapeShellArg(pair))
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
	if !strings.ContainsAny(value, " \t\n'\"\\$&;|<>*?()[]{}!`") {
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
	lastUsedAt  time.Time
	session     *mcpStdioClient
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

	sess, err := h.startContainerdMCPCommandSession(ctx, botID, containerID, req)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	tools := h.probeMCPTools(ctx, sess, botID, strings.TrimSpace(req.Name))
	connectionID := uuid.NewString()
	record := &mcpStdioSession{
		id:          connectionID,
		botID:       botID,
		containerID: containerID,
		name:        strings.TrimSpace(req.Name),
		createdAt:   time.Now().UTC(),
		lastUsedAt:  time.Now().UTC(),
		session:     sess,
	}
	sess.onClose = func() {
		h.mcpStdioMu.Lock()
		if current, ok := h.mcpStdioSess[connectionID]; ok && current == record {
			delete(h.mcpStdioSess, connectionID)
		}
		h.mcpStdioMu.Unlock()
	}
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
	session := h.mcpStdioSess[connectionID]
	h.mcpStdioMu.Unlock()
	if session == nil || session.session == nil || session.botID != botID {
		return echo.NewHTTPError(http.StatusNotFound, "mcp connection not found")
	}
	select {
	case <-session.session.done:
		return echo.NewHTTPError(http.StatusNotFound, "mcp connection closed")
	default:
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
	session.lastUsedAt = time.Now().UTC()
	if mcptools.IsNotification(req) {
		// The SDK client owns the protocol and offers no generic notify to
		// forward. Notifications carry no response, so acknowledge and drop.
		return c.NoContent(http.StatusAccepted)
	}
	payload, err := session.session.dispatch(c.Request().Context(), req)
	if err != nil {
		code := -32603
		if errors.Is(err, errMCPMethodNotFound) {
			code = -32601
		}
		return c.JSON(http.StatusOK, mcptools.JSONRPCErrorResponse(req.ID, code, err.Error()))
	}
	return c.JSON(http.StatusOK, payload)
}

// connectStdioClient performs the MCP initialize handshake over the given process
// pipes and returns the ready session. Split from
// startContainerdMCPCommandSession so tests can exercise the protocol path over
// plain in-memory pipes instead of a container.
func connectStdioClient(ctx context.Context, stdin io.WriteCloser, stdout io.ReadCloser) (*sdkmcp.ClientSession, error) {
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "memoh", Version: "1.0.0"}, nil)
	return client.Connect(ctx, &sdkmcp.IOTransport{Reader: stdout, Writer: stdin}, nil)
}

func (h *ContainerdHandler) startContainerdMCPCommandSession(ctx context.Context, botID, containerID string, req MCPStdioRequest) (*mcpStdioClient, error) {
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

	session, err := connectStdioClient(ctx, stdinW, stdoutR)
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
