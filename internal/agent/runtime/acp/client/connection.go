package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/errs"
)

const promptCancellationDrainTimeout = 3 * time.Second

type clientConnection struct {
	conn    *acp.Connection
	updates *updateGate
	logger  *slog.Logger

	// ACP lets one connection host several sessions, and every inbound call
	// names the session it belongs to. sole is the single-session form: it
	// receives every call whatever ID the agent wrote. Without it, calls are
	// delivered by session ID. A session emits notifications before
	// session/new has returned its ID, so while one is opening the updates
	// for an unregistered ID are held, and it keeps its own once the ID is
	// known.
	mu       sync.RWMutex
	sole     *clientCallbacks
	sessions map[acp.SessionId]*clientCallbacks
	opening  bool
	held     []acp.SessionNotification
	// hooks receive the agent's own notifications by method, for the caller
	// that knows them (Conn.Subscribe).
	hooks map[string]func(json.RawMessage)
}

func (c *clientConnection) subscribe(method string, fn func(json.RawMessage)) {
	c.mu.Lock()
	if c.hooks == nil {
		c.hooks = map[string]func(json.RawMessage){}
	}
	c.hooks[method] = fn
	c.mu.Unlock()
}

func (c *clientConnection) hook(method string) func(json.RawMessage) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.hooks[method]
}

// newClientConnection serves a connection that hosts exactly one session.
func newClientConnection(client *clientCallbacks, peerInput io.Writer, peerOutput io.Reader) *clientConnection {
	c := &clientConnection{sole: client}
	if client != nil {
		c.logger = client.logger
	}
	c.updates = newUpdateGate(c.logger, peerOutput)
	c.conn = acp.NewConnection(c.handle, peerInput, c.updates)
	if client != nil && client.terminals != nil {
		client.terminals.fail = c.updates.fail
		client.terminals.logger = c.logger
	}
	return c
}

// newSharedClientConnection serves a connection whose sessions register by ID.
func newSharedClientConnection(logger *slog.Logger, peerInput io.Writer, peerOutput io.Reader) *clientConnection {
	c := &clientConnection{logger: logger, sessions: map[acp.SessionId]*clientCallbacks{}, updates: newUpdateGate(logger, peerOutput)}
	c.conn = acp.NewConnection(c.handle, peerInput, c.updates)
	return c
}

func (c *clientConnection) register(id acp.SessionId, callbacks *clientCallbacks) {
	c.mu.Lock()
	c.sessions[id] = callbacks
	c.mu.Unlock()
}

// unregister removes the session only while it still owns the ID, so closing
// a replaced session cannot drop its successor.
func (c *clientConnection) unregister(id acp.SessionId, callbacks *clientCallbacks) {
	c.mu.Lock()
	if c.sessions[id] == callbacks {
		delete(c.sessions, id)
	}
	c.mu.Unlock()
}

func (c *clientConnection) setOpening(opening bool) {
	c.mu.Lock()
	c.opening = opening
	c.held = nil
	c.mu.Unlock()
}

// opened registers the session session/new named and applies the updates it
// was sent before its ID was known. The lock keeps them ahead of the updates
// that follow.
func (c *clientConnection) opened(ctx context.Context, id acp.SessionId, callbacks *clientCallbacks) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessions[id] = callbacks
	for _, update := range c.held {
		// Another session's subagent also reports under an unregistered ID.
		if update.SessionId == id {
			_ = callbacks.SessionUpdate(ctx, update)
		}
	}
	c.opening = false
	c.held = nil
}

// callbacksFor resolves the receiver of a request. Requests act on behalf of
// a session, so an ID nobody registered is refused.
func (c *clientConnection) callbacksFor(id acp.SessionId) *clientCallbacks {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.sole != nil {
		return c.sole
	}
	return c.sessions[id]
}

// updatesFor resolves the receiver of a session/update. One that may belong
// to the session being opened is held for it instead.
func (c *clientConnection) updatesFor(update acp.SessionNotification) *clientCallbacks {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sole != nil {
		return c.sole
	}
	if callbacks := c.sessions[update.SessionId]; callbacks != nil {
		return callbacks
	}
	if c.opening {
		c.held = append(c.held, update)
	}
	return nil
}

