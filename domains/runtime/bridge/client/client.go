// Package client provides a gRPC client for the workspace container bridge service.
// Each bot container runs a gRPC server listening on a Unix domain socket.
// This client wraps the generated gRPC stubs with connection pooling and a
// simplified API for callers.
package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"

	"github.com/memohai/memoh/domains/runtime/bridge/bridgepb"
)

const connectingTimeout = 30 * time.Second

// Client wraps a gRPC connection to a single MCP container.
type Client struct {
	conn      *grpc.ClientConn
	svc       bridgepb.ContainerServiceClient
	target    string
	createdAt time.Time
	ownsConn  bool
}

// NewClientFromConn wraps an existing gRPC connection into a Client.
// Intended for testing with in-process transports such as bufconn.
func NewClientFromConn(conn *grpc.ClientConn) *Client {
	return &Client{
		conn:     conn,
		svc:      bridgepb.NewContainerServiceClient(conn),
		target:   conn.Target(),
		ownsConn: true,
	}
}

// Dial creates a new Client connected to the given gRPC target.
// For UDS use "unix:///path/to/sock", for TCP use "host:port".
func Dial(ctx context.Context, target string) (*Client, error) {
	return DialTLS(ctx, target, nil)
}

// DialTLS dials with optional mTLS. A nil TLS config or UDS target uses the
// local trust model; TCP targets use strict mTLS when configured.
func DialTLS(_ context.Context, target string, tlsOpts *TLSOptions) (*Client, error) {
	creds := insecure.NewCredentials()
	opts := []grpc.DialOption{
		grpc.WithNoProxy(),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                30 * time.Second, // ping every 30s if idle
			Timeout:             10 * time.Second, // wait 10s for ping ack
			PermitWithoutStream: true,             // ping even with no active RPC
		}),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(16*1024*1024),
			grpc.MaxCallSendMsgSize(16*1024*1024),
		),
	}
	if tlsOpts.appliesTo(target) {
		tlsCreds, err := tlsOpts.transportCredentials()
		if err != nil {
			return nil, err
		}
		creds = tlsCreds
		// The dial target can be an IP or port-forward address, so authority
		// must be pinned to the synthetic DNS name used in the certificate SAN.
		opts = append(opts, grpc.WithAuthority(tlsOpts.ServerName))
	}
	opts = append(opts, grpc.WithTransportCredentials(creds))
	conn, err := grpc.NewClient(target, opts...)
	if err != nil {
		return nil, fmt.Errorf("grpc dial %s: %w", target, err)
	}
	return &Client{
		conn:      conn,
		svc:       bridgepb.NewContainerServiceClient(conn),
		target:    target,
		createdAt: time.Now(),
		ownsConn:  true,
	}, nil
}

