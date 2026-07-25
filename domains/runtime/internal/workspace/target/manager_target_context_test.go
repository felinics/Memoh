package target

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"

	userruntime "github.com/memohai/memoh/domains/runtime/client"
	workspaceimpl "github.com/memohai/memoh/domains/runtime/internal/workspace"
	runtimeworkspace "github.com/memohai/memoh/domains/runtime/workspace"
	"github.com/memohai/memoh/internal/config"
)

func TestManagerWorkspaceTargetOverridePrecedenceAndConcurrentIsolation(t *testing.T) {
	primaryClient, _ := newRemoteScopeTestClient(t)
	requestClient, requestMetadata := newRemoteScopeTestClient(t)
	store := &fakeRemoteBindingStore{records: []runtimeworkspace.RemoteMountRecord{
		{
			ID: remoteTestTargetID, BotID: remoteTestBotID, RuntimeID: remoteTestRuntimeID,
			RuntimeName: "Primary Mac", IsPrimary: true,
			RuntimeUserID: remoteTestOwnerID, BotOwnerUserID: remoteTestOwnerID,
		},
		{
			ID: remoteTestTargetID2, BotID: remoteTestBotID, RuntimeID: remoteTestRuntimeID2,
			RuntimeName:   "Request PC",
			RuntimeUserID: remoteTestOwnerID, BotOwnerUserID: remoteTestOwnerID,
		},
	}}
	capabilities := []string{userruntime.CapabilityFS, userruntime.CapabilityExec, userruntime.CapabilityHostFS}
	manager := workspaceimpl.NewManager(slog.Default(), nil, nil, config.WorkspaceConfig{}, "", nil)
	manager.SetRemoteWorkspaceService(&RemoteWorkspaceService{
		store:  store,
		owners: defaultBotOwners(),
		runtimes: fakeRuntimeConnections{
			remoteTestRuntimeID: {
				RuntimeID: remoteTestRuntimeID,
				Client:    primaryClient,
				Info: userruntime.RuntimeInfo{
					WorkspaceBase: "/Users/alice/workspaces",
					OS:            "darwin",
					Capabilities:  capabilities,
				},
			},
			remoteTestRuntimeID2: {
				RuntimeID: remoteTestRuntimeID2,
				Client:    requestClient,
				Info: userruntime.RuntimeInfo{
					WorkspaceBase: `C:\Users\alice\workspaces`,
					OS:            "win32",
					Capabilities:  capabilities,
				},
			},
		},
	})

	requestCtx := runtimeworkspace.WithWorkspaceTarget(context.Background(), remoteTestTargetID2)
	resolved, err := manager.ResolveWorkspaceTarget(requestCtx, remoteTestBotID, "")
	if err != nil {
		t.Fatalf("ResolveWorkspaceTarget request override: %v", err)
	}
	if resolved.TargetID != remoteTestTargetID2 {
		t.Fatalf("request override target = %q, want %q", resolved.TargetID, remoteTestTargetID2)
	}

	resolved, err = manager.ResolveWorkspaceTarget(requestCtx, remoteTestBotID, remoteTestTargetID)
	if err != nil {
		t.Fatalf("ResolveWorkspaceTarget explicit target: %v", err)
	}
	if resolved.TargetID != remoteTestTargetID {
		t.Fatalf("explicit target = %q, want %q", resolved.TargetID, remoteTestTargetID)
	}

	resolved, err = manager.ResolveWorkspaceTarget(context.Background(), remoteTestBotID, "")
	if err != nil {
		t.Fatalf("ResolveWorkspaceTarget Primary: %v", err)
	}
	if resolved.TargetID != remoteTestTargetID {
		t.Fatalf("Primary target = %q, want %q", resolved.TargetID, remoteTestTargetID)
	}

	info, err := manager.WorkspaceInfo(requestCtx, remoteTestBotID)
	if err != nil {
		t.Fatalf("WorkspaceInfo request override: %v", err)
	}
	if info.Backend != "remote" || info.OS != "win32" || info.DefaultWorkDir != `C:\Users\alice\workspaces` {
		t.Fatalf("request WorkspaceInfo = %#v", info)
	}
	client, err := manager.MCPClient(requestCtx, remoteTestBotID)
	if err != nil {
		t.Fatalf("MCPClient request override: %v", err)
	}
	if _, err := client.Stat(context.Background(), "/"); err != nil {
		t.Fatalf("request target Stat: %v", err)
	}
	md := <-requestMetadata
	if got := md.Get("x-memoh-workspace-path-bin"); len(got) != 0 {
		t.Fatalf("obsolete request workspace metadata = %v", got)
	}

	const workers = 32
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			ctx := runtimeworkspace.WithWorkspaceTarget(context.Background(), remoteTestTargetID)
			wantOS := "darwin"
			if index%2 == 1 {
				ctx = runtimeworkspace.WithWorkspaceTarget(context.Background(), remoteTestTargetID2)
				wantOS = "win32"
			}
			<-start
			for range 50 {
				info, infoErr := manager.WorkspaceInfo(ctx, remoteTestBotID)
				if infoErr != nil {
					errs <- fmt.Errorf("worker %d WorkspaceInfo: %w", index, infoErr)
					return
				}
				if info.OS != wantOS {
					errs <- fmt.Errorf("worker %d OS = %q, want %q", index, info.OS, wantOS)
					return
				}
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
