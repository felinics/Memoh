package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

const hookTestBufSize = 1 << 20

type fakeToolRunner struct {
	calls []fakeToolCall
	fn    func(context.Context, string, map[string]any) (any, error)
}

type fakeToolCall struct {
	name  string
	input map[string]any
}

func (r *fakeToolRunner) RunHookTool(ctx context.Context, toolName string, input map[string]any) (any, error) {
	r.calls = append(r.calls, fakeToolCall{name: toolName, input: input})
	if r.fn != nil {
		return r.fn(ctx, toolName, input)
	}
	return map[string]any{"decision": DecisionAllow}, nil
}

func TestRunConfigMatchesEveryCatalogEvent(t *testing.T) {
	t.Parallel()

	events := EventCatalog()
	cfg := Config{
		Version: 1,
		Hooks:   make([]Hook, 0, len(events)),
	}
	for _, event := range events {
		cfg.Hooks = append(cfg.Hooks, Hook{
			Name:  "record-" + event,
			Event: event,
			Actions: []HookAction{{
				Type: ActionTool,
				Tool: "record_event",
				Input: map[string]any{
					"event": event,
				},
			}},
		})
	}

	service := NewService(nil, nil)
	runner := &fakeToolRunner{}
	for _, event := range events {
		result, err := service.RunConfig(context.Background(), cfg, Request{Event: event, BotID: "bot-1"}, runner)
		if err != nil {
			t.Fatalf("RunConfig(%s) returned error: %v", event, err)
		}
		if result.HooksMatched != 1 {
			t.Fatalf("RunConfig(%s) matched %d hooks, want 1", event, result.HooksMatched)
		}
		if result.ActionsRun != 1 {
			t.Fatalf("RunConfig(%s) ran %d actions, want 1", event, result.ActionsRun)
		}
		if result.Decision != DecisionAllow {
			t.Fatalf("RunConfig(%s) decision = %q, want %q", event, result.Decision, DecisionAllow)
		}
		if result.RuntimeSupported != RuntimeSupported(event) {
			t.Fatalf("RunConfig(%s) runtime_supported = %v, want %v", event, result.RuntimeSupported, RuntimeSupported(event))
		}
	}
	if len(runner.calls) != len(events) {
		t.Fatalf("runner calls = %d, want %d", len(runner.calls), len(events))
	}
}

func TestRunConfigMergesDecisionsInPriorityOrder(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Version: 1,
		Hooks: []Hook{
			{
				Name:     "append-context",
				Event:    EventPreToolUse,
				Matcher:  "^exec$",
				Priority: 30,
				Actions: []HookAction{{
					Type: ActionTool,
					Tool: "append",
				}},
			},
			{
				Name:     "ask",
				Event:    EventPreToolUse,
				Matcher:  "^exec$",
				Priority: 20,
				Actions: []HookAction{{
					Type: ActionTool,
					Tool: "ask",
				}},
			},
			{
				Name:     "deny",
				Event:    EventPreToolUse,
				Matcher:  "^exec$",
				Priority: 10,
				Actions: []HookAction{{
					Type: ActionTool,
					Tool: "deny",
				}},
			},
		},
	}
	runner := &fakeToolRunner{
		fn: func(_ context.Context, name string, _ map[string]any) (any, error) {
			switch name {
			case "append":
				return map[string]any{
					"decision":       DecisionAppendContext,
					"append_context": "extra guardrail",
				}, nil
			case "ask":
				return map[string]any{
					"decision": DecisionAskApproval,
					"reason":   "needs human approval",
				}, nil
			case "deny":
				return map[string]any{
					"decision": DecisionDeny,
					"reason":   "blocked by policy",
				}, nil
			default:
				return nil, nil
			}
		},
	}

	result, err := NewService(nil, nil).RunConfig(context.Background(), cfg, Request{
		Event: EventPreToolUse,
		Tool:  &ToolPayload{Name: "exec"},
	}, runner)
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("RunConfig error = %v, want ErrDenied", err)
	}
	if result.Decision != DecisionDeny {
		t.Fatalf("decision = %q, want %q", result.Decision, DecisionDeny)
	}
	if result.Reason != "blocked by policy" {
		t.Fatalf("reason = %q, want deny reason", result.Reason)
	}
	if result.AppendContext != "extra guardrail" {
		t.Fatalf("append_context = %q, want merged context", result.AppendContext)
	}
	if got, want := len(runner.calls), 3; got != want {
		t.Fatalf("runner calls = %d, want %d", got, want)
	}
	if got := []string{runner.calls[0].name, runner.calls[1].name, runner.calls[2].name}; !slices.Equal(got, []string{"append", "ask", "deny"}) {
		t.Fatalf("runner call order = %v, want priority order", got)
	}
}

