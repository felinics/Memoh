package runtime

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
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	sdkjsonrpc "github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	mcptools "github.com/memohai/memoh/domains/agent/mcp"
	"github.com/memohai/memoh/domains/runtime/bridge/bridgepb"
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

// mcpSession represents an MCP session over stdio.
type mcpSession struct {
	stdin      io.WriteCloser
	stdout     io.ReadCloser
	stderr     io.ReadCloser
	stderrTail *mcpStderrTail
	readCtx    context.Context
	cancelRead context.CancelFunc
	initMu     sync.Mutex
	initState  mcpSessionInitState
	initWait   chan struct{}
	pendingMu  sync.Mutex
	pending    map[string]chan *sdkjsonrpc.Response
	conn       sdkmcp.Connection
	closed     chan struct{}
	closeOnce  sync.Once
	closeErr   error
	onClose    func()
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

func (s *mcpSession) errorWithStderr(err error) error {
	if err == nil {
		err = io.EOF
	}
	stderr := strings.TrimSpace(s.stderrTail.String())
	if stderr == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, stderr)
}

type mcpSessionInitState uint8

const (
	mcpSessionInitStateNone mcpSessionInitState = iota
	mcpSessionInitStateInitializing
	mcpSessionInitStateInitialized
	mcpSessionInitStateReady
)

func (s *mcpSession) closeWithError(err error) {
	s.closeOnce.Do(func() {
		s.closeErr = err
		close(s.closed)
		if s.cancelRead != nil {
			s.cancelRead()
		}
		s.pendingMu.Lock()
		for _, ch := range s.pending {
			close(ch)
		}
		s.pending = map[string]chan *sdkjsonrpc.Response{}
		s.pendingMu.Unlock()
		if s.conn != nil {
			_ = s.conn.Close()
		}
		if s.stdin != nil {
			_ = s.stdin.Close()
		}
		if s.stdout != nil {
			_ = s.stdout.Close()
		}
		if s.stderr != nil {
			_ = s.stderr.Close()
		}
		if s.onClose != nil {
			s.onClose()
		}
	})
}

func (s *mcpSession) readLoop() {
	if s.conn == nil {
		s.closeWithError(io.EOF)
		return
	}
	for {
		msg, err := s.conn.Read(s.readCtx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				s.closeWithError(io.EOF)
				return
			}
			s.closeWithError(err)
			return
		}
		resp, ok := msg.(*sdkjsonrpc.Response)
		if !ok || !resp.ID.IsValid() {
			continue
		}
		id := sdkIDKey(resp.ID)
		if id == "" {
			continue
		}
		s.pendingMu.Lock()
		ch, ok := s.pending[id]
		if ok {
			delete(s.pending, id)
		}
		s.pendingMu.Unlock()
		if ok {
			ch <- resp
			close(ch)
		}
	}
}

func (s *mcpSession) call(ctx context.Context, req mcptools.JSONRPCRequest) (map[string]any, error) {
	method := strings.TrimSpace(req.Method)
	if method == "initialize" {
		payload, err := s.callRaw(ctx, req)
		if err != nil {
			return nil, err
		}
		// If the server accepted our initialize, advance state so
		// ensureInitialized will only send notifications/initialized next time.
		if _, hasError := payload["error"]; !hasError {
			s.initMu.Lock()
			if s.initState < mcpSessionInitStateInitialized {
				s.initState = mcpSessionInitStateInitialized
			}
			s.initMu.Unlock()
		}
		return payload, nil
	}
	if method != "notifications/initialized" {
		if err := s.ensureInitialized(ctx); err != nil {
			return nil, err
		}
	}
	return s.callRaw(ctx, req)
}

