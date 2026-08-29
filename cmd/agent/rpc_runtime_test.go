package main

import (
	"context"
	"testing"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/adapters/local"
)

type recordingSendRuntime struct{ sends, reacts []channel.ChannelType }

func (r *recordingSendRuntime) Send(_ context.Context, _ string, typ channel.ChannelType, _ channel.SendRequest) error {
	r.sends = append(r.sends, typ)
	return nil
}

func (r *recordingSendRuntime) React(_ context.Context, _ string, typ channel.ChannelType, _ channel.ReactRequest) error {
	r.reacts = append(r.reacts, typ)
	return nil
}

type recordingRemoteRuntime struct {
	channel.Runtime
	sends []channel.ChannelType
}

func (r *recordingRemoteRuntime) Send(_ context.Context, _ string, typ channel.ChannelType, _ channel.SendRequest) error {
	r.sends = append(r.sends, typ)
	return nil
}

func (*recordingRemoteRuntime) React(context.Context, string, channel.ChannelType, channel.ReactRequest) error {
	return nil
}

// TestLocalFirstChannelRuntimeRoutesWebLocally pins the hub-split fix: web
// and cli sends must reach this process's manager (whose RouteHub feeds the
// Web SSE stream), while external platforms go over the internal RPC.
func TestLocalFirstChannelRuntimeRoutesWebLocally(t *testing.T) {
	localRt := &recordingSendRuntime{}
	remoteRt := &recordingRemoteRuntime{}
	rt := &localFirstChannelRuntime{local: localRt, remote: remoteRt}

	ctx := context.Background()
	for _, typ := range []channel.ChannelType{local.WebType, local.CLIType, channel.ChannelType("telegram")} {
		if err := rt.Send(ctx, "bot-1", typ, channel.SendRequest{}); err != nil {
			t.Fatalf("send %s: %v", typ, err)
		}
	}
	if len(localRt.sends) != 2 || localRt.sends[0] != local.WebType || localRt.sends[1] != local.CLIType {
		t.Fatalf("local sends = %v", localRt.sends)
	}
	if len(remoteRt.sends) != 1 || remoteRt.sends[0] != channel.ChannelType("telegram") {
		t.Fatalf("remote sends = %v", remoteRt.sends)
	}
	if err := rt.React(ctx, "bot-1", local.WebType, channel.ReactRequest{}); err != nil {
		t.Fatalf("react: %v", err)
	}
	if len(localRt.reacts) != 1 {
		t.Fatalf("local reacts = %v", localRt.reacts)
	}
}
