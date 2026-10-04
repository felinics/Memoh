package native

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/sessionmode"
	"github.com/felinics/memoh/internal/agent/step"
	tools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/hooks"
)

func TestWorkspaceUnavailableAllowsModelResponseWithoutTools(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(strconv.FormatBool(streaming), func(t *testing.T) {
			a := New(Deps{})
			a.SetToolProviders([]tools.ToolProvider{plainTestProvider{}})
			provider := &agentReadMediaMockProvider{handler: func(_ int, p sdk.Request) (sdk.ModelResult, error) {
				if len(p.Tools) > 0 {
					t.Error("unavailable workspace registered tools")
				}
				return sdk.ModelResult{Text: "text response", FinishReason: sdk.FinishReasonStop}, nil
			}}
			cfg := RunConfig{WorkspaceUnavailable: true, SupportsToolCall: true, Model: &sdk.Model{ID: "fixture", Provider: provider}, Messages: []sdk.Message{sdk.UserMessage("hello")}, Identity: SessionContext{WorkspaceTargetID: "computer-b", WorkdirPath: "/pinned"}}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if streaming {
				for evt := range a.Stream(ctx, cfg) {
					if evt.Type == EventError {
						t.Errorf("stream failure: %+v", evt)
					}
				}
			} else {
				r, err := a.Generate(ctx, cfg)
				if err != nil || r.Text != "text response" {
					t.Fatalf("result=%+v err=%v", r, err)
				}
			}
			if provider.calls != 1 {
				t.Fatalf("model calls=%d", provider.calls)
			}
		})
	}
}

func TestModelHookLoadingStatusRefreshesAfterRecovery(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(strconv.FormatBool(streaming), func(t *testing.T) {
			svc := newMockExecContainerService()
			svc.written[hooks.DefaultConfigPath] = []byte(`{"hooks":[`)
			bridgeProvider, cleanup := setupExecTestInfra(t, svc)
			defer cleanup()
			a := New(Deps{BridgeProvider: bridgeProvider, HookService: hooks.NewService(nil, bridgeProvider)})
			provider := &agentReadMediaMockProvider{handler: func(call int, p sdk.Request) (sdk.ModelResult, error) {
				if call == 1 && !strings.Contains(p.System, "could not be safely repaired") {
					t.Errorf("missing invalid config notice: %s", p.System)
				}
				if call == 2 && strings.Contains(p.System, "could not be safely repaired") {
					t.Error("stale hooks failure survived recovery")
				}
				if strings.Count(p.System, "## Hooks configuration loading status") > 1 {
					t.Error("loading status duplicated")
				}
				return sdk.ModelResult{Text: "ok", FinishReason: sdk.FinishReasonStop}, nil
			}}
			cfg := RunConfig{Model: &sdk.Model{ID: "fixture", Provider: provider}, Messages: []sdk.Message{sdk.UserMessage("hello")}, Identity: SessionContext{BotID: "bot-1"}, OnStepCommitted: func(_ context.Context, index int, _ *step.Record) (StepDirective, error) {
				if index == 0 {
					svc.mu.Lock()
					svc.written[hooks.DefaultConfigPath] = []byte(`{"hooks":[]}`)
					svc.mu.Unlock()
					return StepDirective{NextInputs: []DirectiveInput{{Text: "continue"}}}, nil
				}
				return StepDirective{}, nil
			}}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if streaming {
				for evt := range a.Stream(ctx, cfg) {
					if evt.Type == EventError {
						t.Errorf("stream failure: %+v", evt)
					}
				}
			} else {
				if _, err := a.Generate(ctx, cfg); err != nil {
					t.Fatal(err)
				}
			}
			if provider.calls != 2 {
				t.Fatalf("calls=%d", provider.calls)
			}
		})
	}
}

func TestLoadingStatusRetainsUnknownFilesWithoutFabricatingTheirContent(t *testing.T) {
	files := NewFSClient(nil, "bot", nil).LoadSystemFiles(t.Context())
	files[0] = SystemFile{Filename: "AGENTS.md", LoadStatus: "missing"}
	sections := GenerateSystemSections(SystemPromptParams{SessionType: sessionmode.Chat, Files: files, LoadNotices: []string{"Skills unavailable"}})
	var found bool
	for _, section := range sections {
		if strings.Contains(section.ID, sectionIDWorkspaceFile) {
			t.Fatal("unavailable text was inserted as file content")
		}
		if section.ID == "system.auxiliary_load_status" {
			found = true
			if section.RetentionTier != contextfrag.RetentionRequired || !strings.Contains(section.Text, "AGENTS.md does not exist") || !strings.Contains(section.Text, "MEMORY.md could not be read") {
				t.Fatalf("status=%+v", section)
			}
		}
	}
	if !found {
		t.Fatal("missing service status")
	}
}
