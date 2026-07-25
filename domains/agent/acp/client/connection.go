package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"

	acp "github.com/coder/acp-go-sdk"
)

type clientConnection struct {
	conn   *acp.Connection
	client *clientCallbacks
}

func newClientConnection(client *clientCallbacks, peerInput io.Writer, peerOutput io.Reader) *clientConnection {
	c := &clientConnection{client: client}
	c.conn = acp.NewConnection(c.handle, peerInput, peerOutput)
	return c
}

func (c *clientConnection) Initialize(ctx context.Context, params acp.InitializeRequest) (acp.InitializeResponse, error) {
	return acp.SendRequest[acp.InitializeResponse](c.conn, ctx, acp.AgentMethodInitialize, params)
}

func (c *clientConnection) NewSession(ctx context.Context, params acp.NewSessionRequest) (newSessionResponse, error) {
	return acp.SendRequest[newSessionResponse](c.conn, ctx, acp.AgentMethodSessionNew, params)
}

func (c *clientConnection) Prompt(ctx context.Context, params acp.PromptRequest) (acp.PromptResponse, error) {
	resp, err := acp.SendRequest[acp.PromptResponse](c.conn, ctx, acp.AgentMethodSessionPrompt, params)
	if err != nil && ctx.Err() != nil {
		_ = c.Cancel(context.WithoutCancel(ctx), acp.CancelNotification{SessionId: params.SessionId})
	}
	return resp, err
}

func (c *clientConnection) CloseSession(ctx context.Context, params acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	return acp.SendRequest[acp.CloseSessionResponse](c.conn, ctx, acp.AgentMethodSessionClose, params)
}

func (c *clientConnection) Cancel(ctx context.Context, params acp.CancelNotification) error {
	return c.conn.SendNotification(ctx, acp.AgentMethodSessionCancel, params)
}

func (c *clientConnection) SetSessionMode(ctx context.Context, params acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SendRequest[acp.SetSessionModeResponse](c.conn, ctx, acp.AgentMethodSessionSetMode, params)
}

func (c *clientConnection) SetSessionConfigOption(ctx context.Context, params acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	resp, err := acp.SendRequest[acp.SetSessionConfigOptionResponse](c.conn, ctx, acp.AgentMethodSessionSetConfigOption, params)
	if err != nil {
		return resp, err
	}
	if err := (&resp).Validate(); err != nil {
		return resp, fmt.Errorf("validate session config response: %w", err)
	}
	return resp, nil
}

func (c *clientConnection) SetLegacySessionModel(ctx context.Context, params legacySetSessionModelRequest) (legacySetSessionModelResponse, error) {
	return acp.SendRequest[legacySetSessionModelResponse](c.conn, ctx, legacyAgentMethodSessionSetModel, params)
}

func (c *clientConnection) handle(ctx context.Context, method string, params json.RawMessage) (any, *acp.RequestError) {
	if c == nil || c.client == nil {
		return nil, acp.NewInternalError(map[string]any{"error": "ACP client callbacks not configured"})
	}
	if c.client.logger != nil && method != acp.ClientMethodSessionUpdate {
		c.client.logger.DebugContext(ctx, "ACP client method called", slog.String("method", method))
	}
	switch method {
	case acp.ClientMethodFsReadTextFile:
		var p acp.ReadTextFileRequest
		if err := decodeACPParams(params, &p); err != nil {
			return nil, err
		}
		return callACPHandler(func() (any, error) { return c.client.ReadTextFile(ctx, p) })
	case acp.ClientMethodFsWriteTextFile:
		var p acp.WriteTextFileRequest
		if err := decodeACPParams(params, &p); err != nil {
			return nil, err
		}
		return callACPHandler(func() (any, error) { return c.client.WriteTextFile(ctx, p) })
	case acp.ClientMethodSessionRequestPermission:
		var p acp.RequestPermissionRequest
		if err := decodeACPParams(params, &p); err != nil {
			return nil, err
		}
		return callACPHandler(func() (any, error) { return c.client.RequestPermission(ctx, p) })
	case acp.ClientMethodSessionUpdate:
		var p acp.SessionNotification
		if err := decodeACPParams(params, &p); err != nil {
			return nil, err
		}
		return callACPHandler(func() (any, error) { return nil, c.client.SessionUpdate(ctx, p) })
	case acp.ClientMethodTerminalCreate:
		var p acp.CreateTerminalRequest
		if err := decodeACPParams(params, &p); err != nil {
			return nil, err
		}
		return callACPHandler(func() (any, error) { return c.client.CreateTerminal(ctx, p) })
	case acp.ClientMethodTerminalKill:
		var p acp.KillTerminalRequest
		if err := decodeACPParams(params, &p); err != nil {
			return nil, err
		}
		return callACPHandler(func() (any, error) { return c.client.KillTerminal(ctx, p) })
	case acp.ClientMethodTerminalOutput:
		var p acp.TerminalOutputRequest
		if err := decodeACPParams(params, &p); err != nil {
			return nil, err
		}
		return callACPHandler(func() (any, error) { return c.client.TerminalOutput(ctx, p) })
	case acp.ClientMethodTerminalRelease:
		var p acp.ReleaseTerminalRequest
		if err := decodeACPParams(params, &p); err != nil {
			return nil, err
		}
		return callACPHandler(func() (any, error) { return c.client.ReleaseTerminal(ctx, p) })
	case acp.ClientMethodTerminalWaitForExit:
		var p acp.WaitForTerminalExitRequest
		if err := decodeACPParams(params, &p); err != nil {
			return nil, err
		}
		return callACPHandler(func() (any, error) { return c.client.WaitForTerminalExit(ctx, p) })
	default:
		return nil, acp.NewMethodNotFound(method)
	}
}

type acpValidatable interface {
	Validate() error
}

func decodeACPParams[T acpValidatable](params json.RawMessage, out T) *acp.RequestError {
	if err := json.Unmarshal(params, out); err != nil {
		return acp.NewInvalidParams(map[string]any{"error": err.Error()})
	}
	if err := out.Validate(); err != nil {
		return acp.NewInvalidParams(map[string]any{"error": err.Error()})
	}
	return nil
}

func callACPHandler(fn func() (any, error)) (any, *acp.RequestError) {
	resp, err := fn()
	if err != nil {
		var reqErr *acp.RequestError
		if errors.As(err, &reqErr) {
			return nil, reqErr
		}
		return nil, acp.NewInternalError(map[string]any{"error": err.Error()})
	}
	return resp, nil
}
