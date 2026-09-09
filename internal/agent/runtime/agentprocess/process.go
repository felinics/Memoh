// Package agentprocess provides the shared bridge-backed stdio process used by
// direct External Agent drivers. Protocol framing and lifecycle stay with each
// Driver; this package only owns byte transport, stderr capture, and shutdown.
package agentprocess

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

const stderrTailLimit = 8 * 1024

// Process is one long-lived command running through a workspace bridge exec
// stream and exposed as an io.ReadWriter.
type Process struct {
	stream *bridge.ExecStream
	stdin  *io.PipeWriter
	stdout *io.PipeReader
	done   chan struct{}

	mu     sync.Mutex
	closed bool
	tail   []byte
}

// Start launches command without a bridge-side timeout. Closing the returned
// process cancels the exec stream and terminates the command.
func Start(ctx context.Context, client *bridge.Client, command, workDir string, env []string) (*Process, error) {
	stream, err := client.ExecStreamWithOptions(ctx, command, workDir, -1, bridge.ExecOptions{Env: env, CleanEnv: true})
	if err != nil {
		return nil, err
	}

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	proc := &Process{
		stream: stream,
		stdin:  stdinW,
		stdout: stdoutR,
		done:   make(chan struct{}),
	}

	go func() {
		defer func() { _ = stdinR.Close() }()
		buf := make([]byte, 32*1024)
		for {
			n, readErr := stdinR.Read(buf)
			if n > 0 {
				if sendErr := stream.SendStdin(buf[:n]); sendErr != nil {
					_ = stdoutW.CloseWithError(sendErr)
					return
				}
			}
			if readErr != nil {
				return
			}
		}
	}()

	go func() {
		defer func() { _ = stdinW.Close() }()
		defer close(proc.done)
		for {
			output, recvErr := stream.Recv()
			if recvErr != nil {
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
				_ = stdoutW.Close()
				return
			}
		}
	}()

	return proc, nil
}

func (p *Process) appendStderr(data []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tail = append(p.tail, data...)
	if overflow := len(p.tail) - stderrTailLimit; overflow > 0 {
		p.tail = p.tail[overflow:]
	}
}

// StderrTail returns the most recent stderr output for diagnostics.
func (p *Process) StderrTail() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.TrimSpace(string(p.tail))
}

func (p *Process) Read(b []byte) (int, error)  { return p.stdout.Read(b) }
func (p *Process) Write(b []byte) (int, error) { return p.stdin.Write(b) }

// CloseStdin ends input without stopping the process.
func (p *Process) CloseStdin() { _ = p.stdin.Close() }

// Done is closed when the process exits.
func (p *Process) Done() <-chan struct{} { return p.done }

// Close terminates the process and waits briefly for the stream to settle.
func (p *Process) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	p.mu.Unlock()

	_ = p.stdin.Close()
	_ = p.stream.Close()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
	}
	_ = p.stdout.Close()
	return nil
}
