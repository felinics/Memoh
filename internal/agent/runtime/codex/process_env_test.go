package codex

import (
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
	"github.com/felinics/memoh/internal/workspace/bridgesvc"
)

func TestOnlySubagentAppServerProcessesCarryThreadMarker(t *testing.T) {
	root := t.TempDir()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	pb.RegisterContainerServiceServer(server, bridgesvc.New(bridgesvc.Options{DefaultWorkDir: root, WorkspaceRoot: root, DataMount: "/data", AllowHostAbsolute: true}))
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///codex-env-test", grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return listener.DialContext(ctx)
	}), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := bridge.NewClientFromConn(conn)
	launcher := filepath.Join(root, "codex")
	out := filepath.Join(root, "marker")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\nprintf %s \"$"+external.SubagentThreadEnv+"\" > "+out+".tmp && mv "+out+".tmp "+out+"\n"), 0o700); err != nil { //nolint:gosec // test launcher must be executable.
		t.Fatal(err)
	}
	for _, tc := range []struct {
		input external.PromptInput
		want  string
	}{
		{external.PromptInput{BotID: "bot", BotAgentID: "agent", ThreadID: "child", SessionMode: "subagent"}, "child"},
		{external.PromptInput{BotID: "bot", BotAgentID: "agent", ThreadID: "parent", SessionMode: "chat"}, ""},
	} {
		_ = os.Remove(out)
		ctx, cancel := context.WithCancel(t.Context())
		go func() {
			_, _ = startAppServerSession(ctx, promptServerKey(tc.input), client, Config{}, external.Launcher{Path: launcher}, slog.New(slog.DiscardHandler))
		}()
		deadline := time.Now().Add(5 * time.Second)
		for {
			data, err := os.ReadFile(out) //nolint:gosec // test output path.
			if err == nil {
				if string(data) != tc.want {
					t.Fatalf("%s app-server marker=%q want %q", tc.input.SessionMode, data, tc.want)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("app-server never ran: %v", err)
			}
			time.Sleep(20 * time.Millisecond)
		}
		cancel()
	}
}
