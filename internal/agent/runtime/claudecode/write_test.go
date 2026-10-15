package claudecode

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/felinics/memoh/internal/agent/runtime/agentprocess"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
	"github.com/felinics/memoh/internal/workspace/bridgesvc"
)

type observedWriteProcess struct {
	*agentprocess.Process
	started chan struct{}
	once    sync.Once
}

func (p *observedWriteProcess) Write(data []byte) (int, error) {
	p.once.Do(func() { close(p.started) })
	return p.Process.Write(data)
}

func TestControlWriteDeadlineStopsFullStdinPipe(t *testing.T) {
	proc := fullStdinProcess(t, "")
	go func() { _, _ = io.Copy(io.Discard, proc) }()
	runner := newTestRunner(&recordingSink{})
	observed := &observedWriteProcess{Process: proc, started: make(chan struct{})}
	runner.proc = observed
	t.Cleanup(runner.close)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := runner.callControl(ctx, "initialize", map[string]any{"payload": strings.Repeat("x", 8<<20)})
		done <- err
	}()
	select {
	case <-observed.started:
	case <-ctx.Done():
		t.Fatal("deadline expired before the real stdin write started")
	}
	select {
	case err := <-done:
		t.Fatalf("stdin did not remain blocked: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("write did not retain deadline cause: %v", err)
		}
	case <-time.After(9 * time.Second):
		t.Fatal("full stdin pipe hid the control deadline")
	}
	select {
	case <-proc.Done():
	case <-time.After(time.Second):
		t.Fatal("timed-out writer left the process running")
	}
}

func fullStdinProcess(t *testing.T, output string) *agentprocess.Process {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	pb.RegisterContainerServiceServer(server, bridgesvc.New(bridgesvc.Options{DefaultWorkDir: t.TempDir(), AllowHostAbsolute: true}))
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///claude-write-test", grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return listener.DialContext(ctx)
	}), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	proc, err := agentprocess.Start(t.Context(), bridge.NewClientFromConn(conn), "printf ready; printf '%s' "+shellQuote(output)+"; exec /bin/sleep 30", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proc.Close() })
	ready := make([]byte, 5)
	if _, err := io.ReadFull(proc, ready); err != nil || string(ready) != "ready" {
		t.Fatalf("process did not become ready: %q %v", ready, err)
	}
	return proc
}

func TestInitialWriteDeadlinePreservesPartialState(t *testing.T) {
	output := "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"child-native\",\"model\":\"claude-sonnet-5\"}\n" +
		"{\"type\":\"stream_event\",\"session_id\":\"child-native\",\"event\":{\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial evidence\"}}}\n"
	proc := fullStdinProcess(t, output)
	sink := &recordingSink{}
	runner := newTestRunner(sink)
	observed := &observedWriteProcess{Process: proc, started: make(chan struct{})}
	runner.proc = observed
	runner.input.SessionMode = "subagent"
	t.Cleanup(runner.close)
	go runner.readLoop()
	deadline := time.Now().Add(time.Second)
	for len(sink.snapshot()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(sink.snapshot()) == 0 {
		t.Fatal("CLI did not publish its partial output")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	type completion struct {
		result external.PromptResult
		err    error
	}
	done := make(chan completion, 1)
	go func() {
		err := runner.writeLine(ctx, []byte(strings.Repeat("x", 8<<20)))
		result, err := runner.finishEarlyPrompt(ctx, "", err)
		done <- completion{result, err}
	}()
	select {
	case <-observed.started:
	case <-ctx.Done():
		t.Fatal("deadline expired before the real stdin write started")
	}
	select {
	case completed := <-done:
		t.Fatalf("stdin did not remain blocked: %v", completed.err)
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case completed := <-done:
		if !errors.Is(completed.err, context.DeadlineExceeded) || !completed.result.TerminationUnconfirmed || completed.result.Text != "partial evidence" || completed.result.RuntimeMetadata[metadataSessionIDKey] != "child-native" {
			t.Fatalf("early write failure lost partial state: result=%+v err=%v", completed.result, completed.err)
		}
	case <-time.After(9 * time.Second):
		t.Fatal("initial prompt write or cleanup exceeded its deadline")
	}
}
