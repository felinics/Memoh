package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	mcptools "github.com/memohai/memoh/internal/mcp"
)

// newStdioPipePair returns the two pipe ends an in-container process would
// present to the client (stdin writer, stdout reader) plus the ends a test
// server listens on. Production gets the same shape from the bridge exec
// stream; tests skip the container entirely.
func newStdioPipePair() (clientStdin io.WriteCloser, clientStdout io.ReadCloser, serverStdin io.ReadCloser, serverStdout io.WriteCloser) {
	serverStdin, clientStdin = io.Pipe()
	clientStdout, serverStdout = io.Pipe()
	return clientStdin, clientStdout, serverStdin, serverStdout
}

type echoToolArgs struct {
	Text string `json:"text"`
}

// serveTestMCPServer runs a real go-sdk MCP server with one echo tool, one
// prompt, and one resource over the given pipes — the server half of the stdio
// seam. All three capability families are registered so the proxy dispatch can
// be exercised across everything the initialize result advertises.
func serveTestMCPServer(t *testing.T, stdin io.ReadCloser, stdout io.WriteCloser) {
	t.Helper()
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "test-mcp", Version: "v0"}, nil)
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "echo", Description: "echoes the input text"},
		func(_ context.Context, _ *sdkmcp.CallToolRequest, in echoToolArgs) (*sdkmcp.CallToolResult, any, error) {
			return &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: in.Text}},
			}, nil, nil
		})
	server.AddPrompt(&sdkmcp.Prompt{Name: "greet", Description: "greets by name"},
		func(_ context.Context, req *sdkmcp.GetPromptRequest) (*sdkmcp.GetPromptResult, error) {
			return &sdkmcp.GetPromptResult{
				Messages: []*sdkmcp.PromptMessage{
					{Role: "user", Content: &sdkmcp.TextContent{Text: "hello " + req.Params.Arguments["name"]}},
				},
			}, nil
		})
	server.AddResource(&sdkmcp.Resource{URI: "test://doc", Name: "doc"},
		func(_ context.Context, _ *sdkmcp.ReadResourceRequest) (*sdkmcp.ReadResourceResult, error) {
			return &sdkmcp.ReadResourceResult{
				Contents: []*sdkmcp.ResourceContents{{URI: "test://doc", MIMEType: "text/plain", Text: "doc-body"}},
			}, nil
		})
	sess, err := server.Connect(context.Background(), &sdkmcp.IOTransport{Reader: stdin, Writer: stdout}, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
}

// newTestStdioClient wires a full client↔server pair over pipes and returns the
// wrapper under test.
func newTestStdioClient(t *testing.T) *mcpStdioClient {
	t.Helper()
	clientStdin, clientStdout, serverStdin, serverStdout := newStdioPipePair()
	serveTestMCPServer(t, serverStdin, serverStdout)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := connectStdioClient(ctx, clientStdin, clientStdout)
	if err != nil {
		t.Fatalf("connectStdioClient: %v", err)
	}
	client := &mcpStdioClient{
		session:    session,
		stderrTail: &mcpStderrTail{},
		done:       make(chan struct{}),
	}
	client.exitCode.Store(-1)
	t.Cleanup(client.Close)
	return client
}

func callCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// The SDK session handles handshake, ListTools and CallTool over the same pipe
// transport production uses — this is the regression net for the migration off
// the hand-rolled session machine.
func TestConnectStdioClientRoundTrip(t *testing.T) {
	client := newTestStdioClient(t)
	ctx := callCtx(t)

	if err := client.session.Ping(ctx, &sdkmcp.PingParams{}); err != nil {
		t.Fatalf("ping: %v", err)
	}

	tools, err := client.session.ListTools(ctx, &sdkmcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "echo" {
		t.Fatalf("unexpected tools: %+v", tools.Tools)
	}

	result, err := client.session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      "echo",
		Arguments: map[string]any{"text": "hello"},
	})
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("unexpected content: %+v", result.Content)
	}
	text, ok := result.Content[0].(*sdkmcp.TextContent)
	if !ok || text.Text != "hello" {
		t.Fatalf("unexpected tool result: %+v", result.Content[0])
	}
}

