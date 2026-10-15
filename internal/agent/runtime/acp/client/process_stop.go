package client

import (
	"context"
	"errors"

	"github.com/felinics/memoh/internal/agent/runtime/agentprocess"
)

func (p *bridgeProcess) Stop(ctx context.Context) error {
	confirmed := func() error {
		if p.exitConfirmed.Load() {
			return nil
		}
		return agentprocess.ErrTerminationUnconfirmed
	}
	select {
	case <-p.done:
		return confirmed()
	default:
	}
	sent := make(chan error, 1)
	go func() { sent <- p.stream.Terminate() }()
	select {
	case <-p.done:
		return confirmed()
	case <-sent:
	case <-ctx.Done():
		_ = p.Close()
		return errors.Join(agentprocess.ErrTerminationUnconfirmed, ctx.Err())
	}
	select {
	case <-p.done:
		return confirmed()
	case <-ctx.Done():
		_ = p.Close()
		return errors.Join(agentprocess.ErrTerminationUnconfirmed, ctx.Err())
	}
}
