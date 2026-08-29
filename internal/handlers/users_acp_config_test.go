package handlers

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	acpclient "github.com/felinics/memoh/internal/agent/runtime/acp/client"
	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/workspace"
	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

func TestPrepareACPWorkspaceConfigWritesCodexAPIKeyConfig(t *testing.T) {
	client, recorder := newUsersACPConfigBridgeClient(t)
	workspaceProvider := &usersACPConfigWorkspace{
		backend: bridge.WorkspaceBackendContainer,
		client:  client,
	}
	handler := &UsersHandler{
		acpWorkspace: workspaceProvider,
	}

	err := handler.prepareACPWorkspaceConfig(context.Background(), bots.Bot{
		ID: "bot-1",
		Metadata: map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentCodexID: map[string]any{
						"enabled":    true,
						"setup_mode": "api_key",
						"managed": map[string]any{
							"api_key":  "sk-secret",
							"base_url": "https://proxy.example.com/v1",
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("prepareACPWorkspaceConfig() error = %v", err)
	}
	assertWorkspaceTargetsNative(t, "WorkspaceInfo", workspaceProvider.workspaceInfoTargetsSnapshot())
	assertWorkspaceTargetsNative(t, "MCPClient", workspaceProvider.mcpClientTargetsSnapshot())

	writes := recorder.writes()
	if len(writes) != 2 {
		t.Fatalf("writes len = %d, want config.toml + auth.json: %#v", len(writes), writes)
	}
	configWrite, ok := findUsersACPConfigWrite(writes, acpclient.CodexManagedConfigDir+"/config.toml")
	if !ok {
		t.Fatalf("missing Codex config.toml write: %#v", writes)
	}
	content := string(configWrite.Content)
	for _, want := range []string{
		`model_provider = "OpenAI"`,
		`[model_providers.OpenAI]`,
		`base_url = "https://proxy.example.com/v1"`,
		`wire_api = "responses"`,
		`requires_openai_auth = false`,
		`supports_websockets = false`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("config missing %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "sk-secret") || strings.Contains(content, "api_key") {
		t.Fatalf("config leaked API key:\n%s", content)
	}
	authWrite, ok := findUsersACPConfigWrite(writes, acpclient.CodexManagedConfigDir+"/auth.json")
	if !ok {
		t.Fatalf("missing Codex auth.json write: %#v", writes)
	}
	auth := string(authWrite.Content)
	if !strings.Contains(auth, `"OPENAI_API_KEY": "sk-secret"`) {
		t.Fatalf("auth missing API key:\n%s", auth)
	}
}

func TestPrepareACPWorkspaceConfigSkipsCodexOAuthConfig(t *testing.T) {
	client, recorder := newUsersACPConfigBridgeClient(t)
	handler := &UsersHandler{
		acpWorkspace: &usersACPConfigWorkspace{
			backend: bridge.WorkspaceBackendContainer,
			client:  client,
		},
	}

	err := handler.prepareACPWorkspaceConfig(context.Background(), bots.Bot{
		ID: "bot-1",
		Metadata: map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentCodexID: map[string]any{
						"enabled":    true,
						"setup_mode": "oauth",
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("prepareACPWorkspaceConfig() error = %v", err)
	}
	if writes := recorder.writes(); len(writes) != 0 {
		t.Fatalf("OAuth setup should be written only by ACP Codex OAuth callback, got writes: %#v", writes)
	}
}

func TestPrepareACPWorkspaceConfigSkipsSelf(t *testing.T) {
	handler := &UsersHandler{acpWorkspace: &usersACPConfigWorkspace{backend: bridge.WorkspaceBackendContainer}}
	selfBot := bots.Bot{
		ID: "bot-1",
		Metadata: map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentCodexID: map[string]any{
						"enabled":    true,
						"setup_mode": "self",
					},
				},
			},
		},
	}
	if err := handler.prepareACPWorkspaceConfig(context.Background(), selfBot); err != nil {
		t.Fatalf("self setup should be skipped: %v", err)
	}
}

func TestPrepareACPWorkspaceConfigSurfacesWriteErrors(t *testing.T) {
	handler := &UsersHandler{
		acpWorkspace: &usersACPConfigWorkspace{
			backend: bridge.WorkspaceBackendContainer,
			mcpErr:  errors.New("bridge unavailable"),
		},
	}

	err := handler.prepareACPWorkspaceConfig(context.Background(), bots.Bot{
		ID: "bot-1",
		Metadata: map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentHermesID: map[string]any{
						"enabled":    true,
						"setup_mode": "api_key",
						"managed": map[string]any{
							"provider": "openrouter",
							"model":    "anthropic/claude-sonnet-4",
							"api_key":  "sk-hermes",
						},
					},
				},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "bridge unavailable") {
		t.Fatalf("prepareACPWorkspaceConfig() error = %v, want bridge unavailable", err)
	}
}

func TestValidateACPManagedConfigRejectsInvalidHermesCustom(t *testing.T) {
	err := validateACPManagedConfig(map[string]any{
		acpprofile.MetadataKeyACP: map[string]any{
			"agents": map[string]any{
				acpprofile.AgentHermesID: map[string]any{
					"enabled":    true,
					"setup_mode": "api_key",
					"managed": map[string]any{
						"provider": "custom",
						"model":    "my-model",
						"api_key":  "sk-hermes",
					},
				},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "base_url required") {
		t.Fatalf("validateACPManagedConfig() error = %v, want base_url required", err)
	}
}

func TestValidateACPManagedConfigRejectsUnsupportedHermesSetupMode(t *testing.T) {
	err := validateACPManagedConfig(map[string]any{
		acpprofile.MetadataKeyACP: map[string]any{
			"agents": map[string]any{
				acpprofile.AgentHermesID: map[string]any{
					"enabled":    true,
					"setup_mode": "oauth",
					"managed": map[string]any{
						"provider": "gemini",
						"model":    "gemini-3.5-flash",
						"api_key":  "AIza-test",
					},
				},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), `Hermes does not support setup mode "oauth"`) {
		t.Fatalf("validateACPManagedConfig() error = %v, want unsupported setup mode", err)
	}
}

func TestValidateACPManagedConfigAcceptsLegacyHermesManagedMode(t *testing.T) {
	err := validateACPManagedConfig(map[string]any{
		acpprofile.MetadataKeyACP: map[string]any{
			"agents": map[string]any{
				acpprofile.AgentHermesID: map[string]any{
					"enabled":    true,
					"setup_mode": "managed",
					"managed": map[string]any{
						"provider": "gemini",
						"model":    "gemini-3.5-flash",
						"api_key":  "AIza-test",
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("validateACPManagedConfig() error = %v, want legacy managed accepted as api_key", err)
	}
}

func TestValidateACPManagedConfigAcceptsLegacyCodexManagedOAuthMode(t *testing.T) {
	err := validateACPManagedConfig(map[string]any{
		acpprofile.MetadataKeyACP: map[string]any{
			"agents": map[string]any{
				acpprofile.AgentCodexID: map[string]any{
					"enabled":    true,
					"setup_mode": "managed",
					"managed": map[string]any{
						"auth_type": "provider_oauth",
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("validateACPManagedConfig() error = %v, want legacy managed provider_oauth accepted as oauth", err)
	}
}

func TestACPManagedConfigNeedsWriteOnlyWhenManagedTargetChanges(t *testing.T) {
	existing := map[string]any{
		acpprofile.MetadataKeyACP: map[string]any{
			"agents": map[string]any{
				acpprofile.AgentHermesID: map[string]any{
					"enabled":    true,
					"setup_mode": "api_key",
					"managed": map[string]any{
						"provider": "openrouter",
						"model":    "anthropic/claude-sonnet-4",
						"api_key":  "sk-existing",
					},
				},
			},
		},
		"unrelated": "old",
	}
	unchanged := acpprofile.MergeSensitiveFieldsForUpdate(existing, map[string]any{
		acpprofile.MetadataKeyACP: map[string]any{
			"agents": map[string]any{
				acpprofile.AgentHermesID: map[string]any{
					"enabled":    true,
					"setup_mode": "api_key",
					"managed": map[string]any{
						"provider": "openrouter",
						"model":    "anthropic/claude-sonnet-4",
						"api_key":  "sk-...ting",
					},
				},
			},
		},
		"unrelated": "new",
	})
	if acpManagedConfigNeedsWrite(existing, unchanged) {
		t.Fatal("unchanged managed config should not require workspace write")
	}

	changed := acpprofile.MergeSensitiveFieldsForUpdate(existing, map[string]any{
		acpprofile.MetadataKeyACP: map[string]any{
			"agents": map[string]any{
				acpprofile.AgentHermesID: map[string]any{
					"enabled":    true,
					"setup_mode": "api_key",
					"managed": map[string]any{
						"provider": "openrouter",
						"model":    "openrouter/auto",
						"api_key":  "sk-...ting",
					},
				},
			},
		},
	})
	if !acpManagedConfigNeedsWrite(existing, changed) {
		t.Fatal("changed managed model should require workspace write")
	}
}

func TestACPRuntimeMetadataChangedIgnoresUnrelatedMetadata(t *testing.T) {
	existing := map[string]any{
		acpprofile.MetadataKeyACP: map[string]any{
			"agents": map[string]any{
				acpprofile.AgentHermesID: map[string]any{
					"enabled":    true,
					"setup_mode": "api_key",
					"managed": map[string]any{
						"provider": "openrouter",
						"model":    "anthropic/claude-sonnet-4",
					},
				},
			},
		},
		"unrelated": "old",
	}
	unchanged := map[string]any{
		acpprofile.MetadataKeyACP: existing[acpprofile.MetadataKeyACP],
		"unrelated":               "new",
	}
	if acpRuntimeMetadataChanged(existing, unchanged) {
		t.Fatal("unrelated metadata change should not close ACP runtimes")
	}
	changed := map[string]any{
		acpprofile.MetadataKeyACP: map[string]any{
			"agents": map[string]any{
				acpprofile.AgentHermesID: map[string]any{
					"enabled":    false,
					"setup_mode": "api_key",
					"managed": map[string]any{
						"provider": "openrouter",
						"model":    "anthropic/claude-sonnet-4",
					},
				},
			},
		},
	}
	if !acpRuntimeMetadataChanged(existing, changed) {
		t.Fatal("ACP metadata change should close ACP runtimes")
	}
}

func TestPrepareACPWorkspaceConfigWritesHermesManagedConfig(t *testing.T) {
	client, recorder := newUsersACPConfigBridgeClient(t)
	handler := &UsersHandler{
		acpWorkspace: &usersACPConfigWorkspace{
			backend: bridge.WorkspaceBackendContainer,
			client:  client,
		},
	}

	err := handler.prepareACPWorkspaceConfig(context.Background(), bots.Bot{
		ID: "bot-1",
		Metadata: map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentHermesID: map[string]any{
						"enabled":    true,
						"setup_mode": "api_key",
						"managed": map[string]any{
							"provider": "openrouter",
							"model":    "anthropic/claude-sonnet-4",
							"api_key":  "sk-hermes",
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("prepareACPWorkspaceConfig() error = %v", err)
	}

	writes := recorder.writes()
	configWrite, ok := findUsersACPConfigWrite(writes, acpclient.HermesContainerHome+"/config.yaml")
	if !ok {
		t.Fatalf("missing Hermes config.yaml write: %#v", writes)
	}
	if !strings.Contains(string(configWrite.Content), `provider: "openrouter"`) ||
		!strings.Contains(string(configWrite.Content), `default: "anthropic/claude-sonnet-4"`) {
		t.Fatalf("Hermes config content =\n%s", string(configWrite.Content))
	}
	envWrite, ok := findUsersACPConfigWrite(writes, acpclient.HermesContainerHome+"/.env")
	if !ok {
		t.Fatalf("missing Hermes .env write: %#v", writes)
	}
	if !strings.Contains(string(envWrite.Content), `OPENROUTER_API_KEY='sk-hermes'`) {
		t.Fatalf("Hermes env content =\n%s", string(envWrite.Content))
	}
}

type usersACPConfigWorkspace struct {
	mu             sync.Mutex
	backend        string
	defaultWorkDir string
	client         *bridge.Client
	mcpErr         error

	workspaceInfoTargets []string
	mcpClientTargets     []string
}

func (w *usersACPConfigWorkspace) WorkspaceInfo(ctx context.Context, _ string) (bridge.WorkspaceInfo, error) {
	w.mu.Lock()
	w.workspaceInfoTargets = append(w.workspaceInfoTargets, workspace.WorkspaceTargetFromContext(ctx))
	w.mu.Unlock()
	defaultWorkDir := w.defaultWorkDir
	if defaultWorkDir == "" {
		defaultWorkDir = "/data"
	}
	return bridge.WorkspaceInfo{Backend: w.backend, DefaultWorkDir: defaultWorkDir}, nil
}

func (w *usersACPConfigWorkspace) MCPClient(ctx context.Context, _ string) (*bridge.Client, error) {
	w.mu.Lock()
	w.mcpClientTargets = append(w.mcpClientTargets, workspace.WorkspaceTargetFromContext(ctx))
	w.mu.Unlock()
	if w.mcpErr != nil {
		return nil, w.mcpErr
	}
	return w.client, nil
}

func (w *usersACPConfigWorkspace) workspaceInfoTargetsSnapshot() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.workspaceInfoTargets...)
}

func (w *usersACPConfigWorkspace) mcpClientTargetsSnapshot() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.mcpClientTargets...)
}

func assertWorkspaceTargetsNative(t *testing.T, operation string, targets []string) {
	t.Helper()
	if len(targets) == 0 {
		t.Fatalf("%s was not called", operation)
	}
	for _, target := range targets {
		if target != workspace.WorkspaceTargetNative {
			t.Fatalf("%s workspace target = %q, want %q", operation, target, workspace.WorkspaceTargetNative)
		}
	}
}

func (*usersACPConfigWorkspace) SetupBotContainerWithProgress(context.Context, string, workspace.ContainerSetupProgress) error {
	return nil
}

type usersACPConfigWrite struct {
	Path    string
	Content []byte
}

type usersACPConfigBridgeServer struct {
	pb.UnimplementedContainerServiceServer

	mu           sync.Mutex
	files        []usersACPConfigWrite
	writeErr     error
	writeStarted chan string
	unblockWrite <-chan struct{}
}

func (s *usersACPConfigBridgeServer) WriteFile(ctx context.Context, req *pb.WriteFileRequest) (*pb.WriteFileResponse, error) {
	if s.writeStarted != nil {
		select {
		case s.writeStarted <- req.GetPath():
		default:
		}
	}
	if s.unblockWrite != nil {
		select {
		case <-s.unblockWrite:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return nil, s.writeErr
	}
	s.files = append(s.files, usersACPConfigWrite{
		Path:    req.GetPath(),
		Content: append([]byte(nil), req.GetContent()...),
	})
	return &pb.WriteFileResponse{}, nil
}

func (s *usersACPConfigBridgeServer) writes() []usersACPConfigWrite {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]usersACPConfigWrite, len(s.files))
	copy(out, s.files)
	return out
}

func findUsersACPConfigWrite(writes []usersACPConfigWrite, path string) (usersACPConfigWrite, bool) {
	for _, write := range writes {
		if write.Path == path {
			return write, true
		}
	}
	return usersACPConfigWrite{}, false
}

func newUsersACPConfigBridgeClient(t *testing.T) (*bridge.Client, *usersACPConfigBridgeServer) {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	recorder := &usersACPConfigBridgeServer{}
	pb.RegisterContainerServiceServer(server, recorder)
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	conn, err := grpc.NewClient("passthrough:///users-acp-config-test",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return bridge.NewClientFromConn(conn), recorder
}