// peerError marks an error from the Agent, or from the connection to it, as
// the dependency's. The SDK reports a request the caller canceled as a
// RequestError too; that one keeps this process's attribution.
func peerError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if external.IsPanic(err) {
		return err
	}
	if ctx.Err() != nil {
		return errs.WrapWithDepth(1, err, "")
	}
	return errs.WrapDependencyWithDepth(1, err, "")
}

func (c *clientConnection) Initialize(ctx context.Context, params acp.InitializeRequest) (acp.InitializeResponse, error) {
	resp, err := acp.SendRequest[acp.InitializeResponse](c.conn, ctx, acp.AgentMethodInitialize, params)
	return resp, peerError(ctx, err)
}

func (c *clientConnection) NewSession(ctx context.Context, params acp.NewSessionRequest) (sessionResponse, error) {
	resp, err := acp.SendRequest[sessionResponse](c.conn, ctx, acp.AgentMethodSessionNew, params)
	return resp, peerError(ctx, err)
}

func (c *clientConnection) ResumeSession(ctx context.Context, params acp.ResumeSessionRequest) (sessionResponse, error) {
	resp, err := acp.SendRequest[sessionResponse](c.conn, ctx, acp.AgentMethodSessionResume, params)
	return resp, peerError(ctx, err)
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
		defer func() {
			if r := recover(); r != nil {
				outcomes <- promptOutcome{err: external.RecoveredPanic(ctx, c.logger, "acp prompt", r)}
			}
		}()
		resp, err := acp.SendRequest[acp.PromptResponse](c.conn, requestCtx, acp.AgentMethodSessionPrompt, params)
		outcomes <- promptOutcome{response: resp, err: err}
	}()

	select {
	case outcome := <-outcomes:
		if ctx.Err() == nil {
			return outcome.response, peerError(ctx, outcome.err)
		}
		return c.finishCancelledPrompt(ctx, params.SessionId, outcome, time.Now().Add(promptCancellationDrainTimeout))
	case <-ctx.Done():
		deadline := time.Now().Add(promptCancellationDrainTimeout)
		// Stop can race a response that has already crossed the wire. Prefer that
		// confirmed terminal boundary and avoid sending a session-wide cancel to
		// an Agent that is already idle.
		select {
		case outcome := <-outcomes:
			return c.finishCancelledPrompt(ctx, params.SessionId, outcome, deadline)
		default:
		}
		// A blocked transport write must not defeat the drain deadline. Teardown
		// closes the process/pipe if this best-effort notification cannot finish.
		cancelDone := make(chan error, 1)
		go func() {
			defer func() {
				if r := recover(); r != nil {
					cancelDone <- external.RecoveredPanic(ctx, c.logger, "acp cancel", r)
				}
			}()
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
				if external.IsPanic(cancelSendErr) {
					cancelRequest(cancelSendErr)
					return acp.PromptResponse{}, cancelSendErr
				}
			case <-timer.C:
				cancelRequest(ErrPromptCancellationUnconfirmed)
				return acp.PromptResponse{}, errors.Join(ctx.Err(), ErrPromptCancellationUnconfirmed)
			}
		}
		if cancelSendErr != nil {
			cancelRequest(ErrPromptCancellationUnconfirmed)
			return outcome.response, errors.Join(ctx.Err(), ErrPromptCancellationUnconfirmed, cancelSendErr)
		}
		return c.finishCancelledPrompt(ctx, params.SessionId, outcome, deadline)
	}
}

func (c *clientConnection) finishCancelledPrompt(
	ctx context.Context,
	sessionID acp.SessionId,
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
	if remaining <= 0 || !c.callbacksFor(sessionID).waitDecisionCallbacksIdle(remaining) {
		return outcome.response, errors.Join(ctx.Err(), ErrPromptCancellationUnconfirmed)
	}
	return outcome.response, ctx.Err()
}