func (c *Client) Close() error {
	if c == nil || !c.ownsConn || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// WithOutgoingMetadata returns a non-owning view of the same gRPC connection.
// Every unary and streaming RPC sent through the view carries the supplied
// metadata. Closing the view is intentionally a no-op: the root client owns
// the device connection and may be shared by multiple Bot workspaces.
func (c *Client) WithOutgoingMetadata(values map[string]string) *Client {
	if c == nil || c.conn == nil {
		return nil
	}
	conn := &outgoingMetadataConn{ClientConnInterface: c.conn, values: cloneStringMap(values)}
	return &Client{
		conn:      c.conn,
		svc:       bridgepb.NewContainerServiceClient(conn),
		target:    c.target,
		createdAt: c.createdAt,
		ownsConn:  false,
	}
}

type outgoingMetadataConn struct {
	grpc.ClientConnInterface
	values map[string]string
}

func (c *outgoingMetadataConn) Invoke(ctx context.Context, method string, args, reply any, opts ...grpc.CallOption) error {
	return c.ClientConnInterface.Invoke(withOutgoingMetadata(ctx, c.values), method, args, reply, opts...)
}

func (c *outgoingMetadataConn) NewStream(ctx context.Context, desc *grpc.StreamDesc, method string, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	return c.ClientConnInterface.NewStream(withOutgoingMetadata(ctx, c.values), desc, method, opts...)
}

func withOutgoingMetadata(ctx context.Context, values map[string]string) context.Context {
	md, _ := metadata.FromOutgoingContext(ctx)
	md = md.Copy()
	for key, value := range values {
		md.Set(key, value)
	}
	return metadata.NewOutgoingContext(ctx, md)
}

func cloneStringMap(values map[string]string) map[string]string {
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func (c *Client) ReadFile(ctx context.Context, path string, lineOffset, nLines int32) (*bridgepb.ReadFileResponse, error) {
	resp, err := c.svc.ReadFile(ctx, &bridgepb.ReadFileRequest{
		Path:       path,
		LineOffset: lineOffset,
		NLines:     nLines,
	})
	return resp, mapError(err)
}

func (c *Client) WriteFile(ctx context.Context, path string, content []byte) error {
	_, err := c.svc.WriteFile(ctx, &bridgepb.WriteFileRequest{
		Path:    path,
		Content: content,
	})
	return mapError(err)
}

// ListDirResult holds the paginated result of a directory listing.
type ListDirResult struct {
	Entries    []*bridgepb.FileEntry
	TotalCount int32
	Truncated  bool
}

func (c *Client) ListDir(ctx context.Context, path string, recursive bool, offset, limit, collapseThreshold int32) (*ListDirResult, error) {
	resp, err := c.svc.ListDir(ctx, &bridgepb.ListDirRequest{
		Path:              path,
		Recursive:         recursive,
		Offset:            offset,
		Limit:             limit,
		CollapseThreshold: collapseThreshold,
	})
	if err != nil {
		return nil, mapError(err)
	}
	return &ListDirResult{
		Entries:    resp.GetEntries(),
		TotalCount: resp.GetTotalCount(),
		Truncated:  resp.GetTruncated(),
	}, nil
}

// ListDirAll lists all entries without pagination (offset=0, limit=0, no collapsing).
func (c *Client) ListDirAll(ctx context.Context, path string, recursive bool) ([]*bridgepb.FileEntry, error) {
	result, err := c.ListDir(ctx, path, recursive, 0, 0, 0)
	if err != nil {
		return nil, err
	}
	return result.Entries, nil
}

func (c *Client) Stat(ctx context.Context, path string) (*bridgepb.FileEntry, error) {
	resp, err := c.svc.Stat(ctx, &bridgepb.StatRequest{Path: path})
	if err != nil {
		return nil, mapError(err)
	}
	return resp.GetEntry(), nil
}

func (c *Client) Mkdir(ctx context.Context, path string) error {
	_, err := c.svc.Mkdir(ctx, &bridgepb.MkdirRequest{Path: path})
	return mapError(err)
}

func (c *Client) Rename(ctx context.Context, oldPath, newPath string) error {
	_, err := c.svc.Rename(ctx, &bridgepb.RenameRequest{OldPath: oldPath, NewPath: newPath})
	return mapError(err)
}

// ExecResult holds the output of a non-streaming exec call.
type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int32
}

// ExecOptions controls how a workspace command is launched.
//
// Env entries are KEY=value overrides. By default the bridge preserves its
// process environment and appends Env, matching the historical ExecWithEnv
// behavior. CleanEnv starts from an empty environment instead. UnsetEnv scrubs
// inherited environment keys before Env is appended, so explicit Env entries
// remain authoritative.
type ExecOptions struct {
	Env      []string
	CleanEnv bool
	UnsetEnv []string
}

// Exec runs a command and collects all output. For streaming, use ExecStream.
func (c *Client) Exec(ctx context.Context, command, workDir string, timeout int32) (*ExecResult, error) {
	return c.ExecWithStdin(ctx, command, workDir, timeout, nil)
}

// ExecWithEnv runs a command with additional environment variables.
func (c *Client) ExecWithEnv(ctx context.Context, command, workDir string, timeout int32, env []string) (*ExecResult, error) {
	return c.ExecWithOptions(ctx, command, workDir, timeout, nil, ExecOptions{Env: env})
}

// ExecWithStdin runs a command with optional stdin data.
func (c *Client) ExecWithStdin(ctx context.Context, command, workDir string, timeout int32, stdinData []byte) (*ExecResult, error) {
	return c.ExecWithOptions(ctx, command, workDir, timeout, stdinData, ExecOptions{})
}

// ExecWithStdinEnv runs a command with optional stdin data and additional
// environment variables.
func (c *Client) ExecWithStdinEnv(ctx context.Context, command, workDir string, timeout int32, stdinData []byte, env []string) (*ExecResult, error) {
	return c.ExecWithOptions(ctx, command, workDir, timeout, stdinData, ExecOptions{Env: env})
}

func (c *Client) ExecWithOptions(ctx context.Context, command, workDir string, timeout int32, stdinData []byte, opts ExecOptions) (*ExecResult, error) {
	stream, err := c.svc.Exec(ctx)
	if err != nil {
		return nil, mapError(err)
	}

	// Send config message first
	err = stream.Send(&bridgepb.ExecInput{
		Command:        command,
		WorkDir:        workDir,
		Env:            opts.Env,
		TimeoutSeconds: timeout,
		CleanEnv:       opts.CleanEnv,
		UnsetEnv:       opts.UnsetEnv,
	})
	if err != nil {
		return nil, err
	}
	if len(stdinData) > 0 {
		if err := stream.Send(&bridgepb.ExecInput{StdinData: stdinData}); err != nil {
			return nil, err
		}
	}
	if err := stream.CloseSend(); err != nil {
		return nil, err
	}

	var stdout, stderr bytes.Buffer
	var exitCode int32

	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch msg.GetStream() {
		case bridgepb.ExecOutput_STDOUT:
			stdout.Write(msg.GetData())
		case bridgepb.ExecOutput_STDERR:
			stderr.Write(msg.GetData())
		case bridgepb.ExecOutput_EXIT:
			exitCode = msg.GetExitCode()
		}
	}

	return &ExecResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
	}, nil
}