func TestRunConfigLimitsToolAppendContext(t *testing.T) {
	t.Parallel()

	large := "HEAD\n" + strings.Repeat("context detail ", 300) + "\nTAIL"
	cfg := Config{
		Version: 1,
		Defaults: Defaults{
			MaxOutputBytes: 192,
		},
		Hooks: []Hook{{
			Name:  "append-context",
			Event: EventBeforeModelCall,
			Actions: []HookAction{{
				Type: ActionTool,
				Tool: "append",
			}},
		}},
	}
	runner := &fakeToolRunner{
		fn: func(context.Context, string, map[string]any) (any, error) {
			return map[string]any{
				"decision":       DecisionAppendContext,
				"append_context": large,
			}, nil
		},
	}

	result, err := NewService(nil, nil).RunConfig(context.Background(), cfg, Request{Event: EventBeforeModelCall}, runner)
	if err != nil {
		t.Fatalf("RunConfig returned error: %v", err)
	}
	if len(result.AppendContext) > 192 {
		t.Fatalf("append_context bytes = %d, want <= 192", len(result.AppendContext))
	}
	if len(result.AppendContext) >= len(large) {
		t.Fatalf("append_context was not limited")
	}
	assertHookTextPreservesHeadTail(t, result.AppendContext)
}

func TestApplyActionOutputParsesAppendSystemSectionObject(t *testing.T) {
	t.Parallel()

	var result ActionResult
	applyActionOutput(&result, `{
		"append_system_section": {
			"id": "guardrail",
			"text": "system-only guidance"
		}
	}`, 4096)

	if len(result.AppendSystemSections) != 1 {
		t.Fatalf("append_system_sections = %#v, want one section", result.AppendSystemSections)
	}
	section := result.AppendSystemSections[0]
	if section.ID != "guardrail" || section.Text != "system-only guidance" {
		t.Fatalf("section = %#v, want parsed object", section)
	}
	if section.Retention != SystemSectionRetentionOptional || section.Cache != SystemSectionCacheDynamic {
		t.Fatalf("section defaults = retention %q cache %q, want optional/dynamic", section.Retention, section.Cache)
	}
}

func TestRunConfigAggregatesAppendSystemSectionsWithAppendContext(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Version: 1,
		Hooks: []Hook{{
			Name:  "policy-hook",
			Event: EventBeforePromptBuild,
			Actions: []HookAction{
				{Type: ActionTool, Tool: "mixed"},
				{Type: ActionTool, Tool: "sections"},
			},
		}},
	}
	runner := &fakeToolRunner{
		fn: func(_ context.Context, name string, _ map[string]any) (any, error) {
			switch name {
			case "mixed":
				return map[string]any{
					"decision":       DecisionAppendContext,
					"append_context": "turn-only note",
					"append_system_section": map[string]any{
						"id":        "stable",
						"text":      "stable system guidance",
						"retention": "preferred",
						"cache":     "stable",
					},
				}, nil
			case "sections":
				return map[string]any{
					"append_system_section": []any{
						map[string]any{"id": "defaulted", "text": "default system guidance"},
						map[string]any{"id": "clamped", "text": "cannot be required", "retention": "required"},
					},
				}, nil
			default:
				return nil, nil
			}
		},
	}

	result, err := NewService(nil, nil).RunConfig(
		context.Background(),
		cfg,
		Request{Event: EventBeforePromptBuild},
		runner,
	)
	if err != nil {
		t.Fatalf("RunConfig returned error: %v", err)
	}
	if result.AppendContext != "turn-only note" {
		t.Fatalf("append_context = %q, want byte-identical turn channel", result.AppendContext)
	}
	if len(result.AppendSystemSections) != 3 {
		t.Fatalf("append_system_sections = %#v, want three sections", result.AppendSystemSections)
	}
	sectionsByID := make(map[string]SystemSectionOutput, len(result.AppendSystemSections))
	for _, section := range result.AppendSystemSections {
		sectionsByID[section.ID] = section
	}
	stable, defaulted, clamped := sectionsByID["stable"], sectionsByID["defaulted"], sectionsByID["clamped"]
	if stable.HookName != "policy-hook" || defaulted.HookName != "policy-hook" || clamped.HookName != "policy-hook" {
		t.Fatalf("hook provenance missing: %#v", result.AppendSystemSections)
	}
	if stable.Retention != SystemSectionRetentionPreferred || stable.Cache != SystemSectionCacheStable {
		t.Fatalf("explicit section policy = %#v, want preferred/stable", stable)
	}
	if defaulted.Retention != SystemSectionRetentionOptional || defaulted.Cache != SystemSectionCacheDynamic {
		t.Fatalf("default section policy = %#v, want optional/dynamic", defaulted)
	}
	if clamped.Retention != SystemSectionRetentionPreferred {
		t.Fatalf("required retention = %q, want clamped preferred", clamped.Retention)
	}
	if !slices.Equal(clamped.WarningCodes, []string{WarningSystemSectionRequiredClamped}) {
		t.Fatalf("clamped section warning codes = %#v, want required-retention warning", clamped.WarningCodes)
	}
	if len(result.Warnings) != 1 ||
		result.Warnings[0].Code != WarningSystemSectionRequiredClamped ||
		result.Warnings[0].HookName != "policy-hook" ||
		result.Warnings[0].SectionID != "clamped" {
		t.Fatalf("warnings = %#v, want required-retention clamp warning", result.Warnings)
	}
}

