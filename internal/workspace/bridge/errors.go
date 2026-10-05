package bridge

import (
	"context"
	"errors"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	"github.com/felinics/memoh/internal/rpc"
)

var (
	ErrNotFound    = errors.New("not found")
	ErrUnavailable = errors.New("unavailable")
	ErrBadRequest  = errors.New("invalid argument")
	ErrForbidden   = errors.New("permission denied")
)

// restoredByCode is how the client restores a bridge status: the bridge
// reports its failures by status code alone.
var restoredByCode = map[codes.Code]error{
	codes.NotFound:         ErrNotFound,
	codes.InvalidArgument:  ErrBadRequest,
	codes.PermissionDenied: ErrForbidden,
	codes.Unavailable:      ErrUnavailable,
	codes.Aborted:          ErrUnavailable,
}

// restoredWhileLive adds Canceled to restoredByCode for a call whose context
// is still live: its caller did not end it, so the connection or the bridge
// closed it, and the workspace is unavailable.
var restoredWhileLive = func() map[codes.Code]error {
	m := map[codes.Code]error{codes.Canceled: ErrUnavailable}
	for code, sentinel := range restoredByCode {
		m[code] = sentinel
	}
	return m
}()

// mapError is what the client returns for err, received from a call made
// under ctx, by rpc.Decode: a failure the bridge reported, attributed to it.
func mapError(ctx context.Context, err error) error {
	return decodeError(ctx.Err() == nil, err)
}

// mapStreamError is mapError for err, received on stream: the stream's own
// context is the context of the call.
func mapStreamError(stream grpc.ClientStream, err error) error {
	return decodeError(stream.Context().Err() == nil, err)
}

// decodeError decodes err, received from a call whose context was live or
// had ended when it failed.
func decodeError(live bool, err error) error {
	if live {
		return rpc.Decode(err, nil, restoredWhileLive)
	}
	return rpc.Decode(err, nil, restoredByCode)
}