func (c *clientConnection) CloseSession(ctx context.Context, params acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	resp, err := acp.SendRequest[acp.CloseSessionResponse](c.conn, ctx, acp.AgentMethodSessionClose, params)
	return resp, peerError(ctx, err)
}

func (c *clientConnection) Cancel(ctx context.Context, params acp.CancelNotification) error {
	return peerError(ctx, c.conn.SendNotification(ctx, acp.AgentMethodSessionCancel, params))
}

func (c *clientConnection) SetSessionMode(ctx context.Context, params acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	resp, err := acp.SendRequest[acp.SetSessionModeResponse](c.conn, ctx, acp.AgentMethodSessionSetMode, params)
	return resp, peerError(ctx, err)
}

func (c *clientConnection) SetSessionConfigOption(ctx context.Context, params acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	resp, err := acp.SendRequest[acp.SetSessionConfigOptionResponse](c.conn, ctx, acp.AgentMethodSessionSetConfigOption, params)
	if err != nil {
		return resp, peerError(ctx, err)
	}
	if err := (&resp).Validate(); err != nil {
		return resp, errs.WrapDependency(err, "validate session config response")
	}
	return resp, nil
}

func (c *clientConnection) SetLegacySessionModel(ctx context.Context, params legacySetSessionModelRequest) (legacySetSessionModelResponse, error) {
	resp, err := acp.SendRequest[legacySetSessionModelResponse](c.conn, ctx, legacyAgentMethodSessionSetModel, params)
	return resp, peerError(ctx, err)
}

