package claudecode

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/agentprocess"
	"github.com/felinics/memoh/internal/agent/runtime/external"
)

type unconfirmedProcess struct{ testCLIProcess }

func (*unconfirmedProcess) Stop(context.Context) error { return agentprocess.ErrTerminationUnconfirmed }

func TestDrainRequiresExitAcknowledgement(t *testing.T) {
	for _, disconnected := range []bool{false, true} {
		proc := &unconfirmedProcess{testCLIProcess{done: make(chan struct{})}}
		if disconnected {
			close(proc.done)
		}
		if err := drainCLI(t.Context(), proc, time.Millisecond); !errors.Is(err, external.ErrTerminationUnconfirmed) {
			t.Fatalf("unconfirmed process stop lost its identity (disconnected=%v): %v", disconnected, err)
		}
	}
}

type blockedInterruptProcess struct {
	testCLIProcess
	entered chan struct{}
	exited  chan struct{}
}

func (p *blockedInterruptProcess) Write([]byte) (int, error) {
	close(p.entered)
	<-p.done
	close(p.exited)
	return 0, io.ErrClosedPipe
}

func TestInterruptWriteDoesNotBlockTerminationGrace(t *testing.T) {
	proc := &blockedInterruptProcess{testCLIProcess: testCLIProcess{done: make(chan struct{})}, entered: make(chan struct{}), exited: make(chan struct{})}
	r := newTestRunner(&recordingSink{})
	r.proc = proc
	defer r.close()
	defer func() { close(proc.done); <-proc.exited }()
	returned := make(chan struct{})
	go func() { r.interrupt(); close(returned) }()
	select {
	case <-proc.entered:
	case <-time.After(time.Second):
		t.Fatal("interrupt write never started")
	}
	select {
	case <-returned:
	case <-time.After(50 * time.Millisecond):
		t.Fatal("blocked interrupt prevents the caller from reaching termination grace")
	}
}

func TestUnconfirmedTerminationPreservesPartialTranscriptAndAnchor(t *testing.T) {
	r := newTestRunner(&recordingSink{})
	defer r.close()
	feed(t, r, `{"type":"system","subtype":"init","session_id":"child-native","model":"claude-sonnet-5"}`)
	feed(t, r, `{"type":"stream_event","session_id":"child-native","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}}`)
	r.observeExit(external.ErrTerminationUnconfirmed)
	result, err := r.buildResult("")
	if !result.TerminationUnconfirmed || !errors.Is(err, external.ErrTerminationUnconfirmed) || len(result.Output) == 0 || result.RuntimeMetadata[metadataSessionIDKey] != "child-native" {
		t.Fatalf("unknown termination lost partial state: result=%+v err=%v", result, err)
	}
}

func TestCompletedChildRequiresExitAcknowledgement(t *testing.T) {
	r := newTestRunner(&recordingSink{})
	defer r.close()
	r.input.SessionMode = "subagent"
	feed(t, r, `{"type":"system","subtype":"init","session_id":"child-native","model":"claude-sonnet-5"}`)
	feed(t, r, `{"type":"result","subtype":"success","session_id":"child-native","is_error":false,"result":"finished report"}`)
	r.observeExit(external.ErrTerminationUnconfirmed)
	result, err := r.buildResult("")
	if !result.TerminationUnconfirmed || !errors.Is(err, external.ErrTerminationUnconfirmed) || !result.TurnCompleted || result.Text != "finished report" || len(result.Output) == 0 || result.RuntimeMetadata[metadataSessionIDKey] != "child-native" {
		t.Fatalf("completion hid uncertain process termination or lost its report: result=%+v err=%v", result, err)
	}
}