func (s *mcpSession) callRaw(ctx context.Context, req mcptools.JSONRPCRequest) (map[string]any, error) {
	targetID, err := parseRawJSONRPCID(req.ID)
	if err != nil {
		return nil, err
	}
	target := sdkIDKey(targetID)
	if target == "" {
		return nil, errors.New("missing request id")
	}

	respCh := make(chan *sdkjsonrpc.Response, 1)
	s.pendingMu.Lock()
	s.pending[target] = respCh
	s.pendingMu.Unlock()

	callReq := &sdkjsonrpc.Request{
		ID:     targetID,
		Method: req.Method,
		Params: req.Params,
	}
	if err := s.conn.Write(ctx, callReq); err != nil {
		s.pendingMu.Lock()
		delete(s.pending, target)
		s.pendingMu.Unlock()
		return nil, s.errorWithStderr(err)
	}

	select {
	case resp, ok := <-respCh:
		if !ok {
			if s.closeErr != nil {
				return nil, s.errorWithStderr(s.closeErr)
			}
			return nil, io.EOF
		}
		return sdkResponsePayload(resp)
	case <-s.closed:
		if s.closeErr != nil {
			return nil, s.errorWithStderr(s.closeErr)
		}
		return nil, io.EOF
	case <-ctx.Done():
		s.pendingMu.Lock()
		delete(s.pending, target)
		s.pendingMu.Unlock()
		return nil, ctx.Err()
	}
}

// sdkResponsePayload wraps an SDK JSON-RPC response into a standard JSON-RPC
// envelope ({"jsonrpc":"2.0","id":...,"result":...} or "error":...).
func sdkResponsePayload(resp *sdkjsonrpc.Response) (map[string]any, error) {
	if resp == nil {
		return nil, io.EOF
	}
	if resp.Error != nil {
		code := int64(-32603)
		message := strings.TrimSpace(resp.Error.Error())
		wireErr := &sdkjsonrpc.Error{}
		if errors.As(resp.Error, &wireErr) {
			code = wireErr.Code
			message = strings.TrimSpace(wireErr.Message)
		}
		if message == "" {
			message = "internal error"
		}
		return map[string]any{
			"jsonrpc": "2.0",
			"id":      sdkIDRaw(resp.ID),
			"error": map[string]any{
				"code":    code,
				"message": message,
			},
		}, nil
	}
	var result any
	if len(resp.Result) > 0 {
		if err := json.Unmarshal(resp.Result, &result); err != nil {
			return nil, err
		}
	}
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      sdkIDRaw(resp.ID),
		"result":  result,
	}, nil
}

func sdkIDRaw(id sdkjsonrpc.ID) any {
	if !id.IsValid() {
		return nil
	}
	return id.Raw()
}

func (s *mcpSession) notify(ctx context.Context, req mcptools.JSONRPCRequest) error {
	if s.conn == nil {
		return io.EOF
	}
	return s.conn.Write(ctx, &sdkjsonrpc.Request{
		Method: req.Method,
		Params: req.Params,
	})
}

func (s *mcpSession) ensureInitialized(ctx context.Context) error {
	for {
		s.initMu.Lock()
		state := s.initState

		switch state {
		case mcpSessionInitStateReady:
			s.initMu.Unlock()
			return nil
		case mcpSessionInitStateInitializing:
			waitCh := s.initWait
			s.initMu.Unlock()
			if waitCh == nil {
				continue
			}
			select {
			case <-waitCh:
				continue
			case <-ctx.Done():
				return ctx.Err()
			case <-s.closed:
				if s.closeErr != nil {
					return s.closeErr
				}
				return io.EOF
			}
		case mcpSessionInitStateInitialized:
			waitCh := make(chan struct{})
			s.initState = mcpSessionInitStateInitializing
			s.initWait = waitCh
			s.initMu.Unlock()

			err := s.sendInitializedNotification(ctx)

			s.initMu.Lock()
			if err == nil {
				s.initState = mcpSessionInitStateReady
			} else {
				s.initState = mcpSessionInitStateInitialized
			}
			s.initWait = nil
			close(waitCh)
			s.initMu.Unlock()

			if err != nil {
				return err
			}
			return nil
		default:
			waitCh := make(chan struct{})
			s.initState = mcpSessionInitStateInitializing
			s.initWait = waitCh
			s.initMu.Unlock()

			nextState, err := s.initializeHandshake(ctx)

			s.initMu.Lock()
			s.initState = nextState
			s.initWait = nil
			close(waitCh)
			s.initMu.Unlock()

			if err != nil {
				return err
			}
			if nextState == mcpSessionInitStateReady {
				return nil
			}
		}
	}
}

