package bridge

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/grpc/metadata"

	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

var ErrPayloadWindowClosed = errors.New("workspace dependency cleanup window is closed")

// ExecQuiescent sends a cleanup script only after a compatible bridge proves
// that its current workspace lifetime has admitted no user exec. Old bridges
// receive only an empty shell, never the cleanup body, and fail closed.
func (c *Client) ExecQuiescent(ctx context.Context, script string) (*ExecResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, "x-memoh-exec-purpose", "payload-gc")
	stream, err := c.svc.Exec(ctx)
	if err != nil {
		return nil, err
	}
	if err := stream.Send(&pb.ExecInput{Command: "exec sh -s", TimeoutSeconds: 15}); err != nil {
		return nil, err
	}
	headers, err := stream.Header()
	if err != nil {
		return nil, errors.Join(ErrPayloadWindowClosed, err)
	}
	if values := headers.Get("x-memoh-payload-gc-window"); len(values) != 1 || values[0] != "accepted" {
		return nil, ErrPayloadWindowClosed
	}
	if err := stream.Send(&pb.ExecInput{StdinData: []byte(script)}); err != nil {
		return nil, err
	}
	if err := stream.CloseSend(); err != nil {
		return nil, err
	}
	var stdout, stderr strings.Builder
	for {
		output, err := stream.Recv()
		if err != nil {
			return nil, fmt.Errorf("quiescent exec ended without EXIT: %w", err)
		}
		switch output.GetStream() {
		case pb.ExecOutput_STDOUT:
			stdout.Write(output.GetData())
		case pb.ExecOutput_STDERR:
			stderr.Write(output.GetData())
		case pb.ExecOutput_EXIT:
			return &ExecResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: output.GetExitCode()}, nil
		}
	}
}
