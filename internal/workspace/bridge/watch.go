package bridge

import (
	"context"
	"errors"
	"fmt"
	"io"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

// ErrWatchUnsupported marks a bridge (older binary or non-bridge backend)
// that does not implement WatchDir; callers fall back to event-only
// freshness instead of retrying.
var ErrWatchUnsupported = errors.New("workspace watch unsupported")

// WatchDir streams coalesced change batches for one directory
// (non-recursive), invoking onBatch for each. It blocks until the stream
// ends: nil on caller cancellation or clean server end, ErrWatchUnsupported
// when the bridge lacks the RPC, the mapped error otherwise.
func (c *Client) WatchDir(ctx context.Context, dir string, onBatch func(paths []string)) error {
	stream, err := c.svc.WatchDir(ctx, &pb.WatchDirRequest{Path: dir})
	if err != nil {
		return watchError(err)
	}
	for {
		ev, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) || ctx.Err() != nil || errors.Is(err, context.Canceled) {
				return nil
			}
			return watchError(err)
		}
		if onBatch != nil {
			onBatch(ev.GetPaths())
		}
	}
}

func watchError(err error) error {
	if s, ok := status.FromError(err); ok {
		switch s.Code() {
		case codes.Unimplemented:
			return fmt.Errorf("%w: %s", ErrWatchUnsupported, s.Message())
		case codes.Canceled:
			return nil
		}
	}
	return mapError(err)
}
