package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
)

const RuntimeGRPCMessageLimit = 16 << 20

var (
	ErrPipeNotConfigured     = errors.New("runtime pipe is not configured")
	ErrInvalidPipeConnection = errors.New("invalid runtime pipe connection")
	ErrPipeConnectionUsed    = errors.New("runtime pipe connection already consumed")
	ErrPipeHandshake         = errors.New("runtime pipe handshake failed")
)

type Pipe interface {
	ClientConn(ctx context.Context, conn net.Conn) (*grpc.ClientConn, error)
}

// DirectPipe runs a single gRPC h2c client connection directly over the byte
// stream supplied by the Runtime WebSocket. ClientConn takes ownership of conn
// on every path, including validation and handshake failures.
//
// A Runtime reconnect is a new WebSocket and therefore a new DirectPipe call.
// The dialer below deliberately cannot return the byte stream more than once,
// so grpc-go cannot transparently attach this ClientConn to a new transport
// after the WebSocket is lost.
type DirectPipe struct{}

func NewDirectPipe() *DirectPipe {
	return &DirectPipe{}
}

func (*DirectPipe) ClientConn(ctx context.Context, conn net.Conn) (*grpc.ClientConn, error) {
	if conn == nil {
		return nil, ErrInvalidPipeConnection
	}
	if ctx == nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: context is required", ErrInvalidPipeConnection)
	}
	if err := ctx.Err(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: %w", ErrPipeHandshake, err)
	}

	dialer := newSingleUseConnDialer(conn)
	clientConn, err := grpc.NewClient(
		"passthrough:///remote-runtime",
		grpc.WithContextDialer(dialer.DialContext),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithNoProxy(),
		grpc.WithDisableRetry(),
		// Entering grpc-go's idle state tears down the only transport and would
		// require a second dial. A direct Runtime connection remains active until
		// its WebSocket or owning ClientConn is closed.
		grpc.WithIdleTimeout(0),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(RuntimeGRPCMessageLimit),
			grpc.MaxCallSendMsgSize(RuntimeGRPCMessageLimit),
		),
	)
	if err != nil {
		dialer.Close()
		return nil, fmt.Errorf("%w: create client: %w", ErrPipeHandshake, err)
	}

	clientConn.Connect()
	if err := waitForDirectPipeReady(ctx, clientConn, dialer); err != nil {
		_ = clientConn.Close()
		dialer.Close()
		return nil, err
	}
	return clientConn, nil
}

func waitForDirectPipeReady(ctx context.Context, conn *grpc.ClientConn, dialer *singleUseConnDialer) error {
	for {
		state := conn.GetState()
		switch state {
		case connectivity.Ready:
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("%w: %w", ErrPipeHandshake, err)
			}
			return nil
		case connectivity.Shutdown:
			return fmt.Errorf("%w: gRPC transport shut down", ErrPipeHandshake)
		case connectivity.TransientFailure:
			if dialErr := dialer.LastError(); dialErr != nil {
				return fmt.Errorf("%w: %w", ErrPipeHandshake, dialErr)
			}
			// The sole byte stream was already handed to grpc-go. Any failure of
			// that attempt is terminal for this Runtime connection.
			if dialer.Used() {
				return fmt.Errorf("%w: gRPC transport rejected the byte stream", ErrPipeHandshake)
			}
		}
		if !conn.WaitForStateChange(ctx, state) {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("%w: %w", ErrPipeHandshake, err)
			}
			return fmt.Errorf("%w: gRPC state did not change", ErrPipeHandshake)
		}
	}
}

type singleUseConnDialer struct {
	mu      sync.Mutex
	conn    net.Conn
	used    bool
	lastErr error
}

func newSingleUseConnDialer(conn net.Conn) *singleUseConnDialer {
	return &singleUseConnDialer{conn: conn}
}

func (d *singleUseConnDialer) DialContext(ctx context.Context, _ string) (net.Conn, error) {
	d.mu.Lock()
	if d.used || d.conn == nil {
		d.lastErr = ErrPipeConnectionUsed
		d.mu.Unlock()
		return nil, ErrPipeConnectionUsed
	}
	d.used = true
	conn := d.conn
	d.conn = nil
	d.mu.Unlock()

	select {
	case <-ctx.Done():
		_ = conn.Close()
		err := context.Cause(ctx)
		d.mu.Lock()
		d.lastErr = err
		d.mu.Unlock()
		return nil, err
	default:
		return conn, nil
	}
}

func (d *singleUseConnDialer) Used() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.used
}

func (d *singleUseConnDialer) LastError() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastErr
}

// Close closes the stream only when grpc-go has not already taken ownership.
func (d *singleUseConnDialer) Close() {
	d.mu.Lock()
	conn := d.conn
	d.conn = nil
	d.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}
