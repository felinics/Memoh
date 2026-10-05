package storageruntime

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/media"
	intrpc "github.com/felinics/memoh/internal/rpc"
	"github.com/felinics/memoh/internal/rpc/storagepb"
	"github.com/felinics/memoh/internal/storage"
)

// Provider is the Channel-side proxy for the Server-owned storage provider.
// Every byte-bearing operation streams in bounded frames.
type Provider struct {
	client storagepb.StorageServiceClient
}

func NewProvider(conn grpc.ClientConnInterface) *Provider {
	return &Provider{client: storagepb.NewStorageServiceClient(conn)}
}

func (p *Provider) Put(ctx context.Context, key string, reader io.Reader) error {
	if reader == nil {
		return errors.New("storage reader is required")
	}
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := p.client.Put(streamCtx)
	if err != nil {
		return mapClientError(err)
	}
	if err := stream.Send(&storagepb.PutRequest{Body: &storagepb.PutRequest_Metadata{Metadata: &storagepb.PutMetadata{Key: key}}}); err != nil {
		return mapClientError(err)
	}
	buf := make([]byte, streamChunkBytes)
	for {
		n, readErr := reader.Read(buf)
		if n > 0 {
			data := append([]byte(nil), buf[:n]...)
			if err := stream.Send(&storagepb.PutRequest{Body: &storagepb.PutRequest_Chunk{Chunk: data}}); err != nil {
				return mapClientError(err)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	_, err = stream.CloseAndRecv()
	return mapClientError(err)
}

func (p *Provider) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	streamCtx, cancel := context.WithCancel(ctx)
	stream, err := p.client.Open(streamCtx, &storagepb.KeyRequest{Key: key})
	if err != nil {
		cancel()
		return nil, mapClientError(err)
	}
	return primeStreamReader(cancel, stream.Recv)
}

func (p *Provider) Delete(ctx context.Context, key string) error {
	_, err := p.client.Delete(ctx, &storagepb.KeyRequest{Key: key})
	return mapClientError(err)
}

func (p *Provider) AccessPath(ctx context.Context, key string) string {
	resp, err := p.client.AccessPath(ctx, &storagepb.KeyRequest{Key: key})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(resp.GetPath())
}

func (p *Provider) EnsureAccessPath(ctx context.Context, key string) (string, error) {
	resp, err := p.client.EnsureAccessPath(ctx, &storagepb.KeyRequest{Key: key})
	if err != nil {
		return "", mapClientError(err)
	}
	path := strings.TrimSpace(resp.GetPath())
	if path == "" {
		return "", storage.ErrAccessPathUnavailable
	}
	return path, nil
}

func (p *Provider) ListPrefix(ctx context.Context, prefix string) ([]string, error) {
	resp, err := p.client.ListPrefix(ctx, &storagepb.ListPrefixRequest{Prefix: prefix})
	if err != nil {
		return nil, mapClientError(err)
	}
	return resp.GetKeys(), nil
}

func (p *Provider) OpenContainerFile(ctx context.Context, botID, containerPath string) (io.ReadCloser, error) {
	streamCtx, cancel := context.WithCancel(ctx)
	stream, err := p.client.OpenContainerFile(streamCtx, &storagepb.ContainerFileRequest{BotId: botID, Path: containerPath})
	if err != nil {
		cancel()
		return nil, mapClientError(err)
	}
	return primeStreamReader(cancel, stream.Recv)
}

type chunkReceiver func() (*storagepb.DataChunk, error)

func primeStreamReader(cancel context.CancelFunc, recv chunkReceiver) (io.ReadCloser, error) {
	first, err := recv()
	if errors.Is(err, io.EOF) {
		return &streamReader{cancel: cancel, recv: recv, done: true}, nil
	}
	if err != nil {
		cancel()
		return nil, mapClientError(err)
	}
	return &streamReader{cancel: cancel, recv: recv, pending: first.GetData()}, nil
}

type streamReader struct {
	cancel  context.CancelFunc
	recv    chunkReceiver
	pending []byte
	done    bool
}

func (r *streamReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(r.pending) == 0 && !r.done {
		frame, err := r.recv()
		if errors.Is(err, io.EOF) {
			r.done = true
			break
		}
		if err != nil {
			r.done = true
			return 0, mapClientError(err)
		}
		r.pending = frame.GetData()
	}
	if len(r.pending) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

func (r *streamReader) Close() error {
	r.done = true
	r.pending = nil
	if r.cancel != nil {
		r.cancel()
	}
	return nil
}

// mapClientError restores the storage sentinel of a failed call. A status
// without the envelope, from a server that predates it or produced by the
// gRPC client itself, is restored by its code.
func mapClientError(err error) error {
	if err == nil {
		return nil
	}
	if restored := reasons.Decode(err); restored != nil {
		return restored
	}
	var sentinel error
	switch status.Code(err) {
	case codes.NotFound:
		sentinel = os.ErrNotExist
	case codes.ResourceExhausted:
		sentinel = media.ErrAssetTooLarge
	case codes.Unimplemented:
		sentinel = storage.ErrContainerFileNotSupported
	case codes.FailedPrecondition:
		sentinel = storage.ErrAccessPathUnavailable
	case codes.Canceled:
		sentinel = context.Canceled
	case codes.DeadlineExceeded:
		sentinel = context.DeadlineExceeded
	default:
		// Received from the server, which recorded the cause itself.
		return errs.Remote(err)
	}
	return intrpc.Restored(sentinel, err)
}

var (
	_ storage.Provider            = (*Provider)(nil)
	_ storage.AccessPathEnsurer   = (*Provider)(nil)
	_ storage.PrefixLister        = (*Provider)(nil)
	_ storage.ContainerFileOpener = (*Provider)(nil)
)