func (s *mcpSession) initializeHandshake(ctx context.Context) (mcpSessionInitState, error) {
	initID, _ := sdkjsonrpc.MakeID("init")
	params, _ := json.Marshal(map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "memoh",
			"version": "1.0.0",
		},
	})
	initResp, err := s.invokeCall(ctx, &sdkjsonrpc.Request{
		ID:     initID,
		Method: "initialize",
		Params: params,
	})
	if err != nil {
		return mcpSessionInitStateNone, err
	}
	if initResp.Error != nil {
		return mcpSessionInitStateNone, initResp.Error
	}
	if err := s.sendInitializedNotification(ctx); err != nil {
		return mcpSessionInitStateInitialized, err
	}
	return mcpSessionInitStateReady, nil
}

func (s *mcpSession) sendInitializedNotification(ctx context.Context) error {
	if s.conn == nil {
		return io.EOF
	}
	return s.conn.Write(ctx, &sdkjsonrpc.Request{
		Method: "notifications/initialized",
	})
}

func (s *mcpSession) invokeCall(ctx context.Context, req *sdkjsonrpc.Request) (*sdkjsonrpc.Response, error) {
	if s.conn == nil {
		return nil, io.EOF
	}
	if req == nil || !req.ID.IsValid() {
		return nil, errors.New("missing request id")
	}
	key := sdkIDKey(req.ID)
	if key == "" {
		return nil, errors.New("invalid request id")
	}

	respCh := make(chan *sdkjsonrpc.Response, 1)
	s.pendingMu.Lock()
	s.pending[key] = respCh
	s.pendingMu.Unlock()

	if err := s.conn.Write(ctx, req); err != nil {
		s.removePending(key)
		return nil, err
	}

	select {
	case resp, ok := <-respCh:
		if !ok {
			if s.closeErr != nil {
				return nil, s.closeErr
			}
			return nil, io.EOF
		}
		return resp, nil
	case <-s.closed:
		if s.closeErr != nil {
			return nil, s.closeErr
		}
		return nil, io.EOF
	case <-ctx.Done():
		s.removePending(key)
		return nil, ctx.Err()
	}
}

func (s *mcpSession) removePending(key string) {
	if strings.TrimSpace(key) == "" {
		return
	}
	s.pendingMu.Lock()
	delete(s.pending, key)
	s.pendingMu.Unlock()
}

func parseRawJSONRPCID(raw json.RawMessage) (sdkjsonrpc.ID, error) {
	if len(raw) == 0 {
		return sdkjsonrpc.ID{}, errors.New("missing request id")
	}
	var idValue any
	if err := json.Unmarshal(raw, &idValue); err != nil {
		return sdkjsonrpc.ID{}, err
	}
	id, err := sdkjsonrpc.MakeID(idValue)
	if err != nil {
		return sdkjsonrpc.ID{}, err
	}
	if !id.IsValid() {
		return sdkjsonrpc.ID{}, errors.New("missing request id")
	}
	return id, nil
}

