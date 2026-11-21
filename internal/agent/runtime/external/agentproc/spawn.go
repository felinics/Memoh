package agentproc

// This file owns byte transport, stderr capture, and shutdown of one agent
// CLI run over a workspace bridge exec stream; protocol framing stays with each
// driver.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

const stderrTailLimit = 8 * 1024

// Child is one long-lived command running through a workspace bridge exec
// stream and exposed as an io.ReadWriter.
//
// Writes never wait on the command: they are queued for one sender, and a
// command that leaves inputQueue writes unread has stopped taking input and
// is killed. Done closes as soon as the command can serve no more, on exit or
// on Kill, so nothing hands it out while it is still being torn down.
type Child struct {
	stream *bridge.ExecStream
	input  chan []byte
	stdout *io.PipeReader
	// exited closes when the exec stream ends; done when the command can
	// serve no more.
	exited   chan struct{}
	done     chan struct{}
	doneOnce sync.Once

	mu          sync.Mutex
	inputClosed bool
	closed      bool
	tail        []byte
	exitErr     error
}

const (
	// inputQueue is how many writes a command may leave unread before it
	// counts as no longer taking input.
	inputQueue = 256
	// stdinFrame bounds one stdin frame on the bridge stream, far below its
	// message size limit.
	stdinFrame = 32 * 1024
)

// ErrNotReading reports a write to a command that stopped taking input; the
// command is killed.
var ErrNotReading = errors.New("agent process is not reading its input")

// Spawn launches command on env alone, without a bridge-side timeout.
// Closing the returned process cancels the exec stream and terminates the
// command.
func Spawn(ctx context.Context, client *bridge.Client, command, workDir string, env []string) (*Child, error) {
	return SpawnWith(ctx, client, command, workDir, bridge.ExecOptions{Env: env, CleanEnv: true})
}

// SpawnWith is Spawn for a caller that shapes the environment itself: a user
// supplied agent inherits the workspace environment minus what it must not
// see.
func SpawnWith(ctx context.Context, client *bridge.Client, command, workDir string, opts bridge.ExecOptions) (*Child, error) {
	stream, err := client.ExecStreamWithOptions(ctx, command, workDir, -1, opts)
	if err != nil {
		return nil, err
	}

	stdoutR, stdoutW := io.Pipe()
	proc := &Child{
		stream: stream,
		input:  make(chan []byte, inputQueue),
		stdout: stdoutR,
		exited: make(chan struct{}),
		done:   make(chan struct{}),
	}

	go func() {
		for data := range proc.input {
			// A message may outgrow a bridge frame: stdin is a byte stream,
			// and its frames need not follow the caller's writes.
			for len(data) > 0 {
				n := min(len(data), stdinFrame)
				if sendErr := stream.SendStdin(data[:n]); sendErr != nil {
					_ = stdoutW.CloseWithError(sendErr)
					return
				}
				data = data[n:]
			}
		}
		// Input ended by CloseStdin: the command sees EOF.
		_ = stream.CloseSend()
	}()

	go func() {
		defer proc.markDone()
		defer close(proc.exited)
		for {
			output, recvErr := stream.Recv()
			if recvErr != nil {
				proc.mu.Lock()
				proc.exitErr = recvErr
				if errors.Is(recvErr, io.EOF) {
					proc.exitErr = io.ErrUnexpectedEOF
				}
				proc.mu.Unlock()
				if errors.Is(recvErr, io.EOF) {
					_ = stdoutW.Close()
				} else {
					_ = stdoutW.CloseWithError(recvErr)
				}
				return
			}
			switch output.GetStream() {
			case pb.ExecOutput_STDOUT:
				if _, err := stdoutW.Write(output.GetData()); err != nil {
					_ = stdoutW.CloseWithError(err)
					return
				}
			case pb.ExecOutput_STDERR:
				proc.appendStderr(output.GetData())
			case pb.ExecOutput_EXIT:
				if output.GetExitCode() != 0 {
					proc.mu.Lock()
					proc.exitErr = fmt.Errorf("agent process exited with code %d", output.GetExitCode())
					proc.mu.Unlock()
				}
				_ = stdoutW.Close()
				return
			}
		}
	}()

	return proc, nil
}

func (p *Child) appendStderr(data []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tail = append(p.tail, data...)
	if overflow := len(p.tail) - stderrTailLimit; overflow > 0 {
		p.tail = p.tail[overflow:]
	}
}

// StderrTail returns the most recent stderr output for diagnostics.
func (p *Child) StderrTail() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.TrimSpace(string(p.tail))
}

func (p *Child) Read(b []byte) (int, error) { return p.stdout.Read(b) }

// Write queues b for the command and never waits; a command that stopped
// taking input is killed.
func (p *Child) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.inputClosed {
		return 0, io.ErrClosedPipe
	}
	select {
	case p.input <- append([]byte(nil), b...):
		return len(b), nil
	default:
		p.killLocked()
		return 0, ErrNotReading
	}
}

// CloseStdin ends input without stopping the process: the command sees EOF
// once what was queued is sent.
func (p *Child) CloseStdin() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closeInputLocked()
}

func (p *Child) closeInputLocked() {
	if !p.inputClosed {
		p.inputClosed = true
		close(p.input)
	}
}

// Done is closed once the process can serve no more: it exited, or Kill
// began tearing it down.
func (p *Child) Done() <-chan struct{} { return p.done }

func (p *Child) markDone() { p.doneOnce.Do(func() { close(p.done) }) }

// Exited is closed once the exec stream has ended: the command no longer
// runs.
func (p *Child) Exited() <-chan struct{} { return p.exited }

// Err reports a transport failure or nonzero exit after the process exits.
func (p *Child) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exitErr
}

// Kill takes the process out of service at once and terminates it: Done
// closes before Kill returns, and the bridge kills the command when its
// stream is cancelled. Kill never waits on the command.
func (p *Child) Kill() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.killLocked()
}

func (p *Child) killLocked() {
	p.markDone()
	p.closeInputLocked()
	if !p.closed {
		p.closed = true
		// Output nobody reads any more must not hold the receive loop, or
		// the stream's end would never be seen.
		_ = p.stdout.Close()
		go func() { _ = p.stream.Close() }()
	}
}

// Close kills the process and returns once its exec stream has ended: the
// stream is cancelled locally, so its end does not wait on the command, and
// the bridge kills the command's process group when its stream ends.
func (p *Child) Close() error {
	p.Kill()
	<-p.exited
	return nil
}