// ExecStream returns a bidirectional stream for interactive exec.
// Caller can send stdin data and receive stdout/stderr in real-time.
func (c *Client) ExecStream(ctx context.Context, command, workDir string, timeout int32) (*ExecStream, error) {
	return c.ExecStreamWithEnv(ctx, command, workDir, timeout, nil)
}

// ExecStreamWithEnv returns a bidirectional exec stream with additional env.
func (c *Client) ExecStreamWithEnv(ctx context.Context, command, workDir string, timeout int32, env []string) (*ExecStream, error) {
	return c.ExecStreamWithOptions(ctx, command, workDir, timeout, ExecOptions{Env: env})
}

// ExecStreamWithOptions returns a bidirectional exec stream with environment
// controls.
func (c *Client) ExecStreamWithOptions(ctx context.Context, command, workDir string, timeout int32, opts ExecOptions) (*ExecStream, error) {
	streamCtx, cancel := context.WithCancel(ctx)
	stream, err := c.svc.Exec(streamCtx)
	if err != nil {
		cancel()
		return nil, mapError(err)
	}

	// Send config message first
	err = stream.Send(&bridgepb.ExecInput{
		Command:        command,
		WorkDir:        workDir,
		Env:            opts.Env,
		TimeoutSeconds: timeout,
		CleanEnv:       opts.CleanEnv,
		UnsetEnv:       opts.UnsetEnv,
	})
	if err != nil {
		cancel()
		return nil, err
	}

	return &ExecStream{stream: stream, cancel: cancel}, nil
}

// ExecStream wraps a bidirectional exec stream.
type ExecStream struct {
	stream bridgepb.ContainerService_ExecClient
	cancel context.CancelFunc
	sendMu sync.Mutex
}

// SendStdin sends data to the process stdin.
func (s *ExecStream) SendStdin(data []byte) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return s.stream.Send(&bridgepb.ExecInput{
		StdinData: data,
	})
}

// Recv receives output from the process.
func (s *ExecStream) Recv() (*bridgepb.ExecOutput, error) {
	return s.stream.Recv()
}

// Resize sends a terminal resize event to the running process.
func (s *ExecStream) Resize(cols, rows uint32) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return s.stream.Send(&bridgepb.ExecInput{
		Resize: &bridgepb.TerminalResize{Cols: cols, Rows: rows},
	})
}

