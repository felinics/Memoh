package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

const promptCancellationDrainTimeout = 3 * time.Second

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

func (c *clientConnection) NewSession(ctx context.Context, params acp.NewSessionRequest) (sessionResponse, error) {
	return acp.SendRequest[sessionResponse](c.conn, ctx, acp.AgentMethodSessionNew, params)
}

func (c *clientConnection) Prompt(ctx context.Context, params acp.PromptRequest) (acp.PromptResponse, error) {
	if err := ctx.Err(); err != nil {
		return acp.PromptResponse{}, err
	}

	// Do not bind the JSON-RPC request lifetime directly to the caller's Stop
	// context. acp-go-sdk removes a pending response as soon as that context is
	// cancelled; the later PromptResponse (and its notification watermark) is
	// then lost, so a warm runtime can be handed to the next turn while the old
	// turn is still producing events. Retain the request, send ACP session/cancel,
	// and require the same PromptResponse to finish inside a bounded grace period.
	type promptOutcome struct {
		response acp.PromptResponse
		err      error
	}
	requestCtx, cancelRequest := context.WithCancelCause(context.WithoutCancel(ctx))
	defer cancelRequest(nil)
	outcomes := make(chan promptOutcome, 1)
	go func() {
		resp, err := acp.SendRequest[acp.PromptResponse](c.conn, requestCtx, acp.AgentMethodSessionPrompt, params)
		outcomes <- promptOutcome{response: resp, err: err}
	}()

	select {
	case outcome := <-outcomes:
		if ctx.Err() == nil {
			return outcome.response, outcome.err
		}
		return c.finishCancelledPrompt(ctx, outcome, time.Now().Add(promptCancellationDrainTimeout))
	case <-ctx.Done():
		deadline := time.Now().Add(promptCancellationDrainTimeout)
		// Stop can race a response that has already crossed the wire. Prefer that
		// confirmed terminal boundary and avoid sending a session-wide cancel to
		// an Agent that is already idle.
		select {
		case outcome := <-outcomes:
			return c.finishCancelledPrompt(ctx, outcome, deadline)
		default:
		}
		// A blocked transport write must not defeat the drain deadline. Teardown
		// closes the process/pipe if this best-effort notification cannot finish.
		cancelDone := make(chan error, 1)
		go func() {
			cancelDone <- c.Cancel(context.WithoutCancel(ctx), acp.CancelNotification{SessionId: params.SessionId})
		}()

		timer := time.NewTimer(time.Until(deadline))
		defer timer.Stop()
		var (
			outcome       promptOutcome
			haveOutcome   bool
			haveCancel    bool
			cancelSendErr error
		)
		for !haveOutcome || !haveCancel {
			select {
			case outcome = <-outcomes:
				haveOutcome = true
			case cancelSendErr = <-cancelDone:
				haveCancel = true
			case <-timer.C:
				cancelRequest(ErrPromptCancellationUnconfirmed)
				return acp.PromptResponse{}, errors.Join(ctx.Err(), ErrPromptCancellationUnconfirmed)
			}
		}
		if cancelSendErr != nil {
			cancelRequest(ErrPromptCancellationUnconfirmed)
			return outcome.response, errors.Join(ctx.Err(), ErrPromptCancellationUnconfirmed, cancelSendErr)
		}
		return c.finishCancelledPrompt(ctx, outcome, deadline)
	}
}

func (c *clientConnection) finishCancelledPrompt(
	ctx context.Context,
	outcome struct {
		response acp.PromptResponse
		err      error
	},
	deadline time.Time,
) (acp.PromptResponse, error) {
	// Only a successful response proves the original Prompt reached a terminal
	// boundary. A transport or protocol error may have ended the wait without
	// ending the Agent turn, so make the pool recycle that runtime.
	if outcome.err != nil {
		return outcome.response, errors.Join(ctx.Err(), ErrPromptCancellationUnconfirmed, outcome.err)
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || !c.client.waitDecisionCallbacksIdle(remaining) {
		return outcome.response, errors.Join(ctx.Err(), ErrPromptCancellationUnconfirmed)
	}
	return outcome.response, ctx.Err()
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
		c.client.logger.Debug("ACP client method called", slog.String("method", method))
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
	case acp.ClientMethodElicitationCreate:
		var p createElicitationRequest
		if err := decodeACPParams(params, &p); err != nil {
			return nil, err
		}
		return callACPHandler(func() (any, error) { return c.client.CreateElicitation(ctx, p) })
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
