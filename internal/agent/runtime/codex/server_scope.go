package codex

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/external"
)

func promptServerKey(input external.PromptInput) string {
	key := serverKey(input.BotID, input.BotAgentID)
	if input.IsSubagent() {
		key += "\x00" + strings.TrimSpace(input.ThreadID)
	}
	return key
}

// serverProcessEnv marks a subagent-scoped app-server with its child thread.
func serverProcessEnv(key string) []string {
	parts := strings.SplitN(key, "\x00", 3)
	if len(parts) < 3 {
		return nil
	}
	return external.SubagentThreadEnvEntry(parts[2])
}

func (d *Driver) acquireTurnServer(ctx context.Context, input external.PromptInput) (*appServer, func(), error) {
	if input.IsSubagent() && strings.TrimSpace(input.ThreadID) == "" {
		return nil, nil, external.ErrThreadUnavailable
	}
	return d.acquireServerKey(ctx, promptServerKey(input))
}

// acquireControlServer acquires the server a runtime control runs on. A
// child's server otherwise lives only for its prompts, so a control retires it
// once nothing else holds it.
func (d *Driver) acquireControlServer(ctx context.Context, input external.PromptInput) (*appServer, func(), error) {
	srv, release, err := d.acquireTurnServer(ctx, input)
	if err != nil {
		return nil, nil, err
	}
	return srv, d.controlRelease(input, srv, release), nil
}

func (d *Driver) controlRelease(input external.PromptInput, server recyclable, release func()) func() {
	if !input.IsSubagent() {
		return release
	}
	return func() {
		release()
		d.servers.retireIdle(promptServerKey(input), server)
	}
}

func (d *Driver) recycleTurnServer(input external.PromptInput, server recyclable) {
	d.servers.recycleResource(promptServerKey(input), server)
}

type stoppableServer interface {
	recyclable
	Stop(context.Context) error
}

func (d *Driver) finishSubagentPrompt(ctx context.Context, input external.PromptInput, server stoppableServer, result *external.PromptResult) error {
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	stopErr := server.Stop(stopCtx)
	d.recycleTurnServer(input, server)
	if stopErr != nil {
		result.TerminationUnconfirmed = true
		return errors.Join(external.ErrTerminationUnconfirmed, stopErr)
	}
	return nil
}