func TestRunConfigOrdersAppendSystemSectionsByHookThenDeclaredID(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Version: 1,
		Hooks: []Hook{
			{
				Name:     "low",
				Event:    EventBeforePromptBuild,
				Priority: 10,
				Actions:  []HookAction{{Type: ActionTool, Tool: "low"}},
			},
			{
				Name:     "high",
				Event:    EventBeforePromptBuild,
				Priority: 20,
				Actions:  []HookAction{{Type: ActionTool, Tool: "high"}},
			},
		},
	}
	runner := &fakeToolRunner{
		fn: func(_ context.Context, name string, _ map[string]any) (any, error) {
			if name == "high" {
				return map[string]any{"append_system_section": []any{
					map[string]any{"id": "b", "text": "high b"},
					map[string]any{"id": "a", "text": "high a"},
				}}, nil
			}
			return map[string]any{"append_system_section": []any{
				map[string]any{"id": "z", "text": "low z"},
				map[string]any{"id": "a", "text": "low a"},
			}}, nil
		},
	}

	result, err := NewService(nil, nil).RunConfig(
		context.Background(),
		cfg,
		Request{Event: EventBeforePromptBuild},
		runner,
	)
	if err != nil {
		t.Fatalf("RunConfig returned error: %v", err)
	}
	got := make([]string, 0, len(result.AppendSystemSections))
	for _, section := range result.AppendSystemSections {
		got = append(got, section.HookName+"."+section.ID)
	}
	want := []string{"high.a", "high.b", "low.a", "low.z"}
	if !slices.Equal(got, want) {
		t.Fatalf("section order = %v, want hook execution then declared ID %v", got, want)
	}
}

func TestRunConfigIgnoresInvalidAppendSystemSectionsWithWarnings(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Version: 1,
		Hooks: []Hook{{
			Name:  "invalid-sections",
			Event: EventAfterPromptBuild,
			Actions: []HookAction{
				{Type: ActionTool, Tool: "mixed-array"},
				{Type: ActionTool, Tool: "invalid-root"},
			},
		}},
	}
	runner := &fakeToolRunner{
		fn: func(_ context.Context, name string, _ map[string]any) (any, error) {
			switch name {
			case "mixed-array":
				return map[string]any{
					"append_system_section": []any{
						map[string]any{"id": "valid", "text": "keep me"},
						"not an object",
						map[string]any{"id": "missing-text"},
						map[string]any{"text": "bad retention", "retention": "forever"},
						map[string]any{"text": "bad cache", "cache": "immutable"},
					},
				}, nil
			case "invalid-root":
				return map[string]any{"append_system_section": "not an object or array"}, nil
			default:
				return nil, nil
			}
		},
	}

	result, err := NewService(nil, nil).RunConfig(
		context.Background(),
		cfg,
		Request{Event: EventAfterPromptBuild},
		runner,
	)
	if err != nil {
		t.Fatalf("RunConfig returned error: %v", err)
	}
	if len(result.AppendSystemSections) != 1 || result.AppendSystemSections[0].ID != "valid" {
		t.Fatalf("append_system_sections = %#v, want only valid sibling", result.AppendSystemSections)
	}
	if len(result.Warnings) != 5 {
		t.Fatalf("warnings = %#v, want five invalid-shape warnings", result.Warnings)
	}
	for _, warning := range result.Warnings {
		if warning.Code != WarningInvalidAppendSystemSection || warning.HookName != "invalid-sections" {
			t.Fatalf("warning = %#v, want content-light invalid-section warning", warning)
		}
	}
}

func TestRunConfigLimitsAppendSystemSectionText(t *testing.T) {
	t.Parallel()

	large := "HEAD\n" + strings.Repeat("system detail ", 300) + "\nTAIL"
	cfg := Config{
		Version:  1,
		Defaults: Defaults{MaxOutputBytes: 192},
		Hooks: []Hook{{
			Name:    "limited-system-section",
			Event:   EventBeforePromptBuild,
			Actions: []HookAction{{Type: ActionTool, Tool: "section"}},
		}},
	}
	runner := &fakeToolRunner{
		fn: func(context.Context, string, map[string]any) (any, error) {
			return map[string]any{
				"append_system_section": []any{
					map[string]any{"id": "first", "text": large},
					map[string]any{"id": "second", "text": large},
				},
			}, nil
		},
	}

	result, err := NewService(nil, nil).RunConfig(
		context.Background(),
		cfg,
		Request{Event: EventBeforePromptBuild},
		runner,
	)
	if err != nil {
		t.Fatalf("RunConfig returned error: %v", err)
	}
	if len(result.AppendSystemSections) == 0 {
		t.Fatal("append_system_sections is empty, want capped section output")
	}
	total := 0
	for _, section := range result.AppendSystemSections {
		total += len(section.Text)
	}
	if total > 192 {
		t.Fatalf("aggregate section text bytes = %d, want hook output cap <= 192", total)
	}
	text := result.AppendSystemSections[0].Text
	if len(text) >= len(large) {
		t.Fatalf("first section text bytes = %d, want limited output", len(text))
	}
	assertHookTextPreservesHeadTail(t, text)
	if !slices.Contains(result.AppendSystemSections[0].WarningCodes, WarningAppendSystemSectionOutputLimited) {
		t.Fatalf("section warning codes = %#v, want output-limit audit", result.AppendSystemSections[0].WarningCodes)
	}
	var limitedWarning bool
	for _, warning := range result.Warnings {
		if warning.Code == WarningAppendSystemSectionOutputLimited && warning.SectionID == "first" {
			limitedWarning = true
		}
	}
	if !limitedWarning {
		t.Fatalf("warnings = %#v, want output-limit warning for truncated section", result.Warnings)
	}
}

