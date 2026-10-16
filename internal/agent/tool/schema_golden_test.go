package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/felinics/memoh/internal/agent/background"
	"github.com/felinics/memoh/internal/agent/toolexec"
	pgstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/messaging"
	"github.com/felinics/memoh/internal/searchproviders"
	"github.com/felinics/memoh/internal/settings"
	"github.com/felinics/memoh/internal/workdir"
)

// schemaGoldenTools builds every tool set whose schemas are pinned under
// testdata/schemas/<name>.json. The goldens were captured from the
// hand-written map schemas before the providers moved to toolexec.Define, so
// a typed handler that changed what the model reads fails here. Providers
// gated on bot settings expose their definition step separately so the gate
// does not need a database.
var schemaGoldenTools = map[string]func(t *testing.T) []toolexec.Tool{
	"background": providerTools(NewBackgroundProvider(nil, background.New(nil))),
	"webfetch":   providerTools(NewWebFetchProvider(nil, nil, nil)),
	"web":        providerTools(&WebProvider{settings: &settings.Service{}, searchProviders: &searchproviders.Service{}}),
	"contacts":   providerTools(NewContactsProvider(nil, goldenContactReader{})),
	"skill":      providerTools(NewSkillProvider(nil)),
	"acp_agents": providerTools(&ACPAgentsProvider{pool: goldenACPPool{}, queries: (*pgstore.Queries)(nil)}),
	"workdir":    providerTools(NewWorkdirProvider(nil, goldenWorkdirLister{})),
	"history": providerTools(
		NewHistoryProvider(nil, fakeHistorySessionLister{}, &fakeHistoryMessageReader{}, (*pgstore.Queries)(nil)),
	),
	"schedule": providerTools(NewScheduleProvider(nil, nativeSourceTestScheduler{})),
	"subagent": func(t *testing.T) []toolexec.Tool {
		p := NewSpawnProvider(nil, nil, nil, nil, nil, background.New(nil))
		p.SetAgent(&fakeSpawnAgent{})
		return providerTools(p)(t)
	},
	"ask_user": providerTools(NewAskUserProvider(nil)),
	"message": providerTools(&MessageProvider{exec: &messaging.Executor{
		Sender: goldenMessaging{}, Reactor: goldenMessaging{}, Resolver: goldenMessaging{},
	}}),
	"container":  providerTools(NewContainerProvider(nil, nil, nil, "/data")),
	"memory":     func(*testing.T) []toolexec.Tool { return (&MemoryProvider{}).writeTools(goldenSession, nil) },
	"tts":        func(*testing.T) []toolexec.Tool { return (&TTSProvider{}).speakTools(goldenSession) },
	"transcribe": func(*testing.T) []toolexec.Tool { return (&TranscriptionProvider{}).transcribeTools(goldenSession) },
	"image_gen":  func(*testing.T) []toolexec.Tool { return (&ImageGenProvider{}).imageTools(goldenSession, "") },
	"video_gen":  func(*testing.T) []toolexec.Tool { return (&VideoGenProvider{}).videoTools(goldenSession) },
	"browser":    func(*testing.T) []toolexec.Tool { return (&BrowserProvider{}).browserTools(goldenSession) },
}

func providerTools(provider ToolProvider) func(t *testing.T) []toolexec.Tool {
	return func(t *testing.T) []toolexec.Tool {
		t.Helper()
		tools, err := provider.Tools(context.Background(), goldenSession)
		if err != nil {
			t.Fatalf("tools: %v", err)
		}
		return tools
	}
}

type goldenMessaging struct{}

func (goldenMessaging) Send(context.Context, string, messaging.Platform, messaging.SendRequest) error {
	return nil
}

func (goldenMessaging) React(context.Context, string, messaging.Platform, messaging.ReactRequest) error {
	return nil
}

func (goldenMessaging) ParseChannelType(raw string) (messaging.Platform, error) {
	return messaging.Platform(raw), nil
}

// goldenSession enables every session-gated tool; providers whose schema
// text depends on the session read these same values when the goldens were
// captured.
var goldenSession = SessionContext{
	BotID: "bot", SessionID: "session", ChannelIdentityID: "identity",
	CurrentPlatform: "telegram", ReplyTarget: "chat-1", ConversationType: "private",
	CanRequestUserInput: true, CanListUserInput: true, SupportsImageInput: true,
	Skills: map[string]SkillDetail{"pdf": {Description: "pdf skill", Path: "/data/.agents/skills/pdf"}},
}

type goldenContactReader struct{}

func (goldenContactReader) ListContacts(context.Context, string) ([]messaging.Contact, error) {
	return nil, nil
}

type goldenWorkdirLister struct{}

func (goldenWorkdirLister) List(context.Context, string, bool) ([]workdir.Workdir, error) {
	return nil, nil
}

type goldenACPPool struct{}

func (goldenACPPool) CreateAgentRuntime(context.Context, string, string, string) (ACPRuntimeSummary, error) {
	return ACPRuntimeSummary{}, nil
}
func (goldenACPPool) CloseAgentRuntime(string, string) error { return nil }

func TestToolSchemasMatchGolden(t *testing.T) {
	t.Parallel()
	for name, build := range schemaGoldenTools {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			raw, err := os.ReadFile(filepath.Join("testdata", "schemas", name+".json")) //nolint:gosec // fixture path built from the registry key
			if err != nil {
				t.Fatalf("read golden: %v", err)
			}
			var golden map[string]any
			if err := json.Unmarshal(raw, &golden); err != nil {
				t.Fatalf("parse golden: %v", err)
			}
			tools := build(t)
			seen := map[string]bool{}
			for _, tool := range tools {
				want, ok := golden[tool.Name]
				if !ok {
					t.Errorf("tool %q has no golden schema; add it to testdata/schemas/%s.json", tool.Name, name)
					continue
				}
				seen[tool.Name] = true
				got := canonicalJSON(t, toolexec.SchemaValue(tool.Parameters))
				if expected := canonicalJSON(t, want); got != expected {
					t.Errorf("schema of %q changed\n got  %s\n want %s", tool.Name, got, expected)
				}
			}
			for toolName := range golden {
				if !seen[toolName] {
					t.Errorf("golden names tool %q that the provider no longer registers", toolName)
				}
			}
		})
	}
}

// canonicalJSON renders a schema with sorted keys and a sorted required list.
// An empty required list is dropped: the hand-written schemas spelled
// `"required": []`, the inferred ones omit it, and the order of the names
// follows struct field order rather than the hand-written order; neither
// difference means anything to a provider.
func canonicalJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var generic any
	if err := json.Unmarshal(data, &generic); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	dropEmptyRequired(generic)
	data, err = json.Marshal(generic)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(data)
}

func dropEmptyRequired(value any) {
	switch v := value.(type) {
	case map[string]any:
		if req, ok := v["required"].([]any); ok {
			if len(req) == 0 {
				delete(v, "required")
			} else {
				sort.Slice(req, func(i, j int) bool { return fmt.Sprint(req[i]) < fmt.Sprint(req[j]) })
			}
		}
		for _, child := range v {
			dropEmptyRequired(child)
		}
	case []any:
		for _, child := range v {
			dropEmptyRequired(child)
		}
	}
}
