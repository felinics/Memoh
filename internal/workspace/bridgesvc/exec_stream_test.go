package bridgesvc

import (
	"context"

	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"

	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

type cancelOnStdoutExecStream struct {
	ctx    context.Context
	cancel context.CancelFunc

	outputs  []*pb.ExecOutput
	canceled bool
}

func newCancelOnStdoutExecStream() *cancelOnStdoutExecStream {
	ctx, cancel := context.WithCancel(context.Background())
	return &cancelOnStdoutExecStream{ctx: ctx, cancel: cancel}
}

func (s *cancelOnStdoutExecStream) Send(msg *pb.ExecOutput) error {
	clone := proto.Clone(msg).(*pb.ExecOutput)
	if len(msg.GetData()) > 0 {
		clone.Data = append([]byte(nil), msg.GetData()...)
	}
	s.outputs = append(s.outputs, clone)
	if !s.canceled && msg.GetStream() == pb.ExecOutput_STDOUT && len(msg.GetData()) > 0 {
		s.canceled = true
		s.cancel()
	}
	return nil
}

func (s *cancelOnStdoutExecStream) Recv() (*pb.ExecInput, error) {
	<-s.ctx.Done()
	return nil, s.ctx.Err()
}

func (s *cancelOnStdoutExecStream) Context() context.Context   { return s.ctx }
func (*cancelOnStdoutExecStream) SetHeader(metadata.MD) error  { return nil }
func (*cancelOnStdoutExecStream) SendHeader(metadata.MD) error { return nil }
func (*cancelOnStdoutExecStream) SetTrailer(metadata.MD)       {}
func (*cancelOnStdoutExecStream) SendMsg(any) error            { return nil }
func (*cancelOnStdoutExecStream) RecvMsg(any) error            { return nil }
