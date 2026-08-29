package workspace

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"

	"github.com/felinics/memoh/internal/config"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/userruntime"
)

func TestWorkspaceTargetContextIsRequestScoped(t *testing.T) {
	t.Parallel()

	base := context.Background()
	first := WithWorkspaceTarget(base, " target-1 ")
	second := WithWorkspaceTarget(base, "target-2")

	if got := WorkspaceTargetFromContext(base); got != "" {
		t.Fatalf("base target = %q, want empty", got)
	}
	if got := WorkspaceTargetFromContext(first); got != "target-1" {
		t.Fatalf("first target = %q, want target-1", got)
	}
	if got := WorkspaceTargetFromContext(second); got != "target-2" {
		t.Fatalf("second target = %q, want target-2", got)
	}
	if got := WorkspaceTargetFromContext(WithWorkspaceTarget(first, "")); got != "" {
		t.Fatalf("cleared target = %q, want empty", got)
	}

	const workers = 64
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			ctx := first
			want := "target-1"
			if index%2 == 1 {
				ctx = second
				want = "target-2"
			}
			for range 100 {
				if got := WorkspaceTargetFromContext(ctx); got != want {
					errs <- fmt.Errorf("worker %d target = %q, want %q", index, got, want)
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

func TestManagerCurrentWorkspaceTargetID(t *testing.T) {
	t.Parallel()

	store := &fakeRemoteBindingStore{records: []dbstore.BotRemoteRuntimeBindingRecord{
		{
			ID:        remoteTestTargetID,
			BotID:     remoteTestBotID,
			RuntimeID: remoteTestRuntimeID,
			IsPrimary: true,
		},
	}}
	manager := NewManager(slog.Default(), nil, nil, config.WorkspaceConfig{}, "", nil)
	manager.SetRemoteWorkspaceService(&RemoteWorkspaceService{store: store})

	tests := []struct {
		name  string
		ctx   context.Context
		botID string
		want  string
	}{
		{
			name:  "request override",
			ctx:   WithWorkspaceTarget(context.Background(), remoteTestTargetID2),
			botID: remoteTestBotID,
			want:  remoteTestTargetID2,
		},
		{
			name:  "persisted remote primary",
			ctx:   context.Background(),
			botID: remoteTestBotID,
			want:  remoteTestTargetID,
		},
		{
			name:  "native fallback",
			ctx:   context.Background(),
			botID: remoteTestBotID2,
			want:  WorkspaceTargetNative,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := manager.CurrentWorkspaceTargetID(test.ctx, test.botID)
			if err != nil {
				t.Fatalf("CurrentWorkspaceTargetID: %v", err)
			}
			if got != test.want {
				t.Fatalf("CurrentWorkspaceTargetID = %q, want %q", got, test.want)
			}
		})
	}
}

func TestManagerWorkspaceTargetOverridePrecedenceAndConcurrentIsolation(t *testing.T) {
	primaryClient, _ := newRemoteScopeTestClient(t)
	requestClient, requestMetadata := newRemoteScopeTestClient(t)
	store := &fakeRemoteBindingStore{records: []dbstore.BotRemoteRuntimeBindingRecord{
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
	manager := NewManager(slog.Default(), nil, nil, config.WorkspaceConfig{}, "", nil)
	manager.SetRemoteWorkspaceService(&RemoteWorkspaceService{
		store: store,
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

	requestCtx := WithWorkspaceTarget(context.Background(), remoteTestTargetID2)
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
	if info.Backend != "remote" || info.OS != "win32" || info.DefaultWorkDir != `C:\Users\alice\workspaces` ||
		info.TargetID != remoteTestTargetID2 || info.TargetKind != WorkspaceTargetRemote || info.TargetName != "Request PC" {
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
			ctx := WithWorkspaceTarget(context.Background(), remoteTestTargetID)
			wantOS := "darwin"
			wantTargetID := remoteTestTargetID
			if index%2 == 1 {
				ctx = WithWorkspaceTarget(context.Background(), remoteTestTargetID2)
				wantOS = "win32"
				wantTargetID = remoteTestTargetID2
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
				if info.TargetID != wantTargetID {
					errs <- fmt.Errorf("worker %d target = %q, want %q", index, info.TargetID, wantTargetID)
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
