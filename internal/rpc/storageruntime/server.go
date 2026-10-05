package storageruntime

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/media"
	intrpc "github.com/felinics/memoh/internal/rpc"
	"github.com/felinics/memoh/internal/rpc/storagepb"
	"github.com/felinics/memoh/internal/storage"
)

const streamChunkBytes = 256 << 10

var errInvalidPutFrame = errors.New("invalid storage put frame")

// Server owns the concrete storage provider used by the Agent process. It
// exposes bounded streaming operations so Channel never needs Server's local
// filesystem or workspace runtime mounted into its process.
type Server struct {
	storagepb.UnimplementedStorageServiceServer
	logger   *slog.Logger
	provider storage.Provider
}

func NewServer(log *slog.Logger, provider storage.Provider) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		logger:   log.With(slog.String("component", "storage_rpc")),
		provider: provider,
	}
}

func (s *Server) Put(stream storagepb.StorageService_PutServer) error {
	if s.provider == nil {
		return s.mapError(stream.Context(), "put", media.ErrProviderUnavailable)
	}
	first, err := stream.Recv()
	if err != nil {
		return refuse(reasonInvalidRequest)
	}
	metadata := first.GetMetadata()
	if metadata == nil || !validKey(metadata.GetKey()) {
		return refuse(reasonInvalidRequest)
	}

	// The fallback provider may retry a failed primary write against secondary
	// storage and therefore requires a seekable reader. Spooling here preserves
	// that contract without buffering a media object in memory.
	tempFile, err := os.CreateTemp("", "memoh-storage-rpc-*")
	if err != nil {
		return s.mapError(stream.Context(), "create put spool", err)
	}
	defer func() {
		_ = tempFile.Close()
		_ = os.Remove(tempFile.Name()) //nolint:gosec // path comes from os.CreateTemp
	}()
	reader := &putStreamReader{stream: stream}
	written, err := io.Copy(tempFile, io.LimitReader(reader, media.MaxAssetBytes+1))
	if err != nil {
		return s.mapError(stream.Context(), "receive put stream", err)
	}
	if written > media.MaxAssetBytes {
		return refuse(reasonTooLarge)
	}
	if _, err := tempFile.Seek(0, io.SeekStart); err != nil {
		return s.mapError(stream.Context(), "rewind put spool", err)
	}
	if err := s.provider.Put(stream.Context(), metadata.GetKey(), tempFile); err != nil {
		return s.mapError(stream.Context(), "put", err)
	}
	return stream.SendAndClose(&storagepb.Empty{})
}

func (s *Server) Open(req *storagepb.KeyRequest, stream storagepb.StorageService_OpenServer) error {
	if s.provider == nil {
		return s.mapError(stream.Context(), "open", media.ErrProviderUnavailable)
	}
	if req == nil || !validKey(req.GetKey()) {
		return refuse(reasonInvalidRequest)
	}
	reader, err := s.provider.Open(stream.Context(), req.GetKey())
	if err != nil {
		return s.mapError(stream.Context(), "open", err)
	}
	if reader == nil {
		return s.mapError(stream.Context(), "open", errors.New("storage provider returned a nil reader"))
	}
	defer func() { _ = reader.Close() }()
	return s.sendReader(stream.Context(), reader, stream.Send)
}

func (s *Server) Delete(ctx context.Context, req *storagepb.KeyRequest) (*storagepb.Empty, error) {
	if s.provider == nil {
		return nil, s.mapError(ctx, "delete", media.ErrProviderUnavailable)
	}
	if req == nil || !validKey(req.GetKey()) {
		return nil, refuse(reasonInvalidRequest)
	}
	if err := s.provider.Delete(ctx, req.GetKey()); err != nil {
		return nil, s.mapError(ctx, "delete", err)
	}
	return &storagepb.Empty{}, nil
}

func (s *Server) AccessPath(ctx context.Context, req *storagepb.KeyRequest) (*storagepb.AccessPathResponse, error) {
	if s.provider == nil {
		return nil, s.mapError(ctx, "access path", media.ErrProviderUnavailable)
	}
	if req == nil || !validKey(req.GetKey()) {
		return nil, refuse(reasonInvalidRequest)
	}
	return &storagepb.AccessPathResponse{Path: s.provider.AccessPath(ctx, req.GetKey())}, nil
}

