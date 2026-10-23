package client

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/external"
)

func TestTurnSinkPanicFailsOnlyItsTurn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	failed := make(chan error, 1)
	sink := NewTurnSinkWithFailure(ctx, nil, EventSinkFunc(func(event.StreamEvent) { panic("consumer") }), ToolOutputLimit{}, func(err error) { failed <- err })
	sink.EmitStreamEvent(event.StreamEvent{Type: event.TextDelta, Delta: "hello"})
	select {
	case err := <-failed:
		if !external.IsPanic(err) {
			t.Fatalf("failure = %v", err)
		}
	case <-ctx.Done():
		t.Fatal("sink did not fail")
	}
	if err := sink.Err(); !external.IsPanic(err) {
		t.Fatalf("Err() = %v", err)
	}
	var text string
	other := NewTurnSink(EventSinkFunc(func(ev event.StreamEvent) { text += ev.Delta }), ToolOutputLimit{})
	other.EmitStreamEvent(event.StreamEvent{Type: event.TextDelta, Delta: "other"})
	other.Finish()
	if text != "other" {
		t.Fatalf("unaffected sink = %q", text)
	}
}

type panicWriteTransport struct {
	io.ReadWriter
	method string
}

func (w panicWriteTransport) Write(p []byte) (int, error) {
	if strings.Contains(string(p), w.method) {
		panic("write callback")
	}
	return w.ReadWriter.Write(p)
}

func TestPromptWritePanicReturnsInternal(t *testing.T) {
	_, transport := startMultiSessionAgent(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := Connect(ctx, panicWriteTransport{transport, acp.AgentMethodSessionPrompt}, nil)
	if err != nil {
		t.Fatal(err)
	}
	session, err := conn.NewSession(ctx, SessionConfig{ProjectPath: "/data"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = session.Prompt(ctx, "hello")
	if !external.IsPanic(err) {
		t.Fatalf("Prompt = %v", err)
	}
}

func TestCancelWritePanicReturnsInternal(t *testing.T) {
	_, transport := startMultiSessionAgent(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := Connect(ctx, panicWriteTransport{transport, acp.AgentMethodSessionCancel}, nil)
	if err != nil {
		t.Fatal(err)
	}
	promptCtx, stop := context.WithCancel(ctx)
	defer stop()
	// The original request is held in the transport until cancellation has begun.
	reader, writer := io.Pipe()
	defer func() { _ = reader.Close() }()
	defer func() { _ = writer.Close() }()
	conn.rpc.conn = acp.NewConnection(conn.rpc.handle, signalPromptWriter{stop: stop}, reader)
	_, err = conn.rpc.Prompt(promptCtx, acp.PromptRequest{SessionId: "session"})
	if !external.IsPanic(err) {
		t.Fatalf("Prompt = %v", err)
	}
}

type signalPromptWriter struct{ stop context.CancelFunc }

func (w signalPromptWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), acp.AgentMethodSessionCancel) {
		panic("cancel callback")
	}
	w.stop()
	return len(p), nil
}

type closableTransport struct {
	io.ReadWriter
	reader io.Closer
}

func (t closableTransport) Close() error { return t.reader.Close() }

func TestDispatchPanicClosesOnlyAffectedConnection(t *testing.T) {
	agent, transport := startMultiSessionAgent(t)
	// The fake's Reader is an io.PipeReader, like the owned production transport.
	parts := transport.(struct {
		io.Reader
		io.Writer
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := Connect(ctx, closableTransport{transport, parts.Reader.(io.Closer)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	conn.Subscribe("panic", func(json.RawMessage) { panic("notification callback") })
	if err := agent.conn.SendNotification(ctx, "panic", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-conn.Done():
	case <-ctx.Done():
		t.Fatal("connection remained open")
	}
	if err := conn.Call(ctx, "pending", nil, nil); err == nil {
		t.Fatal("call on failed connection succeeded")
	}
	_, otherTransport := startMultiSessionAgent(t)
	other, err := Connect(ctx, otherTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.NewSession(ctx, SessionConfig{ProjectPath: "/data"}); err != nil {
		t.Fatalf("unaffected connection: %v", err)
	}
}

type panicReadTransport struct{ io.ReadWriter }

func (*panicReadTransport) Read(_ []byte) (int, error) { panic("reader callback") }
func TestReaderPanicClosesConnection(t *testing.T) {
	reader, writer := io.Pipe()
	defer func() { _ = reader.Close() }()
	defer func() { _ = writer.Close() }()
	transport := &panicReadTransport{ReadWriter: struct {
		io.Reader
		io.Writer
	}{reader, writer}}
	conn := newSharedClientConnection(nil, io.Discard, transport)
	select {
	case <-conn.conn.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("connection remained open")
	}
}

func TestTerminalCallbackPanicClosesConnection(t *testing.T) {
	failed := make(chan error, 1)
	term := &terminal{ctx: context.Background(), done: make(chan struct{}), onDone: func(*terminal) { panic("terminal callback") }, fail: func(err error) { failed <- err }}
	go term.readLoop()
	select {
	case err := <-failed:
		if !external.IsPanic(err) {
			t.Fatalf("failure = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("terminal did not fail")
	}
	select {
	case <-term.done:
	default:
		t.Fatal("terminal waiters not released")
	}
}

func TestProcessFinalizerPanicReturnsInternal(t *testing.T) {
	proc := &bridgeProcess{lease: &runtimeLease{root: "/tmp/memoh-acp-runtime/acp/00000000-0000-0000-0000-000000000001", agentID: "acp"}, finalizeDone: make(chan struct{})}
	go proc.finalizeAfterExit(context.Background())
	select {
	case <-proc.finalizeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("finalizer did not finish")
	}
	if !external.IsPanic(proc.finalizeErr) {
		t.Fatalf("finalizeErr = %v", proc.finalizeErr)
	}
}

// The process reader serves one agent process for every session on it. A
// panic while it relays output closes stdout with the internal failure, so the
// connection reading it fails through its disconnect path.
func TestProcessReaderPanicClosesStdoutWithAnInternalFailure(t *testing.T) {
	stdoutR, stdoutW := io.Pipe()
	// A process without a stream panics at its first read.
	proc := &bridgeProcess{done: make(chan struct{}), tail: &stderrTail{}, logger: slog.New(slog.DiscardHandler)}
	go proc.readOutput(context.Background(), stdoutW)

	read := make(chan error, 1)
	go func() {
		_, err := stdoutR.Read(make([]byte, 1))
		read <- err
	}()
	select {
	case err := <-read:
		if !external.IsPanic(err) {
			t.Fatalf("stdout read error = %v, want a recovered panic", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stdout was left open after the reader died")
	}
	select {
	case <-proc.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the process was never marked done")
	}
}