// Close closes the stream.
func (s *ExecStream) Close() error {
	if s.cancel != nil {
		s.cancel()
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return s.stream.CloseSend()
}

// ExecStreamPTY opens a bidirectional PTY exec stream.
// The command runs inside a pseudo-terminal with the given initial size.
func (c *Client) ExecStreamPTY(ctx context.Context, command, workDir string, cols, rows uint32) (*ExecStream, error) {
	return c.ExecStreamPTYWithOptions(ctx, command, workDir, cols, rows, ExecOptions{})
}

func (c *Client) ExecStreamPTYWithOptions(ctx context.Context, command, workDir string, cols, rows uint32, opts ExecOptions) (*ExecStream, error) {
	streamCtx, cancel := context.WithCancel(ctx)
	stream, err := c.svc.Exec(streamCtx)
	if err != nil {
		cancel()
		return nil, mapError(err)
	}

	err = stream.Send(&bridgepb.ExecInput{
		Command:  command,
		WorkDir:  workDir,
		Env:      opts.Env,
		Pty:      true,
		Resize:   &bridgepb.TerminalResize{Cols: cols, Rows: rows},
		CleanEnv: opts.CleanEnv,
		UnsetEnv: opts.UnsetEnv,
	})
	if err != nil {
		cancel()
		return nil, err
	}

	return &ExecStream{stream: stream, cancel: cancel}, nil
}

// ReadRaw streams raw file bytes. Caller must consume the returned reader.
func (c *Client) ReadRaw(ctx context.Context, path string) (io.ReadCloser, error) {
	stream, err := c.svc.ReadRaw(ctx, &bridgepb.ReadRawRequest{Path: path})
	if err != nil {
		return nil, mapError(err)
	}
	return newStreamReader(stream)
}

// WriteRaw writes raw bytes to a file in the container.
//
// The stream is terminated with an explicit complete/abort frame so the server
// can finish temp-file cleanup before CloseAndRecv returns. Local reader
// failures send abort, wait for the server handler, then return the original
// reader error. If sending abort itself fails, the RPC is canceled/drained and
// the original reader/Send error is still preserved.
func (c *Client) WriteRaw(ctx context.Context, path string, r io.Reader) (int64, error) {
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	stream, err := c.svc.WriteRaw(streamCtx)
	if err != nil {
		return 0, mapError(err)
	}
	// Always send the path first, even for an empty reader. Besides making empty
	// files representable, this lets the server create its temporary file before
	// any payload chunks arrive.
	if err := stream.Send(&bridgepb.WriteRawChunk{Path: path}); err != nil {
		return drainWriteRaw(cancel, stream, err)
	}

	buf := make([]byte, 64*1024)
	for {
		n, readErr := r.Read(buf)
		if n > 0 {
			if sendErr := stream.Send(&bridgepb.WriteRawChunk{Data: buf[:n]}); sendErr != nil {
				return drainWriteRaw(cancel, stream, sendErr)
			}
		}
		if readErr == io.EOF {
			if err := stream.Send(&bridgepb.WriteRawChunk{Complete: true}); err != nil {
				return drainWriteRaw(cancel, stream, err)
			}
			break
		}
		if readErr != nil {
			if sendErr := stream.Send(&bridgepb.WriteRawChunk{Abort: true}); sendErr != nil {
				return drainWriteRaw(cancel, stream, readErr)
			}
			_, _ = stream.CloseAndRecv()
			return 0, readErr
		}
	}

	resp, err := stream.CloseAndRecv()
	if err != nil {
		return 0, err
	}
	return resp.GetBytesWritten(), nil
}

// drainWriteRaw cancels (when needed) and drains CloseAndRecv so stream
// resources are released, then returns primaryErr unchanged.
func drainWriteRaw(cancel context.CancelFunc, stream bridgepb.ContainerService_WriteRawClient, primaryErr error) (int64, error) {
	if !errors.Is(primaryErr, io.EOF) {
		cancel()
	}
	_, _ = stream.CloseAndRecv()
	return 0, primaryErr
}

func (c *Client) DeleteFile(ctx context.Context, path string, recursive bool) error {
	_, err := c.svc.DeleteFile(ctx, &bridgepb.DeleteFileRequest{
		Path:      path,
		Recursive: recursive,
	})
	return mapError(err)
}

func (c *Client) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" {
		return nil, fmt.Errorf("unsupported tunnel network %q", network)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	streamCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stream, err := c.svc.Tunnel(streamCtx)
	if err != nil {
		cancel()
		return nil, mapError(err)
	}
	if err := sendTunnelFrame(ctx, stream, &bridgepb.TunnelFrame{
		Frame: &bridgepb.TunnelFrame_Open{Open: &bridgepb.TunnelOpen{Address: address}},
	}); err != nil {
		cancel()
		return nil, mapError(err)
	}
	conn := &tunnelConn{
		stream: stream,
		cancel: cancel,
		local:  tunnelAddr("memoh-bridge"),
		remote: tunnelAddr(address),
	}
	if err := conn.waitOpen(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func sendTunnelFrame(ctx context.Context, stream bridgepb.ContainerService_TunnelClient, frame *bridgepb.TunnelFrame) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- stream.Send(frame)
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

type tunnelConn struct {
	stream bridgepb.ContainerService_TunnelClient
	cancel context.CancelFunc
	local  net.Addr
	remote net.Addr

	readMu  sync.Mutex
	writeMu sync.Mutex
	closeMu sync.Once
	buf     []byte
	off     int
}

type tunnelAddr string

func (tunnelAddr) Network() string  { return "bridge-tunnel" }
func (a tunnelAddr) String() string { return string(a) }

func (c *tunnelConn) waitOpen(ctx context.Context) error {
	type recvResult struct {
		frame *bridgepb.TunnelFrame
		err   error
	}
	resultCh := make(chan recvResult, 1)
	go func() {
		frame, err := c.stream.Recv()
		resultCh <- recvResult{frame: frame, err: err}
	}()
	select {
	case result := <-resultCh:
		if result.err != nil {
			return mapError(result.err)
		}
		switch payload := result.frame.GetFrame().(type) {
		case *bridgepb.TunnelFrame_Data:
			if data := payload.Data.GetData(); len(data) > 0 {
				c.buf = data
			}
			return nil
		case *bridgepb.TunnelFrame_Close:
			if msg := payload.Close.GetError(); msg != "" {
				return errors.New(msg)
			}
			return io.EOF
		default:
			return errors.New("unexpected tunnel open response")
		}
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *tunnelConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	c.readMu.Lock()
	defer c.readMu.Unlock()

	for c.off >= len(c.buf) {
		frame, err := c.stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return 0, io.EOF
			}
			return 0, mapError(err)
		}
		switch payload := frame.GetFrame().(type) {
		case *bridgepb.TunnelFrame_Data:
			c.buf = payload.Data.GetData()
			c.off = 0
			if len(c.buf) == 0 {
				continue
			}
		case *bridgepb.TunnelFrame_Close:
			if msg := payload.Close.GetError(); msg != "" {
				return 0, errors.New(msg)
			}
			return 0, io.EOF
		default:
			continue
		}
	}

	n := copy(p, c.buf[c.off:])
	c.off += n
	return n, nil
}

func (c *tunnelConn) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	data := append([]byte(nil), p...)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.stream.Send(&bridgepb.TunnelFrame{
		Frame: &bridgepb.TunnelFrame_Data{Data: &bridgepb.TunnelData{Data: data}},
	}); err != nil {
		return 0, mapError(err)
	}
	return len(p), nil
}

