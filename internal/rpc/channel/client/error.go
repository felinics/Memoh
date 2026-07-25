package client

import (
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/memohai/memoh/internal/rpc/channel/internal/codec"
)

var ErrDeployment = errors.New("channel RPC deployment mismatch")

type deploymentError struct {
	status *status.Status
}

func (e *deploymentError) Error() string              { return e.status.Err().Error() }
func (*deploymentError) Unwrap() error                { return ErrDeployment }
func (e *deploymentError) GRPCStatus() *status.Status { return e.status }

func decode(err error) error {
	if err == nil {
		return nil
	}
	st := status.Convert(err)
	if st.Code() == codes.Unimplemented {
		return &deploymentError{status: st}
	}
	return codec.DecodeError(err)
}
