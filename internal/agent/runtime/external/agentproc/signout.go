package agentproc

import (
	"context"
	"errors"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

// SignOut removes the login an Agent's runtime keeps in loginFile in the
// bot's workspace. Every process of the Agent closes first and none starts
// until the file is gone: a running one could write a refreshed login back.
// A process at work refuses it as external.FailureCredentialBusy.
// beforeDelete, when set, runs with the processes gone (to end a sign-in
// that would put a login back).
func (s *Supervisor) SignOut(ctx context.Context, workspaces bridge.Provider, runtime, botID, botAgentID, loginFile string, beforeDelete func()) error {
	agent := func(k Key) bool { return k.Runtime == runtime && k.BotID == botID && k.BotAgentID == botAgentID }
	unblock, err := s.Drain(ctx, agent, func(p Process) bool {
		b, ok := p.(busyReporter)
		return ok && b.Busy()
	})
	if errors.Is(err, ErrBusy) {
		return external.Fail(external.FailureCredentialBusy, nil)
	}
	if err != nil {
		return err
	}
	defer unblock()
	if beforeDelete != nil {
		beforeDelete()
	}
	client, err := workspaces.MCPClient(ctx, botID)
	if err != nil {
		return err
	}
	err = client.DeleteFile(ctx, loginFile, false)
	if errors.Is(err, bridge.ErrNotFound) {
		return nil
	}
	return errs.WrapDependency(err, "")
}