func TestMCPStdioClientDispatch(t *testing.T) {
	client := newTestStdioClient(t)
	ctx := callCtx(t)

	t.Run("ping", func(t *testing.T) {
		payload, err := client.dispatch(ctx, mcptools.JSONRPCRequest{Method: "ping", ID: mcptools.RawStringID("p1")})
		if err != nil {
			t.Fatalf("dispatch ping: %v", err)
		}
		if payload["jsonrpc"] != "2.0" || payload["id"] != "p1" {
			t.Fatalf("unexpected ping payload: %+v", payload)
		}
	})

	t.Run("initialize replays the connect handshake result", func(t *testing.T) {
		payload, err := client.dispatch(ctx, mcptools.JSONRPCRequest{Method: "initialize", ID: mcptools.RawStringID("i1")})
		if err != nil {
			t.Fatalf("dispatch initialize: %v", err)
		}
		result, ok := payload["result"].(map[string]any)
		if !ok {
			t.Fatalf("missing initialize result: %+v", payload)
		}
		serverInfo, ok := result["serverInfo"].(map[string]any)
		if !ok || serverInfo["name"] != "test-mcp" {
			t.Fatalf("unexpected initialize result: %+v", result)
		}
	})

	t.Run("tools/list", func(t *testing.T) {
		payload, err := client.dispatch(ctx, mcptools.JSONRPCRequest{Method: "tools/list", ID: mcptools.RawStringID("t1")})
		if err != nil {
			t.Fatalf("dispatch tools/list: %v", err)
		}
		result, ok := payload["result"].(map[string]any)
		if !ok {
			t.Fatalf("missing tools/list result: %+v", payload)
		}
		tools, ok := result["tools"].([]any)
		if !ok || len(tools) != 1 {
			t.Fatalf("unexpected tools: %+v", result)
		}
	})

	t.Run("tools/call", func(t *testing.T) {
		params, _ := json.Marshal(map[string]any{
			"name":      "echo",
			"arguments": map[string]any{"text": "hi"},
		})
		payload, err := client.dispatch(ctx, mcptools.JSONRPCRequest{Method: "tools/call", ID: mcptools.RawStringID("c1"), Params: params})
		if err != nil {
			t.Fatalf("dispatch tools/call: %v", err)
		}
		result, ok := payload["result"].(map[string]any)
		if !ok {
			t.Fatalf("missing tools/call result: %+v", payload)
		}
		content, ok := result["content"].([]any)
		if !ok || len(content) != 1 {
			t.Fatalf("unexpected content: %+v", result)
		}
		first, _ := content[0].(map[string]any)
		if first["text"] != "hi" {
			t.Fatalf("unexpected tool output: %+v", first)
		}
	})

	t.Run("prompts/list", func(t *testing.T) {
		payload, err := client.dispatch(ctx, mcptools.JSONRPCRequest{Method: "prompts/list", ID: mcptools.RawStringID("pl1")})
		if err != nil {
			t.Fatalf("dispatch prompts/list: %v", err)
		}
		result, ok := payload["result"].(map[string]any)
		if !ok {
			t.Fatalf("missing prompts/list result: %+v", payload)
		}
		prompts, ok := result["prompts"].([]any)
		if !ok || len(prompts) != 1 {
			t.Fatalf("unexpected prompts: %+v", result)
		}
	})

	t.Run("prompts/get", func(t *testing.T) {
		params, _ := json.Marshal(map[string]any{
			"name":      "greet",
			"arguments": map[string]any{"name": "world"},
		})
		payload, err := client.dispatch(ctx, mcptools.JSONRPCRequest{Method: "prompts/get", ID: mcptools.RawStringID("pg1"), Params: params})
		if err != nil {
			t.Fatalf("dispatch prompts/get: %v", err)
		}
		result, ok := payload["result"].(map[string]any)
		if !ok {
			t.Fatalf("missing prompts/get result: %+v", payload)
		}
		messages, ok := result["messages"].([]any)
		if !ok || len(messages) != 1 {
			t.Fatalf("unexpected messages: %+v", result)
		}
		first, _ := messages[0].(map[string]any)
		content, _ := first["content"].(map[string]any)
		if content["text"] != "hello world" {
			t.Fatalf("unexpected prompt message: %+v", first)
		}
	})

	t.Run("resources/list", func(t *testing.T) {
		payload, err := client.dispatch(ctx, mcptools.JSONRPCRequest{Method: "resources/list", ID: mcptools.RawStringID("rl1")})
		if err != nil {
			t.Fatalf("dispatch resources/list: %v", err)
		}
		result, ok := payload["result"].(map[string]any)
		if !ok {
			t.Fatalf("missing resources/list result: %+v", payload)
		}
		resources, ok := result["resources"].([]any)
		if !ok || len(resources) != 1 {
			t.Fatalf("unexpected resources: %+v", result)
		}
	})

	t.Run("resources/read", func(t *testing.T) {
		params, _ := json.Marshal(map[string]any{"uri": "test://doc"})
		payload, err := client.dispatch(ctx, mcptools.JSONRPCRequest{Method: "resources/read", ID: mcptools.RawStringID("rr1"), Params: params})
		if err != nil {
			t.Fatalf("dispatch resources/read: %v", err)
		}
		result, ok := payload["result"].(map[string]any)
		if !ok {
			t.Fatalf("missing resources/read result: %+v", payload)
		}
		contents, ok := result["contents"].([]any)
		if !ok || len(contents) != 1 {
			t.Fatalf("unexpected contents: %+v", result)
		}
		first, _ := contents[0].(map[string]any)
		if first["text"] != "doc-body" {
			t.Fatalf("unexpected resource contents: %+v", first)
		}
	})

	t.Run("list methods decode params (pagination cursor surface)", func(t *testing.T) {
		// A cursor with the wrong JSON type must be rejected by params decoding,
		// proving list params are forwarded instead of silently dropped.
		params, _ := json.Marshal(map[string]any{"cursor": 123})
		_, err := client.dispatch(ctx, mcptools.JSONRPCRequest{Method: "tools/list", ID: mcptools.RawStringID("tc1"), Params: params})
		if err == nil || !strings.Contains(err.Error(), "invalid tools/list params") {
			t.Fatalf("expected params decode error, got %v", err)
		}
	})

	t.Run("unknown method is method-not-found", func(t *testing.T) {
		_, err := client.dispatch(ctx, mcptools.JSONRPCRequest{Method: "experimental/custom", ID: mcptools.RawStringID("u1")})
		if !errors.Is(err, errMCPMethodNotFound) {
			t.Fatalf("expected errMCPMethodNotFound, got %v", err)
		}
	})
}

