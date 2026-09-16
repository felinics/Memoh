//go:build !linux

package bridgesvc

import (
	"context"
	"errors"
)

var errPayloadWindowClosed = errors.New("dependency cleanup has no workspace lifetime evidence on this platform")

func (*Server) beginExecution(_ context.Context, maintenance bool) (func(), error) {
	if maintenance {
		return nil, errPayloadWindowClosed
	}
	return func() {}, nil
}
