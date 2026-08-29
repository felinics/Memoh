package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

const (
	defaultTerminalOutputLimit = 128 * 1024
	maxTerminalOutputLimit     = 1024 * 1024
	defaultTerminalTimeout     = int32(600)
	terminalReleaseGrace       = 200 * time.Millisecond
)

type terminalManager struct {
	ctx        context.Context
	client     *bridge.Client
	root       string
	defaultCwd string
	timeout    int32
	baseEnv    []string
	cleanEnv   bool
	unsetEnv   []string
	events     *toolEventEmitter
	limit      ToolOutputLimit

	mu        sync.Mutex
	nextID    int
	terminals map[string]*terminal
}

type (
	terminalApprovalFunc func(toolCallID string, input map[string]any) (terminalApprovalResult, error)
	terminalRuntimeGuard func(context.Context) error
)

type terminalRuntimeScope struct {
	validate   terminalRuntimeGuard
	runContext context.Context
}

type terminalApprovalResult struct {
	Approved   bool
	ToolCallID string
	// RejectionMessage is the agent-visible text for an unapproved result.
	RejectionMessage string
}

type terminal struct {
	stream         *bridge.ExecStream
	outputLimit    ToolOutputLimit
	id             string
	input          map[string]any
	releaseContext func()

	mu        sync.Mutex
	output    string
	truncated bool
	exitCode  *int
	signal    *string
	reported  bool
	// endReported is closed after the winning emitTerminalEnd call has
	// actually emitted the tool_call_end event, so concurrent callers (the
	// readLoop drain goroutine vs WaitForTerminalExit) can rely on the event
	// being visible once emitTerminalEnd returns.
	endReported chan struct{}
	done        chan struct{}
	doneOnce    sync.Once
	onDone      func(*terminal)
}

func newTerminalManager(ctx context.Context, client *bridge.Client, root, defaultCwd string, timeoutSeconds int32, baseEnv []string, cleanEnv bool, unsetEnv []string, events *toolEventEmitter) *terminalManager { //nolint:contextcheck // terminal streams must live for the ACP turn, not a single RPC callback.
	if timeoutSeconds <= 0 {
		timeoutSeconds = defaultTerminalTimeout
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return &terminalManager{
		ctx:        ctx,
		client:     client,
		root:       root,
		defaultCwd: defaultCwd,
		timeout:    timeoutSeconds,
		baseEnv:    append([]string(nil), baseEnv...),
		cleanEnv:   cleanEnv,
		unsetEnv:   append([]string(nil), unsetEnv...),
		events:     events,
		terminals:  map[string]*terminal{},
	}
}

func (m *terminalManager) setToolOutputLimit(limit ToolOutputLimit) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.limit = limit
	m.mu.Unlock()
}

