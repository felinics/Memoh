package handlers

import (
	"context"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	tools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/agent/toolexec"
)

type hookRunnerTestProvider struct{}

func (hookRunnerTestProvider) Tools(context.Context, tools.SessionContext) ([]toolexec.Tool, error) {
	return []toolexec.Tool{toolexec.Define("hook_probe", "returns a hook decision",
		func(*toolexec.ToolExecContext, struct{}) (sdk.ToolOutput, error) {
			return toolexec.OutputFromValue(map[string]any{"decision": "deny", "reason": "probe"}), nil
		},
	)}, nil
}

// The hook service reads decision/reason off the value the runner returns;
// the SDK's {text|json} envelope hides them and every test run reports allow.
func TestHookTestToolRunnerReturnsDecodedToolValue(t *testing.T) {
	agent := native.New(native.Deps{})
	agent.SetToolProviders([]tools.ToolProvider{hookRunnerTestProvider{}})
	runner := hookTestToolRunner{agent: agent, cfg: native.RunConfig{SupportsToolCall: true}}

	output, err := runner.RunHookTool(context.Background(), "hook_probe", map[string]any{})
	if err != nil {
		t.Fatalf("RunHookTool: %v", err)
	}
	value, ok := output.(map[string]any)
	if !ok {
		t.Fatalf("output = %#v, want the tool's object", output)
	}
	if value["decision"] != "deny" || value["reason"] != "probe" {
		t.Fatalf("output = %#v, want decision/reason readable", value)
	}
}