func sdkIDKey(id sdkjsonrpc.ID) string {
	if !id.IsValid() {
		return ""
	}
	raw, _ := json.Marshal(id.Raw())
	return string(raw)
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

func extractToolNames(payload map[string]any) []string {
	result, ok := payload["result"].(map[string]any)
	if !ok {
		return nil
	}
	rawTools, ok := result["tools"].([]any)
	if !ok {
		return nil
	}
	names := make([]string, 0, len(rawTools))
	for _, raw := range rawTools {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, _ := item["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

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
	session     *mcpSession
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
	case <-session.session.closed:
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
		if err := session.session.notify(c.Request().Context(), req); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		return c.NoContent(http.StatusAccepted)
	}
	payload, err := session.session.call(c.Request().Context(), req)
	if err != nil {
		return c.JSON(http.StatusOK, mcptools.JSONRPCErrorResponse(req.ID, -32603, err.Error()))
	}
	return c.JSON(http.StatusOK, payload)
}

func (h *ContainerdHandler) startContainerdMCPCommandSession(ctx context.Context, botID, containerID string, req MCPStdioRequest) (*mcpSession, error) {
	// Get gRPC client for the bot container via manager
	client, err := h.manager.MCPClient(ctx, botID)
	if err != nil {
		return nil, fmt.Errorf("get workspace runtime client: %w", err)
	}

	command := buildShellCommand(req)

	// Create bidirectional exec stream
	execStream, err := client.ExecStream(ctx, command, strings.TrimSpace(req.Cwd), 0)
	if err != nil {
		return nil, err
	}

	// Create pipes for stdin/stdout/stderr
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()

	readCtx, cancelRead := context.WithCancel(context.Background()) //nolint:gosec // G118: cancelRead is stored in sess.cancelRead
	sess := &mcpSession{
		stdin:      stdinW,
		stdout:     stdoutR,
		stderr:     stderrR,
		stderrTail: &mcpStderrTail{},
		readCtx:    readCtx,
		cancelRead: cancelRead,
		pending:    make(map[string]chan *sdkjsonrpc.Response),
		closed:     make(chan struct{}),
	}

	// Forward stdin to gRPC stream
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

	// Forward gRPC stdout/stderr to pipes
	go func() {
		for {
			output, err := execStream.Recv()
			if err != nil {
				if !errors.Is(err, io.EOF) {
					h.logger.DebugContext(ctx, "exec stream recv done", slog.Any("error", err))
				}
				_ = stdoutW.Close()
				_ = stderrW.Close()
				break
			}
			switch output.GetStream() {
			case bridgepb.ExecOutput_STDOUT:
				_, _ = stdoutW.Write(output.GetData())
			case bridgepb.ExecOutput_STDERR:
				_, _ = stderrW.Write(output.GetData())
			case bridgepb.ExecOutput_EXIT:
				_ = stdoutW.Close()
				_ = stderrW.Close()
				return
			}
		}
	}()

	transport := &sdkmcp.IOTransport{
		Reader: sess.stdout,
		Writer: sess.stdin,
	}
	conn, err := transport.Connect(ctx)
	if err != nil {
		sess.closeWithError(err)
		return nil, err
	}
	sess.conn = conn
	startMCPStderrLogger(sess.stderr, containerID, h.logger, sess.stderrTail)
	go sess.readLoop()
	go func() {
		<-sess.closed
		_ = execStream.Close()
	}()
	return sess, nil
}

func (h *ContainerdHandler) probeMCPTools(ctx context.Context, sess *mcpSession, botID, name string) []string {
	if sess == nil {
		return nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	payload, err := sess.call(probeCtx, mcptools.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      mcptools.RawStringID("probe-tools"),
		Method:  "tools/list",
	})
	if err != nil {
		h.logger.WarnContext(ctx, "mcp stdio tools probe failed",
			slog.String("bot_id", botID),
			slog.String("name", name),
			slog.Any("error", err),
		)
		return nil
	}
	tools := extractToolNames(payload)
	if len(tools) == 0 {
		h.logger.WarnContext(ctx, "mcp stdio tools empty",
			slog.String("bot_id", botID),
			slog.String("name", name),
		)
	} else {
		h.logger.InfoContext(ctx, "mcp stdio tools loaded",
			slog.String("bot_id", botID),
			slog.String("name", name),
			slog.Int("count", len(tools)),
		)
	}
	return tools
}