func (m *terminalManager) CreateTerminal(ctx context.Context, p acp.CreateTerminalRequest, approve terminalApprovalFunc, runtimeScope terminalRuntimeScope) (acp.CreateTerminalResponse, error) {
	cwd := m.defaultCwd
	if p.Cwd != nil && strings.TrimSpace(*p.Cwd) != "" {
		resolved, err := m.resolvePath(*p.Cwd)
		if err != nil {
			return acp.CreateTerminalResponse{}, err
		}
		cwd = resolved
	}
	command := buildShellCommand(p.Command, p.Args)
	if strings.TrimSpace(command) == "" {
		return acp.CreateTerminalResponse{}, errors.New("terminal command is required")
	}
	id := m.nextTerminalID()
	input := map[string]any{
		"command": command,
	}
	if p.Cwd != nil && strings.TrimSpace(*p.Cwd) != "" {
		input["cwd"] = strings.TrimSpace(*p.Cwd)
	}
	toolCallID := "terminal-" + id
	if approve != nil {
		approval, err := approve(toolCallID, input)
		if err != nil {
			m.emitToolCallStart(toolCallID, "exec", input)
			m.emitToolCallEnd(toolCallID, "exec", input, toolErrorResult(err), err)
			return acp.CreateTerminalResponse{}, err
		}
		if strings.TrimSpace(approval.ToolCallID) != "" {
			toolCallID = strings.TrimSpace(approval.ToolCallID)
		}
		if !approval.Approved {
			message := strings.TrimSpace(approval.RejectionMessage)
			if message == "" {
				message = "tool execution was not approved"
			}
			err := errors.New(message)
			m.emitToolCallStart(toolCallID, "exec", input)
			m.emitToolCallEnd(toolCallID, "exec", input, toolErrorResult(err), err)
			return acp.CreateTerminalResponse{}, err
		}
	}
	m.emitToolCallStart(toolCallID, "exec", input)

	outputLimit := ToolOutputLimit{MaxBytes: defaultTerminalOutputLimit}
	if p.OutputByteLimit != nil && *p.OutputByteLimit > 0 {
		outputLimit.MaxBytes = *p.OutputByteLimit
		if outputLimit.MaxBytes > maxTerminalOutputLimit {
			outputLimit.MaxBytes = maxTerminalOutputLimit
		}
	}
	if promptLimit := m.toolOutputLimit(); hasToolOutputLimit(promptLimit) {
		normalized := normalizedToolOutputLimit(promptLimit)
		if normalized.MaxBytes > 0 && normalized.MaxBytes < outputLimit.MaxBytes {
			outputLimit.MaxBytes = normalized.MaxBytes
		}
		if normalized.MaxLines > 0 {
			outputLimit.MaxLines = normalized.MaxLines
		}
	}

	env := append([]string(nil), m.baseEnv...)
	for _, item := range p.Env {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		if envNameBlocked(name, m.unsetEnv) {
			continue
		}
		env = append(env, name+"="+item.Value)
	}
	if runtimeScope.validate != nil {
		if err := runtimeScope.validate(ctx); err != nil {
			m.emitToolCallEnd(toolCallID, "exec", input, toolErrorResult(err), err)
			return acp.CreateTerminalResponse{}, err
		}
	}
	execCtx := m.ctx
	releaseExecContext := func() {}
	if runtimeScope.runContext != nil {
		var cancelExec context.CancelFunc
		execCtx, cancelExec = context.WithCancel(m.ctx)
		stopRunCancel := context.AfterFunc(runtimeScope.runContext, cancelExec) //nolint:contextcheck // The owning run is deliberately independent from the ACP callback context.
		if runtimeScope.runContext.Err() != nil {
			cancelExec()
		}
		releaseExecContext = func() {
			stopRunCancel()
			cancelExec()
		}
	}
	stream, err := m.client.ExecStreamWithOptions(execCtx, command, cwd, m.timeout, bridge.ExecOptions{ //nolint:contextcheck // execution outlives the create RPC but not its owning run.
		Env:      env,
		CleanEnv: m.cleanEnv,
		UnsetEnv: m.unsetEnv,
	})
	if err != nil {
		releaseExecContext()
		m.emitToolCallEnd(toolCallID, "exec", input, toolErrorResult(err), err)
		return acp.CreateTerminalResponse{}, err
	}

	term := &terminal{stream: stream, outputLimit: outputLimit, id: toolCallID, input: input, releaseContext: releaseExecContext, done: make(chan struct{}), endReported: make(chan struct{}), onDone: m.emitTerminalEnd}
	m.mu.Lock()
	m.terminals[id] = term
	m.mu.Unlock()

	go term.readLoop()
	return acp.CreateTerminalResponse{TerminalId: id}, nil
}

func (m *terminalManager) resolvePath(path string) (string, error) {
	return ResolvePathUnderVirtualRoot(m.root, path)
}

func (m *terminalManager) KillTerminal(_ context.Context, p acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	term, err := m.get(p.TerminalId)
	if err != nil {
		return acp.KillTerminalResponse{}, err
	}
	term.kill("killed")
	m.emitTerminalEnd(term)
	return acp.KillTerminalResponse{}, nil
}

func (m *terminalManager) TerminalOutput(_ context.Context, p acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	term, err := m.get(p.TerminalId)
	if err != nil {
		return acp.TerminalOutputResponse{}, err
	}
	output, truncated, status := term.snapshot()
	limitedOutput := m.limitTerminalOutput(output)
	if limitedOutput != output {
		truncated = true
	}
	output = limitedOutput
	if output == "" {
		output = "\n"
	}
	return acp.TerminalOutputResponse{Output: output, Truncated: truncated, ExitStatus: status}, nil
}

func (m *terminalManager) ReleaseTerminal(_ context.Context, p acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	term, err := m.remove(p.TerminalId)
	if err != nil {
		return acp.ReleaseTerminalResponse{}, err
	}
	if !term.waitDone(terminalReleaseGrace) {
		term.kill("released")
	}
	m.emitTerminalEnd(term)
	return acp.ReleaseTerminalResponse{}, nil
}

func (m *terminalManager) WaitForTerminalExit(ctx context.Context, p acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	term, err := m.get(p.TerminalId)
	if err != nil {
		return acp.WaitForTerminalExitResponse{}, err
	}
	select {
	case <-term.done:
	case <-ctx.Done():
		return acp.WaitForTerminalExitResponse{}, ctx.Err()
	}
	code, signal := term.exit()
	m.emitTerminalEnd(term)
	return acp.WaitForTerminalExitResponse{ExitCode: code, Signal: signal}, nil
}

func (m *terminalManager) killAll() {
	m.mu.Lock()
	terms := make([]*terminal, 0, len(m.terminals))
	for id, term := range m.terminals {
		terms = append(terms, term)
		delete(m.terminals, id)
	}
	m.mu.Unlock()
	for _, term := range terms {
		term.kill("closed")
		m.emitTerminalEnd(term)
	}
}

func (m *terminalManager) emitToolCallStart(id, name string, input map[string]any) {
	if m == nil || m.events == nil {
		return
	}
	m.events.emit(event.StreamEvent{
		Type:       event.ToolCallStart,
		ToolCallID: id,
		ToolName:   name,
		Input:      input,
	})
}

