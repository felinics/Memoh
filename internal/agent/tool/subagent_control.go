package tools

import (
	"context"
	"errors"
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/toolexec"
)

type SubagentRuntimeState struct {
	RunID          string `json:"run_id,omitempty"`
	Status         string `json:"status"`
	Code           string `json:"code,omitempty"`
	SteerSupported bool   `json:"steer_supported"`
	Active         bool   `json:"-"`
}

type SubagentControlInput struct {
	Parent    SessionContext
	SessionID string
	RunID     string
	Action    string
	Message   string
	ControlID string
}

type SubagentControlResult struct {
	SubagentRuntimeState
	Accepted bool   `json:"accepted"`
	ItemID   string `json:"item_id,omitempty"`
}

type SubagentRuntimeService interface {
	SnapshotSubagent(context.Context, SessionContext, string) (SubagentRuntimeState, error)
	WaitSubagentIdle(context.Context, SessionContext, string) error
	ControlSubagent(context.Context, SubagentControlInput) (SubagentControlResult, error)
}

type controlAgentArgs struct {
	ID      string `json:"id" jsonschema:"Child agent_id returned by spawn_agent or list_agents."`
	RunID   string `json:"run_id" jsonschema:"Exact run_id from list_agents. A replacement run is never controlled."`
	Action  string `json:"action" jsonschema:"Stop the specified run, or steer it when steer_supported is true."`
	Message string `json:"message,omitempty" jsonschema:"Required for steer; the additional instruction for this run."`
}

func (p *SpawnProvider) controlAgentTool(parent SessionContext) toolexec.Tool {
	return toolexec.Tool{
		Name:        ToolControlAgent().String(),
		Description: "Stop or steer one exact run of a child created by this parent. Stop acknowledgement may report stopping; observe list_agents until terminal. A stopped or failed External Agent run can also stop the workspace processes Memoh traces to that child, including services from its earlier runs; processes that detached from it may survive. Steer requires advertised support.",
		Parameters:  toolexec.SchemaFor[controlAgentArgs](toolexec.Enum("action", "stop", "steer")),
		Execute: toolexec.Typed(func(ctx *toolexec.ToolExecContext, args controlAgentArgs) (sdk.ToolOutput, error) {
			if err := validateParentSession(parent); err != nil {
				return sdk.ToolOutput{}, err
			}
			id, err := normalizeAgentID(args.ID)
			if err != nil {
				return sdk.ToolOutput{}, err
			}
			if strings.TrimSpace(args.RunID) == "" || (args.Action != "stop" && args.Action != "steer") || (args.Action == "steer" && strings.TrimSpace(args.Message) == "") {
				return sdk.ToolOutput{}, errors.New("run_id, a valid action, and a message for steer are required")
			}
			child, err := p.findAgent(ctx.Context, parent, id)
			if err != nil {
				return sdk.ToolOutput{}, err
			}
			runtime, ok := p.admitter.(SubagentRuntimeService)
			if !ok {
				return sdk.ToolOutput{}, errors.New("subagent runtime control unavailable")
			}
			return toolexec.OutputPair(runtime.ControlSubagent(ctx.Context, SubagentControlInput{Parent: parent, SessionID: child.SessionID, RunID: strings.TrimSpace(args.RunID), Action: args.Action, Message: args.Message, ControlID: ctx.ToolCallID}))
		}),
	}
}