func (c *tunnelConn) Close() error {
	var err error
	c.closeMu.Do(func() {
		c.writeMu.Lock()
		defer c.writeMu.Unlock()
		err = c.stream.Send(&bridgepb.TunnelFrame{Frame: &bridgepb.TunnelFrame_Close{Close: &bridgepb.TunnelClose{}}})
		if closeErr := c.stream.CloseSend(); err == nil {
			err = closeErr
		}
		c.cancel()
	})
	return err
}

func (c *tunnelConn) LocalAddr() net.Addr  { return c.local }
func (c *tunnelConn) RemoteAddr() net.Addr { return c.remote }

func (*tunnelConn) SetDeadline(_ time.Time) error      { return nil }
func (*tunnelConn) SetReadDeadline(_ time.Time) error  { return nil }
func (*tunnelConn) SetWriteDeadline(_ time.Time) error { return nil }

// streamReader adapts a gRPC server stream into an io.ReadCloser.
type streamReader struct {
	stream bridgepb.ContainerService_ReadRawClient
	buf    []byte
	off    int
}

func newStreamReader(stream bridgepb.ContainerService_ReadRawClient) (io.ReadCloser, error) {
	first, err := stream.Recv()
	switch {
	case errors.Is(err, io.EOF):
		return io.NopCloser(bytes.NewReader(nil)), nil
	case err != nil:
		return nil, mapError(err)
	default:
		return &streamReader{stream: stream, buf: first.GetData()}, nil
	}
}