func TestRunConfigLimitsAppendSystemSectionToDefaultCap(t *testing.T) {
	t.Parallel()

	large := "HEAD\n" + strings.Repeat("hook system detail ", 300) + "\nTAIL"
	cfg := Config{
		Version:  1,
		Defaults: Defaults{MaxOutputBytes: 192},
		Hooks: []Hook{{
			Name:   "hook system section",
			Event:  EventBeforePromptBuild,
			source: hookSource{Kind: sourceKindUser},
			Actions: []HookAction{
				{Type: ActionTool, Tool: "section-one"},
				{Type: ActionTool, Tool: "section-two"},
			},
		}},
	}
	runner := &fakeToolRunner{
		fn: func(_ context.Context, name string, _ map[string]any) (any, error) {
			return map[string]any{
				"append_system_section": map[string]any{"id": name, "text": large},
			}, nil
		},
	}

	result, err := NewService(nil, nil).RunConfig(
		context.Background(),
		cfg,
		Request{Event: EventBeforePromptBuild},
		runner,
	)
	if err != nil {
		t.Fatalf("RunConfig returned error: %v", err)
	}
	total := 0
	for _, section := range result.AppendSystemSections {
		total += len(section.Text)
	}
	if total > 192 {
		t.Fatalf("aggregate section text bytes = %d, want default cap <= 192", total)
	}
}

func TestRunConfigLimitsAppendSystemSectionCount(t *testing.T) {
	t.Parallel()

	sections := make([]any, 0, maxAppendSystemSections+2)
	for i := range maxAppendSystemSections + 2 {
		sections = append(sections, map[string]any{
			"id":   fmt.Sprintf("section-%d", i),
			"text": "x",
		})
	}
	cfg := Config{
		Version: 1,
		Hooks: []Hook{{
			Name:    "many sections",
			Event:   EventBeforePromptBuild,
			Actions: []HookAction{{Type: ActionTool, Tool: "sections"}},
		}},
	}
	runner := &fakeToolRunner{
		fn: func(context.Context, string, map[string]any) (any, error) {
			return map[string]any{"append_system_section": sections}, nil
		},
	}

	result, err := NewService(nil, nil).RunConfig(
		context.Background(),
		cfg,
		Request{Event: EventBeforePromptBuild},
		runner,
	)
	if err != nil {
		t.Fatalf("RunConfig returned error: %v", err)
	}
	if len(result.AppendSystemSections) != maxAppendSystemSections {
		t.Fatalf("append_system_sections = %d, want cap %d", len(result.AppendSystemSections), maxAppendSystemSections)
	}
	if len(result.ActionResults) != 1 ||
		len(result.ActionResults[0].AppendSystemSections) != maxAppendSystemSections {
		t.Fatalf("action results retain unbounded sections: %#v", result.ActionResults)
	}
	if len(result.Warnings) != 1 ||
		result.Warnings[0].Code != WarningAppendSystemSectionOutputLimited {
		t.Fatalf("warnings = %#v, want one bounded output-limit warning", result.Warnings)
	}
}

func TestRunConfigLimitsAggregatedAppendContext(t *testing.T) {
	t.Parallel()

	large := "HEAD\n" + strings.Repeat("context detail ", 300) + "\nTAIL"
	cfg := Config{
		Version: 1,
		Defaults: Defaults{
			MaxOutputBytes: 192,
		},
		Hooks: []Hook{{
			Name:  "append-context",
			Event: EventBeforeModelCall,
			Actions: []HookAction{
				{Type: ActionTool, Tool: "append_one"},
				{Type: ActionTool, Tool: "append_two"},
			},
		}},
	}
	runner := &fakeToolRunner{
		fn: func(context.Context, string, map[string]any) (any, error) {
			return map[string]any{
				"decision":       DecisionAppendContext,
				"append_context": large,
			}, nil
		},
	}

	result, err := NewService(nil, nil).RunConfig(context.Background(), cfg, Request{Event: EventBeforeModelCall}, runner)
	if err != nil {
		t.Fatalf("RunConfig returned error: %v", err)
	}
	if len(result.AppendContext) > 192 {
		t.Fatalf("append_context bytes = %d, want <= 192", len(result.AppendContext))
	}
	if len(result.AppendContext) >= len(large) {
		t.Fatalf("append_context was not limited after aggregation")
	}
	assertHookTextPreservesHeadTail(t, result.AppendContext)
}