func (m *terminalManager) emitToolCallEnd(id, name string, input map[string]any, result any, err error) {
	if m == nil || m.events == nil {
		return
	}
	ev := event.StreamEvent{
		Type:       event.ToolCallEnd,
		ToolCallID: id,
		ToolName:   name,
		Input:      input,
		Result:     result,
	}
	if err != nil {
		ev.Error = err.Error()
	}
	m.events.emit(ev)
}

func (m *terminalManager) emitTerminalEnd(term *terminal) {
	if m == nil || term == nil {
		return
	}
	if !term.markReported() {
		// Another caller won the report race; wait until its end event is
		// emitted so this call's caller observes the event too.
		if term.endReported != nil {
			<-term.endReported
		}
		return
	}
	if term.endReported != nil {
		defer close(term.endReported)
	}
	output, truncated, status := term.snapshot()
	limitedOutput := m.limitTerminalOutput(output)
	if limitedOutput != output {
		truncated = true
	}
	output = limitedOutput
	result := map[string]any{
		"stdout":    output,
		"truncated": truncated,
	}
	if status != nil {
		if status.ExitCode != nil {
			result["exit_code"] = *status.ExitCode
		}
		if status.Signal != nil {
			result["signal"] = *status.Signal
		}
	}
	m.emitToolCallEnd(term.id, "exec", term.input, result, nil)
}

func (m *terminalManager) toolOutputLimit() ToolOutputLimit {
	if m == nil {
		return ToolOutputLimit{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.limit
}

func (m *terminalManager) limitTerminalOutput(output string) string {
	limit := m.toolOutputLimit()
	if !hasToolOutputLimit(limit) {
		return output
	}
	return limitToolOutputString(output, "tool result (exec)", limit)
}

func (m *terminalManager) nextTerminalID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	return fmt.Sprintf("term-%d", m.nextID)
}

func (m *terminalManager) get(id string) (*terminal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	term := m.terminals[id]
	if term == nil {
		return nil, fmt.Errorf("terminal %q not found", id)
	}
	return term, nil
}

func (m *terminalManager) remove(id string) (*terminal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	term := m.terminals[id]
	if term == nil {
		return nil, fmt.Errorf("terminal %q not found", id)
	}
	delete(m.terminals, id)
	return term, nil
}

func (t *terminal) readLoop() {
	defer func() {
		if t.onDone != nil {
			t.onDone(t)
		}
	}()
	for {
		output, err := t.stream.Recv()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				sig := "stream_error"
				t.finish(nil, &sig)
			} else {
				code := 0
				t.finish(&code, nil)
			}
			return
		}
		switch output.GetStream() {
		case pb.ExecOutput_STDOUT, pb.ExecOutput_STDERR:
			t.appendOutput(string(output.GetData()))
		case pb.ExecOutput_EXIT:
			code := int(output.GetExitCode())
			t.finish(&code, nil)
			return
		}
	}
}

func (t *terminal) waitDone(timeout time.Duration) bool {
	if t == nil {
		return true
	}
	if timeout <= 0 {
		select {
		case <-t.done:
			return true
		default:
			return false
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-t.done:
		return true
	case <-timer.C:
		return false
	}
}

func (t *terminal) appendOutput(s string) {
	if s == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.output += s
	if hasToolOutputLimit(t.outputLimit) && limitToolOutputStringExact(t.output, "tool result (exec)", t.outputLimit) != t.output {
		t.truncated = true
	}
	if len(t.output) > maxTerminalOutputLimit {
		limited := limitToolOutputStringExact(t.output, "tool result (exec)", ToolOutputLimit{
			MaxBytes: maxTerminalOutputLimit,
			MaxLines: t.outputLimit.MaxLines,
		})
		if limited == t.output {
			return
		}
		t.truncated = true
		t.output = limited
	}
}

func (t *terminal) snapshot() (string, bool, *acp.TerminalExitStatus) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var status *acp.TerminalExitStatus
	if t.exitCode != nil || t.signal != nil {
		status = &acp.TerminalExitStatus{ExitCode: t.exitCode, Signal: t.signal}
	}
	output := t.output
	truncated := t.truncated
	if hasToolOutputLimit(t.outputLimit) {
		limited := limitToolOutputStringExact(output, "tool result (exec)", t.outputLimit)
		if limited != output {
			output = limited
			truncated = true
		}
	}
	return output, truncated, status
}

func (t *terminal) exit() (*int, *string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.exitCode, t.signal
}

func (t *terminal) markReported() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.reported {
		return false
	}
	t.reported = true
	return true
}

func (t *terminal) kill(signal string) {
	_ = t.stream.Close()
	t.finish(nil, &signal)
}

func (t *terminal) finish(code *int, signal *string) {
	t.doneOnce.Do(func() {
		if t.releaseContext != nil {
			t.releaseContext()
		}
		t.mu.Lock()
		if code != nil {
			v := *code
			t.exitCode = &v
		}
		if signal != nil {
			v := *signal
			t.signal = &v
		}
		t.mu.Unlock()
		close(t.done)
	})
}