func (c *clientConnection) handle(ctx context.Context, method string, params json.RawMessage) (result any, reqErr *acp.RequestError) {
	defer func() {
		if r := recover(); r != nil {
			err := external.RecoveredPanic(ctx, c.logger, "acp dispatch", r)
			c.updates.fail(err)
			result, reqErr = nil, acp.NewInternalError(nil)
		}
	}()
	if c == nil {
		return nil, acp.NewInternalError(map[string]any{"error": "ACP client callbacks not configured"})
	}
	if method == acp.ClientMethodSessionUpdate {
		c.updates.release()
	}
	if c.sole == nil && c.sessions == nil {
		return nil, acp.NewInternalError(map[string]any{"error": "ACP client callbacks not configured"})
	}
	if c.logger != nil && method != acp.ClientMethodSessionUpdate {
		c.logger.DebugContext(ctx, "ACP client method called", slog.String("method", method))
	}
	switch method {
	case acp.ClientMethodFsReadTextFile:
		var p acp.ReadTextFileRequest
		if err := decodeACPParams(params, &p); err != nil {
			return nil, err
		}
		callbacks, reqErr := c.workspaceCallbacks(method, p.SessionId)
		if reqErr != nil {
			return nil, reqErr
		}
		return callACPHandler(func() (any, error) { return callbacks.ReadTextFile(ctx, p) })
	case acp.ClientMethodFsWriteTextFile:
		var p acp.WriteTextFileRequest
		if err := decodeACPParams(params, &p); err != nil {
			return nil, err
		}
		callbacks, reqErr := c.workspaceCallbacks(method, p.SessionId)
		if reqErr != nil {
			return nil, reqErr
		}
		return callACPHandler(func() (any, error) { return callbacks.WriteTextFile(ctx, p) })
	case acp.ClientMethodSessionRequestPermission:
		var p acp.RequestPermissionRequest
		if err := decodeACPParams(params, &p); err != nil {
			return nil, err
		}
		callbacks, reqErr := c.sessionCallbacks(p.SessionId)
		if reqErr != nil {
			return nil, reqErr
		}
		return callACPHandler(func() (any, error) { return callbacks.RequestPermission(ctx, p) })
	case acp.ClientMethodElicitationCreate:
		var p createElicitationRequest
		if err := decodeACPParams(params, &p); err != nil {
			return nil, err
		}
		callbacks, reqErr := c.sessionCallbacks(acp.SessionId(p.SessionID))
		if reqErr != nil {
			return nil, reqErr
		}
		return callACPHandler(func() (any, error) { return callbacks.CreateElicitation(ctx, p) })
	case acp.ClientMethodSessionUpdate:
		var p acp.SessionNotification
		if err := decodeACPParams(params, &p); err != nil {
			return nil, err
		}
		callbacks := c.updatesFor(p)
		if callbacks == nil {
			// Held for the session being opened, or nobody reads this ID: a
			// closed session's late update, or a session the agent runs
			// internally, such as a subagent's.
			return nil, nil
		}
		return callACPHandler(func() (any, error) { return nil, callbacks.SessionUpdate(ctx, p) })
	case acp.ClientMethodTerminalCreate:
		var p acp.CreateTerminalRequest
		if err := decodeACPParams(params, &p); err != nil {
			return nil, err
		}
		callbacks, reqErr := c.workspaceCallbacks(method, p.SessionId)
		if reqErr != nil {
			return nil, reqErr
		}
		return callACPHandler(func() (any, error) { return callbacks.CreateTerminal(ctx, p) })
	case acp.ClientMethodTerminalKill:
		var p acp.KillTerminalRequest
		if err := decodeACPParams(params, &p); err != nil {
			return nil, err
		}
		callbacks, reqErr := c.workspaceCallbacks(method, p.SessionId)
		if reqErr != nil {
			return nil, reqErr
		}
		return callACPHandler(func() (any, error) { return callbacks.KillTerminal(ctx, p) })
	case acp.ClientMethodTerminalOutput:
		var p acp.TerminalOutputRequest
		if err := decodeACPParams(params, &p); err != nil {
			return nil, err
		}
		callbacks, reqErr := c.workspaceCallbacks(method, p.SessionId)
		if reqErr != nil {
			return nil, reqErr
		}
		return callACPHandler(func() (any, error) { return callbacks.TerminalOutput(ctx, p) })
	case acp.ClientMethodTerminalRelease:
		var p acp.ReleaseTerminalRequest
		if err := decodeACPParams(params, &p); err != nil {
			return nil, err
		}
		callbacks, reqErr := c.workspaceCallbacks(method, p.SessionId)
		if reqErr != nil {
			return nil, reqErr
		}
		return callACPHandler(func() (any, error) { return callbacks.ReleaseTerminal(ctx, p) })
	case acp.ClientMethodTerminalWaitForExit:
		var p acp.WaitForTerminalExitRequest
		if err := decodeACPParams(params, &p); err != nil {
			return nil, err
		}
		callbacks, reqErr := c.workspaceCallbacks(method, p.SessionId)
		if reqErr != nil {
			return nil, reqErr
		}
		return callACPHandler(func() (any, error) { return callbacks.WaitForTerminalExit(ctx, p) })
	default:
		if hook := c.hook(method); hook != nil {
			hook(params)
			return nil, nil
		}
		var p struct {
			SessionID acp.SessionId `json:"sessionId"`
		}
		if json.Unmarshal(params, &p) == nil {
			if callbacks := c.callbacksFor(p.SessionID); callbacks != nil && callbacks.userQuestions != nil && callbacks.userQuestions.Method == method {
				return callACPHandler(func() (any, error) { return callbacks.AskUserQuestions(ctx, params) })
			}
		}
		return nil, acp.NewMethodNotFound(method)
	}
}

func (c *clientConnection) sessionCallbacks(id acp.SessionId) (*clientCallbacks, *acp.RequestError) {
	if callbacks := c.callbacksFor(id); callbacks != nil {
		return callbacks, nil
	}
	return nil, acp.NewInvalidParams(map[string]any{"error": "unknown session"})
}

// workspaceCallbacks resolves the receiver of a client file or terminal call.
// A session opened without a workspace bridge never advertised those
// capabilities, so the method does not exist for it.
func (c *clientConnection) workspaceCallbacks(method string, id acp.SessionId) (*clientCallbacks, *acp.RequestError) {
	callbacks, reqErr := c.sessionCallbacks(id)
	if reqErr != nil {
		return nil, reqErr
	}
	if callbacks.terminals == nil {
		return nil, acp.NewMethodNotFound(method)
	}
	return callbacks, nil
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