func TestRunConfigOnErrorBlockDenies(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Version: 1,
		Hooks: []Hook{{
			Name:  "block-on-error",
			Event: EventPreToolUse,
			Actions: []HookAction{{
				Type:    ActionTool,
				Tool:    "failing_tool",
				OnError: OnErrorBlock,
			}},
		}},
	}
	runner := &fakeToolRunner{
		fn: func(context.Context, string, map[string]any) (any, error) {
			return nil, errors.New("tool failed")
		},
	}

	result, err := NewService(nil, nil).RunConfig(context.Background(), cfg, Request{Event: EventPreToolUse}, runner)
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("RunConfig error = %v, want ErrDenied", err)
	}
	if result.Decision != DecisionDeny {
		t.Fatalf("decision = %q, want %q", result.Decision, DecisionDeny)
	}
	if result.Reason != "tool failed" {
		t.Fatalf("reason = %q, want tool error", result.Reason)
	}
}

func TestRunLimitsCommandAppendContext(t *testing.T) {
	t.Parallel()

	large := "HEAD\n" + strings.Repeat("context detail ", 300) + "\nTAIL"
	cfg := `{
		"version": 1,
		"enabled": true,
		"defaults": {
			"timeout": "3s",
			"max_output_bytes": 192
		},
		"hooks": [{
			"name": "command context",
			"event": "BeforeModelCall",
			"actions": [{
				"type": "command",
				"command": "node /data/.memoh/hook.js"
			}]
		}]
	}`
	payload, err := json.Marshal(map[string]string{
		"decision":       DecisionAppendContext,
		"append_context": large,
	})
	if err != nil {
		t.Fatalf("marshal hook output: %v", err)
	}
	server := &hookBridgeTestServer{
		files: map[string][]byte{
			DefaultConfigPath: []byte(cfg),
		},
		stdout: string(payload),
	}
	service := NewService(nil, hookBridgeProvider{client: newHookBridgeTestClient(t, server)})

	result, err := service.Run(context.Background(), Request{
		Event: EventBeforeModelCall,
		BotID: "bot-1",
		Workspace: WorkspaceInfo{
			CWD:     "/data/workspace",
			Runtime: "container",
		},
	}, nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(result.AppendContext) > 192 {
		t.Fatalf("append_context bytes = %d, want <= 192", len(result.AppendContext))
	}
	if len(result.AppendContext) >= len(large) {
		t.Fatalf("append_context was not limited")
	}
	assertHookTextPreservesHeadTail(t, result.AppendContext)
}

func TestRunLimitsCommandMetadataAppendContext(t *testing.T) {
	t.Parallel()

	large := "HEAD\n" + strings.Repeat("metadata context detail ", 300) + "\nTAIL"
	cfg := `{
		"version": 1,
		"enabled": true,
		"defaults": {
			"timeout": "3s",
			"max_output_bytes": 192
		},
		"hooks": [{
			"name": "command metadata context",
			"event": "BeforeModelCall",
			"actions": [{
				"type": "command",
				"command": "node /data/.memoh/hook.js"
			}]
		}]
	}`
	payload, err := json.Marshal(map[string]any{
		"decision": DecisionAppendContext,
		"metadata": map[string]any{
			"append_context": large,
		},
	})
	if err != nil {
		t.Fatalf("marshal hook output: %v", err)
	}
	server := &hookBridgeTestServer{
		files: map[string][]byte{
			DefaultConfigPath: []byte(cfg),
		},
		stdout: string(payload),
	}
	service := NewService(nil, hookBridgeProvider{client: newHookBridgeTestClient(t, server)})

	result, err := service.Run(context.Background(), Request{
		Event: EventBeforeModelCall,
		BotID: "bot-1",
		Workspace: WorkspaceInfo{
			CWD:     "/data/workspace",
			Runtime: "container",
		},
	}, nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(result.AppendContext) > 192 {
		t.Fatalf("append_context bytes = %d, want <= 192", len(result.AppendContext))
	}
	if len(result.AppendContext) >= len(large) {
		t.Fatalf("metadata append_context was not limited")
	}
	assertHookTextPreservesHeadTail(t, result.AppendContext)
}

func TestRunLimitsCommandRawStdoutMetadata(t *testing.T) {
	t.Parallel()

	large := "HEAD\n" + strings.Repeat("raw stdout ", 300) + "\nTAIL"
	largeErr := "HEAD\n" + strings.Repeat("raw stderr ", 300) + "\nTAIL"
	cfg := `{
		"version": 1,
		"enabled": true,
		"defaults": {
			"timeout": "3s",
			"max_output_bytes": 192
		},
		"hooks": [{
			"name": "command raw stdout",
			"event": "BeforeModelCall",
			"actions": [{
				"type": "command",
				"command": "node /data/.memoh/hook.js"
			}]
		}]
	}`
	server := &hookBridgeTestServer{
		files: map[string][]byte{
			DefaultConfigPath: []byte(cfg),
		},
		stdout: large,
		stderr: largeErr,
	}
	service := NewService(nil, hookBridgeProvider{client: newHookBridgeTestClient(t, server)})

	result, err := service.Run(context.Background(), Request{
		Event: EventBeforeModelCall,
		BotID: "bot-1",
		Workspace: WorkspaceInfo{
			CWD:     "/data/workspace",
			Runtime: "container",
		},
	}, nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	action := result.ActionResults[0]
	raw, ok := action.Metadata["raw_stdout"].(string)
	if !ok {
		t.Fatalf("raw_stdout metadata = %#v, want string", action.Metadata["raw_stdout"])
	}
	if len(raw) > 192 {
		t.Fatalf("raw_stdout bytes = %d, want <= 192", len(raw))
	}
	if len(raw) >= len(large) {
		t.Fatalf("raw_stdout was not limited")
	}
	assertHookTextPreservesHeadTail(t, raw)
	if len(action.Stdout) > 192 {
		t.Fatalf("stdout bytes = %d, want <= 192", len(action.Stdout))
	}
	if len(action.Stderr) > 192 {
		t.Fatalf("stderr bytes = %d, want <= 192", len(action.Stderr))
	}
	assertHookTextPreservesHeadTail(t, action.Stdout)
	assertHookTextPreservesHeadTail(t, action.Stderr)
}

func assertHookTextPreservesHeadTail(t *testing.T, text string) {
	t.Helper()
	for _, want := range []string{"[memoh pruned]", "HEAD", "TAIL"} {
		if !strings.Contains(text, want) {
			t.Fatalf("hook text missing %q:\n%s", want, text)
		}
	}
}

func TestParseConfigRejectsReservedMCPTool(t *testing.T) {
	t.Parallel()

	_, err := ParseConfig([]byte(`{
		"version": 1,
		"hooks": [{
			"name": "future mcp",
			"event": "PreToolUse",
			"actions": [{"type": "mcp_tool", "server": "example", "tool": "ping"}]
		}]
	}`))
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("ParseConfig error = %v, want ErrUnsupported", err)
	}
}

func TestParseConfigRejectsInvalidNumericTimeout(t *testing.T) {
	t.Parallel()

	_, err := ParseConfig([]byte(`{
		"version": 1,
		"hooks": [{
			"name": "bad timeout",
			"event": "PreToolUse",
			"actions": [{"type": "tool", "tool": "noop", "timeout": "10wat"}]
		}]
	}`))
	if err == nil {
		t.Fatal("ParseConfig returned nil error for invalid timeout")
	}
}

func TestRunLoadsConfigAndExecutesCommandHook(t *testing.T) {
	t.Parallel()

	cfg := `{
		"version": 1,
		"enabled": true,
		"defaults": {
			"timeout": "3s",
			"max_output_bytes": 4096
		},
		"env": {
			"HOOK_ENV": "enabled"
		},
		"hooks": [{
			"name": "command logger",
			"event": "PostToolUse",
			"matcher": "^exec$",
			"actions": [{
				"type": "command",
				"command": "node /data/.memoh/hook.js",
				"work_dir": "/data/project"
			}]
		}]
	}`
	server := &hookBridgeTestServer{
		files: map[string][]byte{
			DefaultConfigPath: []byte(cfg),
		},
		stdout: `{"decision":"append_context","append_context":"from command","metadata":{"source":"test"}}`,
	}
	client := newHookBridgeTestClient(t, server)
	service := NewService(nil, hookBridgeProvider{client: client})

	result, err := service.Run(context.Background(), Request{
		Event: EventPostToolUse,
		BotID: "bot-1",
		Tool:  &ToolPayload{Name: "exec", Result: "ok"},
		Workspace: WorkspaceInfo{
			CWD:     "/data/workspace",
			Runtime: "container",
		},
	}, nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Decision != DecisionAppendContext {
		t.Fatalf("decision = %q, want %q", result.Decision, DecisionAppendContext)
	}
	if result.AppendContext != "from command" {
		t.Fatalf("append_context = %q, want command context", result.AppendContext)
	}

	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.execs) != 1 {
		t.Fatalf("exec count = %d, want 1", len(server.execs))
	}
	exec := server.execs[0]
	if exec.command != "node /data/.memoh/hook.js" {
		t.Fatalf("command = %q", exec.command)
	}
	if exec.workDir != "/data/project" {
		t.Fatalf("work_dir = %q, want explicit hook work_dir", exec.workDir)
	}
	if exec.timeout != 3 {
		t.Fatalf("timeout = %d, want 3", exec.timeout)
	}
	if !slices.Contains(exec.env, "HOOK_ENV=enabled") || !slices.Contains(exec.env, "MEMOH_HOOK_EVENT=PostToolUse") {
		t.Fatalf("env = %v, missing hook env values", exec.env)
	}
	var req Request
	if err := json.Unmarshal(exec.stdin, &req); err != nil {
		t.Fatalf("stdin is not hook request JSON: %v", err)
	}
	if req.HookName != "command logger" || req.Tool == nil || req.Tool.Name != "exec" {
		t.Fatalf("stdin request = %+v", req)
	}
}

func TestRunCommandHookDoesNotUseTargetWorkspaceAsExecutionDirectory(t *testing.T) {
	t.Parallel()

	cfg := `{
		"version": 1,
		"enabled": true,
		"hooks": [{
			"name": "remote guard",
			"event": "BeforeWorkspaceCommand",
			"actions": [{
				"type": "command",
				"command": "node /data/.memoh/guard.js"
			}]
		}]
	}`
	server := &hookBridgeTestServer{
		files:  map[string][]byte{DefaultConfigPath: []byte(cfg)},
		stdout: `{"decision":"allow"}`,
	}
	service := NewService(nil, hookBridgeProvider{client: newHookBridgeTestClient(t, server)})

	_, err := service.Run(context.Background(), Request{
		Event: EventBeforeWorkspaceCommand,
		BotID: "bot-1",
		Workspace: WorkspaceInfo{
			CWD:     `C:\Users\alice\project`,
			Runtime: bridge.WorkspaceBackendRemote,
		},
	}, nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.execs) != 1 {
		t.Fatalf("exec count = %d, want 1", len(server.execs))
	}
	exec := server.execs[0]
	if exec.workDir != DefaultWorkDir {
		t.Fatalf("hook work_dir = %q, want primary workspace %q", exec.workDir, DefaultWorkDir)
	}
	var req Request
	if err := json.Unmarshal(exec.stdin, &req); err != nil {
		t.Fatalf("stdin is not hook request JSON: %v", err)
	}
	if req.Workspace.CWD != `C:\Users\alice\project` || req.Workspace.Runtime != bridge.WorkspaceBackendRemote {
		t.Fatalf("hook request lost target workspace metadata: %+v", req.Workspace)
	}
}

func TestRunPinsWorkspaceTargetAcrossLoadAndExecution(t *testing.T) {
	cfg := `{
		"version": 1,
		"enabled": true,
		"hooks": [{
			"name": "pinned",
			"event": "PostToolUse",
			"actions": [{"type": "command", "command": "true"}]
		}]
	}`
	nativeServer := &hookBridgeTestServer{
		files:  map[string][]byte{DefaultConfigPath: []byte(cfg)},
		stdout: `{"decision":"allow"}`,
	}
	remoteServer := &hookBridgeTestServer{files: map[string][]byte{}}
	provider := &switchingHookBridgeProvider{clients: map[string]*bridge.Client{
		"native":        newHookBridgeTestClient(t, nativeServer),
		"remote-target": newHookBridgeTestClient(t, remoteServer),
	}}
	service := NewService(nil, provider)

	if _, err := service.Run(context.Background(), Request{
		Event: EventPostToolUse,
		BotID: "bot-1",
		Tool:  &ToolPayload{Name: "exec", Result: "ok"},
	}, nil); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if provider.resolveCalls != 1 {
		t.Fatalf("target resolver calls = %d, want 1", provider.resolveCalls)
	}
	if got := strings.Join(provider.clientTargets, ","); got != "native,native" {
		t.Fatalf("MCP client targets = %q, want native,native", got)
	}
	nativeServer.mu.Lock()
	nativeExecs := len(nativeServer.execs)
	nativeServer.mu.Unlock()
	remoteServer.mu.Lock()
	remoteExecs := len(remoteServer.execs)
	remoteServer.mu.Unlock()
	if nativeExecs != 1 || remoteExecs != 0 {
		t.Fatalf("execs native=%d remote=%d, want 1/0", nativeExecs, remoteExecs)
	}
}

func TestLoadCreatesEmptyConfigWhenMissing(t *testing.T) {
	t.Parallel()

	server := &hookBridgeTestServer{files: map[string][]byte{}}
	client := newHookBridgeTestClient(t, server)
	service := NewService(nil, hookBridgeProvider{client: client})

	cfg, exists, err := service.Load(context.Background(), "bot-1")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if !exists {
		t.Fatal("Load exists = false, want true after creating default config")
	}
	if cfg.Version != 1 {
		t.Fatalf("version = %d, want 1", cfg.Version)
	}
	if cfg.Enabled == nil || !*cfg.Enabled {
		t.Fatalf("enabled = %v, want true", cfg.Enabled)
	}
	if len(cfg.Hooks) != 0 {
		t.Fatalf("hooks len = %d, want 0", len(cfg.Hooks))
	}

	server.mu.Lock()
	raw := append([]byte(nil), server.files[DefaultConfigPath]...)
	server.mu.Unlock()
	if len(raw) == 0 {
		t.Fatalf("default hooks file was not written at %s", DefaultConfigPath)
	}
	var written struct {
		Version int   `json:"version"`
		Enabled bool  `json:"enabled"`
		Hooks   []any `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &written); err != nil {
		t.Fatalf("written config is not JSON: %v", err)
	}
	if written.Version != 1 || !written.Enabled {
		t.Fatalf("written config = %+v, want version 1 and enabled true", written)
	}
	if written.Hooks == nil {
		t.Fatal("written config omitted hooks field, want empty hooks array")
	}
	if len(written.Hooks) != 0 {
		t.Fatalf("written hooks len = %d, want 0", len(written.Hooks))
	}
}

type hookBridgeProvider struct {
	client *bridge.Client
}

type switchingHookBridgeProvider struct {
	clients       map[string]*bridge.Client
	resolveCalls  int
	clientTargets []string
}

func (p *switchingHookBridgeProvider) CurrentWorkspaceTargetID(context.Context, string) (string, error) {
	p.resolveCalls++
	if p.resolveCalls == 1 {
		return "native", nil
	}
	return "remote-target", nil
}

func (p *switchingHookBridgeProvider) MCPClient(ctx context.Context, _ string) (*bridge.Client, error) {
	targetID := bridge.WorkspaceTargetFromContext(ctx)
	p.clientTargets = append(p.clientTargets, targetID)
	client := p.clients[targetID]
	if client == nil {
		return nil, errors.New("unexpected workspace target: " + targetID)
	}
	return client, nil
}

func (p hookBridgeProvider) MCPClient(context.Context, string) (*bridge.Client, error) {
	return p.client, nil
}

type capturedExec struct {
	command string
	workDir string
	timeout int32
	stdin   []byte
	env     []string
}

type hookBridgeTestServer struct {
	pb.UnimplementedContainerServiceServer
	files    map[string][]byte
	stdout   string
	stderr   string
	exitCode int32

	mu    sync.Mutex
	execs []capturedExec
}

func (s *hookBridgeTestServer) ReadRaw(req *pb.ReadRawRequest, stream pb.ContainerService_ReadRawServer) error {
	s.mu.Lock()
	data, ok := s.files[req.GetPath()]
	data = append([]byte(nil), data...)
	s.mu.Unlock()
	if !ok {
		return status.Errorf(codes.NotFound, "open: open %s: no such file or directory", req.GetPath())
	}
	if len(data) == 0 {
		return nil
	}
	return stream.Send(&pb.DataChunk{Data: data})
}

func (s *hookBridgeTestServer) WriteFile(_ context.Context, req *pb.WriteFileRequest) (*pb.WriteFileResponse, error) {
	if strings.TrimSpace(req.GetPath()) == "" {
		return nil, status.Error(codes.InvalidArgument, "path is required")
	}
	s.mu.Lock()
	if s.files == nil {
		s.files = map[string][]byte{}
	}
	s.files[req.GetPath()] = append([]byte(nil), req.GetContent()...)
	s.mu.Unlock()
	return &pb.WriteFileResponse{}, nil
}

func (s *hookBridgeTestServer) Exec(stream pb.ContainerService_ExecServer) error {
	input, err := stream.Recv()
	if err != nil {
		return err
	}
	var stdin []byte
	if data := input.GetStdinData(); len(data) > 0 {
		stdin = append(stdin, data...)
	}
	for {
		msg, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			return recvErr
		}
		if data := msg.GetStdinData(); len(data) > 0 {
			stdin = append(stdin, data...)
		}
	}
	s.mu.Lock()
	s.execs = append(s.execs, capturedExec{
		command: input.GetCommand(),
		workDir: input.GetWorkDir(),
		timeout: input.GetTimeoutSeconds(),
		stdin:   stdin,
		env:     append([]string(nil), input.GetEnv()...),
	})
	s.mu.Unlock()

	if strings.TrimSpace(s.stdout) != "" {
		if err := stream.Send(&pb.ExecOutput{Stream: pb.ExecOutput_STDOUT, Data: []byte(s.stdout)}); err != nil {
			return err
		}
	}
	if strings.TrimSpace(s.stderr) != "" {
		if err := stream.Send(&pb.ExecOutput{Stream: pb.ExecOutput_STDERR, Data: []byte(s.stderr)}); err != nil {
			return err
		}
	}
	return stream.Send(&pb.ExecOutput{Stream: pb.ExecOutput_EXIT, ExitCode: s.exitCode})
}

func newHookBridgeTestClient(t *testing.T, server pb.ContainerServiceServer) *bridge.Client {
	t.Helper()

	listener := bufconn.Listen(hookTestBufSize)
	grpcServer := grpc.NewServer()
	pb.RegisterContainerServiceServer(grpcServer, server)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = grpcServer.Serve(listener)
	}()
	t.Cleanup(func() {
		grpcServer.Stop()
		<-done
	})

	dialer := func(ctx context.Context, _ string) (net.Conn, error) {
		return listener.DialContext(ctx)
	}
	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
	})
	return bridge.NewClientFromConn(conn)
}
