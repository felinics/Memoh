package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/felinics/memoh/internal/agent/decision/approval"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/external/toolmount"
	"github.com/felinics/memoh/internal/errs"
)

// sessionDrainTimeout bounds how long a resident CLI may take to flush its
// durable writes once its input ends.
const sessionDrainTimeout = 10 * time.Second

// cliSession is one Claude Code process serving a conversation's turns, one
// at a time. It owns what lasts as long as the process: the control channel,
// the read loop and the Memoh tool route. A turn attaches for its duration
// and receives everything the CLI sends meanwhile.
type cliSession struct {
	proc   cliProcess
	logger *slog.Logger

	nextID atomic.Uint64

	mu      sync.Mutex
	pending map[string]chan controlResult
	turn    *turnRunner
	// nativeID is the CLI session the process holds, once a turn's init
	// reported it.
	nativeID string
	// model and effort are what the process last applied, and mode the
	// permission mode it was last seen in; a turn changes them only when it
	// asks for something else.
	model, effort, mode string

	// capabilities is the CLI's answer to initialize, asked once per process.
	capabilities initializeResponse
	// version is the CLI's own, when it said.
	version string
	// mount serves Memoh tools to the process; nil when it could not be
	// mounted, and every turn then says the tools are unavailable.
	mount *toolmount.Mount

	readDone chan struct{}
}

func newCLISession(proc cliProcess, logger *slog.Logger) *cliSession {
	return &cliSession{
		proc:     proc,
		logger:   logger,
		pending:  map[string]chan controlResult{},
		readDone: make(chan struct{}),
	}
}

// Done reports the process can serve no more turns: it exited, its output
// ended, or it was ended for no longer answering.
func (s *cliSession) Done() <-chan struct{} { return s.proc.Done() }

// Busy keeps the process while a turn is attached.
func (s *cliSession) Busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turn != nil
}

// Close ends the input so the CLI flushes its session and exits; a CLI that
// does not is killed.
func (s *cliSession) Close() error {
	if s.mount != nil {
		s.mount.Stop()
	}
	err := drainCLI(s.proc, sessionDrainTimeout)
	_ = s.proc.Close()
	return err
}

// beginTurn attaches a new turn to the process.
func (s *cliSession) beginTurn(parent context.Context, input external.PromptInput, approvalSvc approval.FlowService, waiter func(string) func()) *turnRunner {
	t := newTurn(parent, s, input, approvalSvc, waiter)
	s.mu.Lock()
	s.turn = t
	s.mu.Unlock()
	return t
}

func (s *cliSession) endTurn(t *turnRunner) {
	s.mu.Lock()
	if s.turn == t {
		s.turn = nil
	}
	s.mu.Unlock()
}

func (s *cliSession) currentTurn() *turnRunner {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turn
}

func (s *cliSession) heldSession() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nativeID
}

// writeLine sends a line to the CLI; it never waits on the CLI (see
// agentproc.Child.Write).
func (s *cliSession) writeLine(line []byte) error {
	_, err := s.proc.Write(append(line, '\n'))
	return errs.WrapDependency(err, "")
}

// end kills a CLI that stopped answering: what it last applied or holds is
// no longer known. It is out of service at once, so the next turn starts
// another process.
func (s *cliSession) end() { s.proc.Kill() }

// callControl bounds every handshake and removes abandoned waiters. Wire
// errors remain private diagnostics and are mapped at the application
// boundary.
func (s *cliSession) callControl(ctx context.Context, subtype string, extra map[string]any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	id := "memoh-" + strconv.FormatUint(s.nextID.Add(1), 10)
	line, err := controlRequestLine(id, subtype, extra)
	if err != nil {
		return nil, err
	}
	ch := make(chan controlResult, 1)
	s.mu.Lock()
	s.pending[id] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
	}()
	if err := s.writeLine(line); err != nil {
		return nil, err
	}
	select {
	case result := <-ch:
		return result.response, result.err
	case <-ctx.Done():
		// The CLI may still apply the request, or answer it, after nothing
		// waits for the answer.
		s.end()
		return nil, ctx.Err()
	case <-s.readDone:
		return nil, s.processError(subtype)
	}
}

func (s *cliSession) processError(operation string) error {
	return errors.Join(
		errs.NewDependency(fmt.Sprintf("claude %s: stream ended; stderr: %s", operation, s.proc.StderrTail())),
		s.proc.Err(),
	)
}

func (s *cliSession) resolveControl(msg *inboundMessage) {
	var envelope struct {
		Subtype   string          `json:"subtype"`
		RequestID string          `json:"request_id"`
		Response  json.RawMessage `json:"response"`
		Error     string          `json:"error"`
	}
	if err := json.Unmarshal(msg.Response, &envelope); err != nil {
		return
	}
	s.mu.Lock()
	ch := s.pending[envelope.RequestID]
	delete(s.pending, envelope.RequestID)
	s.mu.Unlock()
	if ch == nil {
		return
	}
	result := controlResult{response: envelope.Response}
	if envelope.Subtype != "success" {
		result.err = errs.WrapDependency(&controlRejection{diagnostic: fmt.Sprintf("claude control %s: %s", envelope.Subtype, envelope.Error)}, "")
	}
	ch <- result
}

// readLoop consumes the CLI's NDJSON stream until the process exits, handing
// each message to the attached turn.
func (s *cliSession) readLoop() {
	defer close(s.readDone)
	// Output ends with the process; whatever ended it, nothing is left to
	// serve.
	defer s.proc.Kill()
	scanner := bufio.NewScanner(s.proc)
	scanner.Buffer(make([]byte, 64*1024), maxLineBytes)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		msg, err := decodeInbound(line)
		if err != nil {
			// The CLI occasionally writes plain text on stdout; survivable.
			s.logger.Warn("claude: undecodable line", slog.String("line", truncateForLog(line)))
			continue
		}
		if msg.Type == messageTypeControlResponse {
			s.resolveControl(msg)
			continue
		}
		if t := s.currentTurn(); t != nil {
			t.handleMessage(msg)
		} else if msg.Type == messageTypeControlRequest {
			// Nothing can decide a request that arrives between turns.
			s.respondControlError(msg.RequestID, "no turn is running")
		}
	}
	t := s.currentTurn()
	if t == nil {
		return
	}
	if err := scanner.Err(); err != nil {
		t.observeExit(errs.WrapDependency(err, ""))
	}
	// Output ends with the process; a turn still waiting learns how.
	select {
	case <-s.proc.Done():
		t.observeExit(s.proc.Err())
	default:
	}
	t.finish()
}

func (s *cliSession) respondControlError(requestID, message string) {
	if line, err := controlErrorResponse(requestID, message); err == nil {
		_ = s.writeLine(line)
	}
}
