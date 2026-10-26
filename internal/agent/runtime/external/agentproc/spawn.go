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
	"time"

	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

const stderrTailLimit = 8 * 1024

// Child is one long-lived command running through a workspace bridge exec
// stream and exposed as an io.ReadWriter.
type Child struct {
	stream *bridge.ExecStream
	stdin  *io.PipeWriter
	stdout *io.PipeReader
	done   chan struct{}

	mu      sync.Mutex
	closed  bool
	tail    []byte
	exitErr error
}

// Spawn launches command without a bridge-side timeout. Closing the returned
// process cancels the exec stream and terminates the command.
func Spawn(ctx context.Context, client *bridge.Client, command, workDir string, env []string) (*Child, error) {
	stream, err := client.ExecStreamWithOptions(ctx, command, workDir, -1, bridge.ExecOptions{Env: env, CleanEnv: true})
	if err != nil {
		return nil, err
	}

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	proc := &Child{
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
				if errors.Is(readErr, io.EOF) {
					_ = stream.CloseSend()
				}
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

func (p *Child) Read(b []byte) (int, error)  { return p.stdout.Read(b) }
func (p *Child) Write(b []byte) (int, error) { return p.stdin.Write(b) }

// CloseStdin ends input without stopping the process.
func (p *Child) CloseStdin() { _ = p.stdin.Close() }

// Done is closed when the process exits.
func (p *Child) Done() <-chan struct{} { return p.done }

// Err reports a transport failure or nonzero exit after Done closes.
func (p *Child) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exitErr
}

// Close terminates the process and waits briefly for the stream to settle.
func (p *Child) Close() error {
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