// When the container-side process dies (e.g. command not found), the failure
// must name the exit code and stderr — the regression this fixes is a bare
// "EOF" reaching the UI.
func TestMCPStdioClientEnrichError(t *testing.T) {
	t.Run("exit code and stderr tail", func(t *testing.T) {
		client := &mcpStdioClient{stderrTail: &mcpStderrTail{}}
		client.exitCode.Store(127)
		client.stderrTail.append("/bin/sh: 1: foo: not found")
		err := client.enrichError(io.EOF)
		msg := err.Error()
		if !strings.Contains(msg, "process exited with code 127") || !strings.Contains(msg, "foo: not found") {
			t.Fatalf("unhelpful error: %q", msg)
		}
	})

	t.Run("stderr tail without exit code keeps original error", func(t *testing.T) {
		client := &mcpStdioClient{stderrTail: &mcpStderrTail{}}
		client.exitCode.Store(-1)
		client.stderrTail.append("some warning")
		err := client.enrichError(io.EOF)
		if !strings.Contains(err.Error(), "EOF") || !strings.Contains(err.Error(), "some warning") {
			t.Fatalf("unexpected error: %q", err.Error())
		}
	})

	t.Run("nil error stays nil", func(t *testing.T) {
		client := &mcpStdioClient{stderrTail: &mcpStderrTail{}}
		if err := client.enrichError(nil); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("no diagnostics falls back to the original error", func(t *testing.T) {
		client := &mcpStdioClient{stderrTail: &mcpStderrTail{}}
		client.exitCode.Store(-1)
		if err := client.enrichError(io.EOF); !errors.Is(err, io.EOF) {
			t.Fatalf("expected io.EOF, got %v", err)
		}
	})
}

// Killing the server side mid-session must surface as a call error, not a hang.
func TestStdioClientServerDeath(t *testing.T) {
	clientStdin, clientStdout, serverStdin, serverStdout := newStdioPipePair()
	serveTestMCPServer(t, serverStdin, serverStdout)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := connectStdioClient(ctx, clientStdin, clientStdout)
	if err != nil {
		t.Fatalf("connectStdioClient: %v", err)
	}
	defer func() { _ = session.Close() }()

	// Simulate the process exiting: the bridge would close both pipes.
	_ = serverStdout.Close()
	_ = serverStdin.Close()

	if _, err := session.ListTools(ctx, &sdkmcp.ListToolsParams{}); err == nil {
		t.Fatal("expected error after server death, got nil")
	}
}

// buildShellCommand treats Command as ONE executable token; arguments smuggled
// into it stay quoted (the historical paste-whole-line trap), and flags belong
// in Args.
func TestBuildShellCommand(t *testing.T) {
	tests := []struct {
		name string
		req  MCPStdioRequest
		want string
	}{
		{
			name: "command only",
			req:  MCPStdioRequest{Command: "npx"},
			want: "npx",
		},
		{
			name: "command with args",
			req:  MCPStdioRequest{Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-everything"}},
			want: "npx -y @modelcontextprotocol/server-everything",
		},
		{
			name: "whole line pasted into command stays one quoted token",
			req:  MCPStdioRequest{Command: "npx -y pkg"},
			want: "'npx -y pkg'",
		},
		{
			name: "env pairs are sorted and values escaped",
			req:  MCPStdioRequest{Command: "srv", Env: map[string]string{"B_KEY": "2", "A_KEY": "a b"}},
			want: "A_KEY='a b' B_KEY=2 srv",
		},
		{
			name: "env value with comment char is quoted",
			req:  MCPStdioRequest{Command: "srv", Env: map[string]string{"TOKEN": "#abc"}},
			want: "TOKEN='#abc' srv",
		},
		{
			name: "cwd wraps the command",
			req:  MCPStdioRequest{Command: "srv", Cwd: "/opt/my dir"},
			want: "cd '/opt/my dir' && srv",
		},
		{
			name: "arg with single quote",
			req:  MCPStdioRequest{Command: "srv", Args: []string{"it's"}},
			want: `srv 'it'\''s'`,
		},
		{
			name: "empty command",
			req:  MCPStdioRequest{},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := buildShellCommand(tt.req); got != tt.want {
				t.Fatalf("buildShellCommand() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestJSONRPCResultPayload(t *testing.T) {
	payload := jsonrpcResultPayload(mcptools.RawStringID("abc"), map[string]any{"ok": true})
	if payload["jsonrpc"] != "2.0" || payload["id"] != "abc" {
		t.Fatalf("unexpected envelope: %+v", payload)
	}
	result, ok := payload["result"].(map[string]any)
	if !ok || result["ok"] != true {
		t.Fatalf("unexpected result: %+v", payload)
	}
}