func (r *streamReader) fill() error {
	for r.off >= len(r.buf) {
		msg, err := r.stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return io.EOF
			}
			return mapError(err)
		}
		r.buf = msg.GetData()
		r.off = 0
	}
	return nil
}

func (r *streamReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if err := r.fill(); err != nil {
		return 0, err
	}
	n := copy(p, r.buf[r.off:])
	r.off += n
	return n, nil
}

func (*streamReader) Close() error {
	return nil
}

// Provider resolves a gRPC client for a given bot container.
type Provider interface {
	MCPClient(ctx context.Context, botID string) (*Client, error)
}

// Pool manages cached gRPC clients keyed by bot ID.
type Pool struct {
	mu             sync.RWMutex
	clients        map[string]*Client
	dialTargetFunc func(botID string) string
	tlsOpts        *TLSOptions
}

// NewPool creates a client pool. dialTargetFunc maps bot ID to a gRPC target
// string (e.g. "unix:///path/sock" or "host:port").
func NewPool(dialTargetFunc func(string) string) *Pool {
	return &Pool{
		clients:        make(map[string]*Client),
		dialTargetFunc: dialTargetFunc,
	}
}

// SetTLSOptions enables strict mTLS for subsequent TCP dials (UDS targets are
// exempt). Call once during startup, before the pool serves traffic.
func (p *Pool) SetTLSOptions(opts *TLSOptions) {
	p.mu.Lock()
	p.tlsOpts = opts
	p.mu.Unlock()
}

func (p *Pool) tlsOptions() *TLSOptions {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.tlsOpts
}

// MCPClient implements Provider. Alias for Get.
func (p *Pool) MCPClient(ctx context.Context, botID string) (*Client, error) {
	return p.Get(ctx, botID)
}

// Get returns a cached client or dials a new one.
// Stale connections (Shutdown / TransientFailure / stuck Connecting) are evicted automatically.
func (p *Pool) Get(ctx context.Context, botID string) (*Client, error) {
	p.mu.RLock()
	if c, ok := p.clients[botID]; ok {
		state := c.conn.GetState()
		stale := state == connectivity.Shutdown || state == connectivity.TransientFailure ||
			(state == connectivity.Connecting && time.Since(c.createdAt) > connectingTimeout)
		if !stale {
			p.mu.RUnlock()
			return c, nil
		}
		p.mu.RUnlock()
		p.Remove(botID)
	} else {
		p.mu.RUnlock()
	}

	target := p.dialTargetFunc(botID)
	if target == "" {
		return nil, fmt.Errorf("no dial target for bot %s", botID)
	}

	c, err := DialTLS(ctx, target, p.tlsOptions())
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	if existing, ok := p.clients[botID]; ok {
		p.mu.Unlock()
		_ = c.Close()
		return existing, nil
	}
	p.clients[botID] = c
	p.mu.Unlock()
	return c, nil
}

// Remove closes and removes the client for a bot.
func (p *Pool) Remove(botID string) {
	p.mu.Lock()
	if c, ok := p.clients[botID]; ok {
		_ = c.Close()
		delete(p.clients, botID)
	}
	p.mu.Unlock()
}

// CloseAll closes all cached clients.
func (p *Pool) CloseAll() {
	p.mu.Lock()
	for id, c := range p.clients {
		_ = c.Close()
		delete(p.clients, id)
	}
	p.mu.Unlock()
}
