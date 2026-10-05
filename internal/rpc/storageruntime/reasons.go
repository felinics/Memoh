package storageruntime

import (
	"context"
	"errors"
	"os"

	"google.golang.org/grpc/codes"

	"github.com/felinics/memoh/internal/media"
	intrpc "github.com/felinics/memoh/internal/rpc"
	"github.com/felinics/memoh/internal/storage"
)

// errInvalidRequest is the identity of a storage request the server refused
// before running it: a missing or malformed key, path or stream frame.
var errInvalidRequest = errors.New("invalid storage request")

var (
	reasonInvalidRequest        = intrpc.Reason{Err: errInvalidRequest, Reason: "storage.invalid_request", Code: codes.InvalidArgument, Message: "invalid storage request"}
	reasonInvalidPutFrame       = intrpc.Reason{Err: errInvalidPutFrame, Reason: "storage.invalid_put_frame", Code: codes.InvalidArgument, Message: "invalid storage put stream"}
	reasonTooLarge              = intrpc.Reason{Err: media.ErrAssetTooLarge, Reason: "storage.too_large", Code: codes.ResourceExhausted, Message: "storage object is too large"}
	reasonProviderUnavailable   = intrpc.Reason{Err: media.ErrProviderUnavailable, Reason: "storage.provider_unavailable", Code: codes.Unavailable, Message: "storage provider is unavailable"}
	reasonUnsupported           = intrpc.Reason{Err: storage.ErrContainerFileNotSupported, Reason: "storage.unsupported", Code: codes.Unimplemented, Message: "storage operation is unsupported"}
	reasonAccessPathUnavailable = intrpc.Reason{Err: storage.ErrAccessPathUnavailable, Reason: "storage.access_path_unavailable", Code: codes.FailedPrecondition, Message: "storage access path is unavailable"}
	reasonCanceled              = intrpc.Reason{Err: context.Canceled, Reason: "storage.canceled", Code: codes.Canceled, Message: "storage operation canceled"}
	reasonDeadlineExceeded      = intrpc.Reason{Err: context.DeadlineExceeded, Reason: "storage.deadline_exceeded", Code: codes.DeadlineExceeded, Message: "storage operation timed out"}
	reasonNotFound              = intrpc.Reason{Err: os.ErrNotExist, Reason: "storage.not_found", Code: codes.NotFound, Message: "storage object not found"}
)

// reasons is the wire table of the storage RPCs, read by the server encoding
// and the client decoding. Entries are matched in order.
var reasons = intrpc.Reasons{
	reasonInvalidRequest,
	reasonInvalidPutFrame,
	reasonTooLarge,
	reasonProviderUnavailable,
	reasonUnsupported,
	reasonAccessPathUnavailable,
	reasonCanceled,
	reasonDeadlineExceeded,
	reasonNotFound,
}
