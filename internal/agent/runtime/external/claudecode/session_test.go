package claudecode

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/external"
)

// pipeProcess is a CLI whose output the test writes line by line.
type pipeProcess struct {
	out    *io.PipeReader
	in     *io.PipeWriter
	writes chan []byte
	done   chan struct{}
	exit   sync.Once
}

func newPipeProcess() *pipeProcess {
	r, w := io.Pipe()
	return &pipeProcess{out: r, in: w, writes: make(chan []byte, 16), done: make(chan struct{})}
}

func (p *pipeProcess) Read(b []byte) (int, error) { return p.out.Read(b) }
func (p *pipeProcess) Write(b []byte) (int, error) {
	p.writes <- append([]byte(nil), b...)
	return len(b), nil
}

// The CLI exits once its input ends.
func (p *pipeProcess) CloseStdin() {
	p.exit.Do(func() {
		_ = p.in.Close()
		close(p.done)
	})
}
func (p *pipeProcess) Close() error          { p.CloseStdin(); return nil }
func (p *pipeProcess) Done() <-chan struct{} { return p.done }
func (*pipeProcess) Err() error              { return nil }
func (*pipeProcess) StderrTail() string      { return "" }

func (p *pipeProcess) send(t *testing.T, line string) {
	t.Helper()
	if _, err := io.WriteString(p.in, line+"\n"); err != nil {
		t.Fatal(err)
	}
}

func (p *pipeProcess) written(t *testing.T) map[string]any {
	t.Helper()
	select {
	case raw := <-p.writes:
		var line map[string]any
		if err := json.Unmarshal(raw, &line); err != nil {
			t.Fatal(err)
		}
		return line
	case <-time.After(time.Second):
		t.Fatal("nothing written to the CLI")
		return nil
	}
}

func waitTurn(t *testing.T, turn *turnRunner) {
	t.Helper()
	select {
	case <-turn.done:
	case <-time.After(time.Second):
		t.Fatal("turn did not finish")
	}
}

// One process serves turn after turn: each receives only its own output, and
// a request arriving between turns is refused instead of left waiting.
func TestSessionServesSuccessiveTurns(t *testing.T) {
	p := newPipeProcess()
	s := newCLISession(p, slog.Default())
	go s.readLoop()
	defer func() { _ = s.Close() }()

	first := s.beginTurn(t.Context(), external.PromptInput{Sink: &recordingSink{}}, nil, nil)
	if !s.Busy() {
		t.Fatal("a process running a turn reported idle")
	}
	p.send(t, `{"type":"system","subtype":"init","session_id":"native-1"}`)
	p.send(t, `{"type":"result","subtype":"success","result":"one"}`)
	waitTurn(t, first)
	first.close()
	if s.Busy() || s.heldSession() != "native-1" {
		t.Fatalf("after a turn: busy %v, session %q", s.Busy(), s.heldSession())
	}

	p.send(t, `{"type":"control_request","request_id":"stray","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{}}}`)
	if refused := p.written(t); refused["type"] != messageTypeControlResponse {
		t.Fatalf("stray request answered with %v", refused)
	}

	second := s.beginTurn(t.Context(), external.PromptInput{Sink: &recordingSink{}}, nil, nil)
	p.send(t, `{"type":"result","subtype":"success","result":"two"}`)
	waitTurn(t, second)
	second.close()
	result, err := second.buildResult("native-1")
	if err != nil || result.Text != "two" {
		t.Fatalf("second turn: %q, %v", result.Text, err)
	}
}

// A turn changes the model and effort only when they differ from what the
// process runs with.
func TestApplySettingsSendsOnlyChanges(t *testing.T) {
	p := newPipeProcess()
	s := newCLISession(p, slog.Default())
	s.model, s.effort = "model-a", "high"
	go s.readLoop()
	defer func() { _ = s.Close() }()
	turn := s.beginTurn(t.Context(), external.PromptInput{Sink: &recordingSink{}}, nil, nil)
	defer turn.close()

	if err := turn.applySettings(t.Context(), "model-a", "high"); err != nil {
		t.Fatal(err)
	}
	select {
	case raw := <-p.writes:
		t.Fatalf("unchanged settings sent %s", raw)
	default:
	}

	done := make(chan error, 1)
	go func() { done <- turn.applySettings(context.Background(), "model-b", "") }()
	for _, want := range []string{"set_model", "apply_flag_settings"} {
		request := p.written(t)
		inner := request["request"].(map[string]any)
		if inner["subtype"] != want {
			t.Fatalf("sent %v, want %s", inner, want)
		}
		if want == "apply_flag_settings" && inner["settings"].(map[string]any)["effortLevel"] != nil {
			t.Fatalf("an empty effort must return to the default: %v", inner)
		}
		p.send(t, `{"type":"control_response","response":{"subtype":"success","request_id":"`+request["request_id"].(string)+`","response":{}}}`)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if s.model != "model-b" || s.effort != "" {
		t.Fatalf("applied %q/%q", s.model, s.effort)
	}
}