func (s *Server) EnsureAccessPath(ctx context.Context, req *storagepb.KeyRequest) (*storagepb.AccessPathResponse, error) {
	if s.provider == nil {
		return nil, s.mapError(ctx, "ensure access path", media.ErrProviderUnavailable)
	}
	if req == nil || !validKey(req.GetKey()) {
		return nil, refuse(reasonInvalidRequest)
	}
	ensurer, ok := s.provider.(storage.AccessPathEnsurer)
	if !ok {
		return nil, refuse(reasonAccessPathUnavailable)
	}
	accessPath, err := ensurer.EnsureAccessPath(ctx, req.GetKey())
	if err != nil {
		return nil, s.mapError(ctx, "ensure access path", err)
	}
	return &storagepb.AccessPathResponse{Path: accessPath}, nil
}

func (s *Server) ListPrefix(ctx context.Context, req *storagepb.ListPrefixRequest) (*storagepb.ListPrefixResponse, error) {
	if s.provider == nil {
		return nil, s.mapError(ctx, "list prefix", media.ErrProviderUnavailable)
	}
	if req == nil || !validKey(req.GetPrefix()) {
		return nil, refuse(reasonInvalidRequest)
	}
	lister, ok := s.provider.(storage.PrefixLister)
	if !ok {
		return nil, refuse(reasonUnsupported)
	}
	keys, err := lister.ListPrefix(ctx, req.GetPrefix())
	if err != nil {
		return nil, s.mapError(ctx, "list prefix", err)
	}
	return &storagepb.ListPrefixResponse{Keys: keys}, nil
}

func (s *Server) OpenContainerFile(req *storagepb.ContainerFileRequest, stream storagepb.StorageService_OpenContainerFileServer) error {
	if s.provider == nil {
		return s.mapError(stream.Context(), "open container file", media.ErrProviderUnavailable)
	}
	if req == nil || strings.TrimSpace(req.GetBotId()) == "" || strings.TrimSpace(req.GetPath()) == "" {
		return refuse(reasonInvalidRequest)
	}
	opener, ok := s.provider.(storage.ContainerFileOpener)
	if !ok {
		return refuse(reasonUnsupported)
	}
	reader, err := opener.OpenContainerFile(stream.Context(), req.GetBotId(), req.GetPath())
	if err != nil {
		return s.mapError(stream.Context(), "open container file", err)
	}
	if reader == nil {
		return s.mapError(stream.Context(), "open container file", errors.New("storage provider returned a nil reader"))
	}
	defer func() { _ = reader.Close() }()
	return s.sendReader(stream.Context(), reader, stream.Send)
}

func (s *Server) sendReader(ctx context.Context, reader io.Reader, send func(*storagepb.DataChunk) error) error {
	buf := make([]byte, streamChunkBytes)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			data := append([]byte(nil), buf[:n]...)
			if sendErr := send(&storagepb.DataChunk{Data: data}); sendErr != nil {
				return sendErr
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return s.mapError(ctx, "read", err)
		}
		if ctx.Err() != nil {
			return s.mapError(ctx, "read", ctx.Err())
		}
	}
}

// mapError answers a failed storage operation with the envelope of its
// sentinel, and records the cause for the call's result line. A failure with
// no sentinel is an internal error; its text stays in the record.
func (*Server) mapError(ctx context.Context, operation string, err error) error {
	intrpc.RecordError(ctx, errs.Wrap(err, "storage "+operation))
	if entry, ok := reasons.Lookup(err); ok {
		return entry.Status("")
	}
	if os.IsNotExist(err) {
		return reasonNotFound.Status("")
	}
	return status.Error(codes.Internal, "internal storage operation failed")
}

// refuse answers a request the server rejects before running it.
func refuse(entry intrpc.Reason) error {
	return entry.Status("")
}

type putStreamReader struct {
	stream  storagepb.StorageService_PutServer
	pending []byte
}

func (r *putStreamReader) Read(p []byte) (int, error) {
	for len(r.pending) == 0 {
		frame, err := r.stream.Recv()
		if err != nil {
			return 0, err
		}
		chunk, ok := frame.GetBody().(*storagepb.PutRequest_Chunk)
		if !ok {
			return 0, errInvalidPutFrame
		}
		r.pending = chunk.Chunk
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

func validKey(key string) bool {
	key = strings.TrimSpace(key)
	if key == "" || strings.HasPrefix(key, "/") {
		return false
	}
	clean := path.Clean(key)
	return clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}
