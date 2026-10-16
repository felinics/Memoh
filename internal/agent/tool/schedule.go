package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/jsonschema-go/jsonschema"

	"github.com/felinics/memoh/internal/agent/toolexec"
	sched "github.com/felinics/memoh/internal/schedule"
)

type ScheduleProvider struct {
	service Scheduler
	logger  *slog.Logger
}

// Scheduler is the interface for schedule CRUD operations.
type Scheduler interface {
	List(ctx context.Context, botID string) ([]sched.Schedule, error)
	Get(ctx context.Context, id string) (sched.Schedule, error)
	Create(ctx context.Context, botID string, req sched.CreateRequest) (sched.Schedule, error)
	Update(ctx context.Context, id string, req sched.UpdateRequest) (sched.Schedule, error)
	Delete(ctx context.Context, id string) error
}

func NewScheduleProvider(log *slog.Logger, service Scheduler) *ScheduleProvider {
	if log == nil {
		log = slog.Default()
	}
	return &ScheduleProvider{
		service: service,
		logger:  log.With(slog.String("tool", "schedule")),
	}
}

// Usage describes how the schedule tool group works together. Injected only
// when the schedule tools are registered (main-agent sessions with a schedule
// service); guidance is emitted only when schedule tools are actually present.
func (*ScheduleProvider) Usage(_ context.Context, _ SessionContext, available AvailableTools) string {
	var parts []string
	delivery := "include an instruction to deliver results to a person or channel when messaging is available"
	if sendRef, ok := available.Ref(ToolSend()); ok {
		delivery = "use " + sendRef + " inside the command with explicit `platform` and `target` to deliver results to a person or channel"
	} else if speakRef, ok := available.Ref(ToolSpeak()); ok {
		delivery = "use " + speakRef + " inside the command with explicit `platform` and `target` to deliver voice results to a person or channel"
	}
	if createRef, ok := available.Ref(ToolCreateSchedule()); ok {
		parts = append(parts, "You can create and manage scheduled tasks via cron.")
		parts = append(parts, "Use "+createRef+" to create a new task — fill `command` with natural language.")
		parts = append(parts, "Interpret the cron `pattern` in the effective timezone stated in the system context; do not convert part of it to UTC.")
		parts = append(parts, "After creation succeeds, immediately confirm the scheduled task to the user and end the turn; do not wait for it to fire or poll schedules or messages.")
		parts = append(parts, "When the cron pattern fires, you will receive a message with your `command`; "+delivery+".")
		var execHints []string
		if ref, ok := available.Ref(ToolListModels()); ok {
			execHints = append(execHints, "a specific model (`model_id` from "+ref+")")
		}
		if ref, ok := available.Ref(ToolListACPAgents()); ok {
			execHints = append(execHints, "an ACP agent (`acp_agent_id` from "+ref+")")
		}
		if ref, ok := available.Ref(ToolListWorkdirs()); ok {
			execHints = append(execHints, "a workdir (`workdir_id` from "+ref+")")
		}
		if len(execHints) > 0 {
			parts = append(parts, "Scheduled tasks can also run with "+strings.Join(execHints, ", ")+", a `reasoning_effort` override, or inside an existing session (`session_id`).")
		}
	}
	if ref, ok := available.Ref(ToolListSchedule()); ok {
		parts = append(parts, "Use "+ref+" to list scheduled tasks.")
	}
	if ref, ok := available.Ref(ToolGetSchedule()); ok {
		parts = append(parts, "Use "+ref+" to inspect one scheduled task by id.")
	}
	if ref, ok := available.Ref(ToolUpdateSchedule()); ok {
		parts = append(parts, "Use "+ref+" to update an existing scheduled task.")
	}
	if ref, ok := available.Ref(ToolDeleteSchedule()); ok {
		parts = append(parts, "Use "+ref+" to delete a scheduled task.")
	}
	return usageSection("Scheduled tasks", parts)
}

func (p *ScheduleProvider) Tools(_ context.Context, session SessionContext) ([]toolexec.Tool, error) {
	if p.service == nil {
		return nil, nil
	}
	sess := session
	return []toolexec.Tool{
		{
			Name: ToolListSchedule().String(), Description: "List schedules for current bot",
			Parameters: toolexec.SchemaFor[listScheduleArgs](),
			Execute: toolexec.Typed(func(ctx *toolexec.ToolExecContext, _ listScheduleArgs) (sdk.ToolOutput, error) {
				botID := strings.TrimSpace(sess.BotID)
				if botID == "" {
					return sdk.ToolOutput{}, errors.New("bot_id is required")
				}
				items, err := p.service.List(ctx.Context, botID)
				if err != nil {
					return sdk.ToolOutput{}, err
				}
				return toolexec.OutputFromValue(map[string]any{"items": items}), nil
			}),
		},
		{
			Name: ToolGetSchedule().String(), Description: "Get a schedule by id",
			Parameters: toolexec.SchemaFor[scheduleIDArgs](),
			Execute: toolexec.Typed(func(ctx *toolexec.ToolExecContext, args scheduleIDArgs) (sdk.ToolOutput, error) {
				botID := strings.TrimSpace(sess.BotID)
				if botID == "" {
					return sdk.ToolOutput{}, errors.New("bot_id is required")
				}
				id := strings.TrimSpace(args.ID)
				if id == "" {
					return sdk.ToolOutput{}, errors.New("id is required")
				}
				item, err := p.service.Get(ctx.Context, id)
				if err != nil {
					return sdk.ToolOutput{}, err
				}
				if item.BotID != botID {
					return sdk.ToolOutput{}, errors.New("bot mismatch")
				}
				return toolexec.OutputFromValue(item), nil
			}),
		},
		{
			Name: ToolCreateSchedule().String(), Description: "Create a new cron-scheduled task. Fill `command` with a natural-language instruction; when the cron `pattern` fires, the task runs and you receive a message containing that `command`. Include explicit platform and target in delivery instructions when results should be sent to a person or channel. Set `max_calls` to null for unlimited runs. " +
				"By default each fire runs in a fresh session with the bot's default model. Optional execution parameters: `session_id` runs every fire inside that existing session (its runtime and workdir are inherited; only model/effort overrides apply). For fresh sessions, `acp_agent_id` (from list_acp_agents) runs fires through an ACP agent — combine with `acp_model_id`; `model_id` (a model_uuid from list_models) picks a native model instead; `workdir_id` (from list_workdirs) pins the session's working directory. `reasoning_effort` overrides the effort in both modes.",
			Parameters: toolexec.SchemaFor[createScheduleArgs](toolexec.Replace("max_calls", nullableIntegerSchema("Optional max calls, null means unlimited")), toolexec.Range("max_run_seconds", 300, 86400)),
			Execute: toolexec.Typed(func(ctx *toolexec.ToolExecContext, args createScheduleArgs) (sdk.ToolOutput, error) {
				botID := strings.TrimSpace(sess.BotID)
				if botID == "" {
					return sdk.ToolOutput{}, errors.New("bot_id is required")
				}
				name := strings.TrimSpace(args.Name)
				description := strings.TrimSpace(args.Description)
				pattern := strings.TrimSpace(args.Pattern)
				command := strings.TrimSpace(args.Command)
				if name == "" || description == "" || pattern == "" || command == "" {
					return sdk.ToolOutput{}, errors.New("name, description, pattern, command are required")
				}
				req := sched.CreateRequest{Name: name, Description: description, Pattern: pattern, Command: command}
				req.ExecutionConfig = args.executionConfig()
				req.MaxCalls = args.MaxCalls.nullableInt()
				if args.Enabled != nil {
					req.Enabled = args.Enabled
				}
				item, err := p.service.Create(ctx.Context, botID, req)
				if err != nil {
					return sdk.ToolOutput{}, err
				}
				return toolexec.OutputFromValue(item), nil
			}),
		},
		{
			Name: ToolUpdateSchedule().String(), Description: "Update an existing schedule. To change execution parameters (session_id / model_id / acp_agent_id / acp_model_id / reasoning_effort / workdir_id), set `update_execution` to true and pass the FULL desired execution state — the whole block is replaced as one unit, and omitted execution fields reset to their defaults.",
			Parameters: toolexec.SchemaFor[updateScheduleArgs](toolexec.Replace("max_calls", nullableIntegerSchema("")), toolexec.Range("max_run_seconds", 300, 86400)),
			Execute: toolexec.Typed(func(ctx *toolexec.ToolExecContext, args updateScheduleArgs) (sdk.ToolOutput, error) {
				botID := strings.TrimSpace(sess.BotID)
				if botID == "" {
					return sdk.ToolOutput{}, errors.New("bot_id is required")
				}
				id := strings.TrimSpace(args.ID)
				if id == "" {
					return sdk.ToolOutput{}, errors.New("id is required")
				}
				// Ownership check before any write: Update itself has no bot
				// scope, so the read guards it.
				existing, err := p.service.Get(ctx.Context, id)
				if err != nil {
					return sdk.ToolOutput{}, err
				}
				if existing.BotID != botID {
					return sdk.ToolOutput{}, errors.New("bot mismatch")
				}
				req := sched.UpdateRequest{}
				req.MaxCalls = args.MaxCalls.nullableInt()
				if v := strings.TrimSpace(args.Name); v != "" {
					req.Name = &v
				}
				if v := strings.TrimSpace(args.Description); v != "" {
					req.Description = &v
				}
				if v := strings.TrimSpace(args.Pattern); v != "" {
					req.Pattern = &v
				}
				if v := strings.TrimSpace(args.Command); v != "" {
					req.Command = &v
				}
				if args.Enabled != nil {
					req.Enabled = args.Enabled
				}
				if args.UpdateExecution {
					exec := args.executionConfig()
					req.Execution = &exec
				}
				item, err := p.service.Update(ctx.Context, id, req)
				if err != nil {
					return sdk.ToolOutput{}, err
				}
				return toolexec.OutputFromValue(item), nil
			}),
		},
		{
			Name: ToolDeleteSchedule().String(), Description: "Delete a schedule by id",
			Parameters: toolexec.SchemaFor[deleteScheduleArgs](),
			Execute: toolexec.Typed(func(ctx *toolexec.ToolExecContext, args deleteScheduleArgs) (sdk.ToolOutput, error) {
				botID := strings.TrimSpace(sess.BotID)
				if botID == "" {
					return sdk.ToolOutput{}, errors.New("bot_id is required")
				}
				id := strings.TrimSpace(args.ID)
				if id == "" {
					return sdk.ToolOutput{}, errors.New("id is required")
				}
				item, err := p.service.Get(ctx.Context, id)
				if err != nil {
					return sdk.ToolOutput{}, err
				}
				if item.BotID != botID {
					return sdk.ToolOutput{}, errors.New("bot mismatch")
				}
				if err := p.service.Delete(ctx.Context, id); err != nil {
					return sdk.ToolOutput{}, err
				}
				return toolexec.OutputFromValue(map[string]any{"success": true}), nil
			}),
		},
	}, nil
}

// executionConfigFromArgs maps the flat tool arguments onto the schedule
// execution block. run_target is derived: a session_id selects
// existing_session mode, an acp_agent_id selects the ACP runtime; the
// schedule service validates the combination.
// scheduleExecutionConfig builds the execution block create and update share;
// their argument structs spell their own descriptions and hand the values here.
func scheduleExecutionConfig(maxRunSeconds int, sessionID, acpAgentID, modelID, acpModelID, reasoningEffort, workdirID string) sched.ExecutionConfig {
	exec := sched.ExecutionConfig{
		MaxRunSeconds:   maxRunSeconds,
		TargetSessionID: strings.TrimSpace(sessionID),
		ACPAgentID:      strings.TrimSpace(acpAgentID),
		ModelID:         strings.TrimSpace(modelID),
		ACPModelID:      strings.TrimSpace(acpModelID),
		ReasoningEffort: strings.TrimSpace(reasoningEffort),
		WorkdirID:       strings.TrimSpace(workdirID),
	}
	if exec.TargetSessionID != "" {
		exec.RunTarget = sched.RunTargetExistingSession
	}
	if exec.ACPAgentID != "" {
		exec.RuntimeType = sched.RuntimeACPAgent
	}
	return exec
}

func (a createScheduleArgs) executionConfig() sched.ExecutionConfig {
	return scheduleExecutionConfig(a.MaxRunSeconds, a.SessionID, a.ACPAgentID, a.ModelID, a.ACPModelID, a.ReasoningEffort, a.WorkdirID)
}

func (a updateScheduleArgs) executionConfig() sched.ExecutionConfig {
	return scheduleExecutionConfig(a.MaxRunSeconds, a.SessionID, a.ACPAgentID, a.ModelID, a.ACPModelID, a.ReasoningEffort, a.WorkdirID)
}

// scheduleMaxCalls keeps the three states of max_calls apart: omitted (leave
// the limit alone), null (unlimited), or a number. A plain *int cannot tell
// the first two apart, which is what sched.NullableInt.Set records.
type scheduleMaxCalls struct {
	set   bool
	value *int
}

func (m *scheduleMaxCalls) UnmarshalJSON(data []byte) error {
	m.set = true
	if string(data) == "null" {
		m.value = nil
		return nil
	}
	var value int
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("max_calls must be an integer or null: %w", err)
	}
	m.value = &value
	return nil
}

func (m scheduleMaxCalls) nullableInt() sched.NullableInt {
	return sched.NullableInt{Set: m.set, Value: m.value}
}

// nullableIntegerSchema is the wire shape max_calls has always had: an
// integer-or-null union the struct type above cannot express on its own.
func nullableIntegerSchema(description string) *jsonschema.Schema {
	return &jsonschema.Schema{
		Description: description,
		AnyOf:       []*jsonschema.Schema{{Type: "integer"}, {Type: "null"}},
	}
}

func emptyObjectSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

type listScheduleArgs struct{}

type scheduleIDArgs struct {
	ID string `json:"id" jsonschema:"Schedule ID"`
}

type createScheduleArgs struct {
	ACPAgentID      string           `json:"acp_agent_id,omitempty" jsonschema:"Run fires through this ACP agent (id from list_acp_agents). Only for fresh sessions."`
	ACPModelID      string           `json:"acp_model_id,omitempty" jsonschema:"ACP agent model id (from list_acp_agents with agent_id). Requires acp_agent_id or an ACP session_id."`
	Command         string           `json:"command"`
	Description     string           `json:"description"`
	Enabled         *bool            `json:"enabled,omitempty"`
	MaxCalls        scheduleMaxCalls `json:"max_calls,omitempty" jsonschema:"Optional max calls, null means unlimited"`
	MaxRunSeconds   int              `json:"max_run_seconds,omitempty" jsonschema:"Per-fire execution budget, default 3600 seconds. Overlapping fires are skipped."`
	ModelID         string           `json:"model_id,omitempty" jsonschema:"Native model override: a model_uuid from list_models. Not valid together with acp_agent_id/acp_model_id."`
	Name            string           `json:"name"`
	Pattern         string           `json:"pattern"`
	ReasoningEffort string           `json:"reasoning_effort,omitempty" jsonschema:"Reasoning effort override (native: none|minimal|low|medium|high|xhigh|max as supported by the model; ACP: the agent's effort ids)."`
	SessionID       string           `json:"session_id,omitempty" jsonschema:"Run every fire in this existing session instead of a fresh one. The session's runtime and workdir are inherited."`
	WorkdirID       string           `json:"workdir_id,omitempty" jsonschema:"Bind fresh sessions to this workdir (id from list_workdirs)."`
}

type updateScheduleArgs struct {
	ACPAgentID      string           `json:"acp_agent_id,omitempty" jsonschema:"Run fires through this ACP agent (id from list_acp_agents). Only for fresh sessions."`
	ACPModelID      string           `json:"acp_model_id,omitempty" jsonschema:"ACP agent model id (from list_acp_agents with agent_id)."`
	Command         string           `json:"command,omitempty"`
	Description     string           `json:"description,omitempty"`
	Enabled         *bool            `json:"enabled,omitempty"`
	ID              string           `json:"id"`
	MaxCalls        scheduleMaxCalls `json:"max_calls,omitempty"`
	MaxRunSeconds   int              `json:"max_run_seconds,omitempty" jsonschema:"Per-fire execution budget, default 3600 seconds. Overlapping fires are skipped."`
	ModelID         string           `json:"model_id,omitempty" jsonschema:"Native model override: a model_uuid from list_models."`
	Name            string           `json:"name,omitempty"`
	Pattern         string           `json:"pattern,omitempty"`
	ReasoningEffort string           `json:"reasoning_effort,omitempty" jsonschema:"Reasoning effort override."`
	SessionID       string           `json:"session_id,omitempty" jsonschema:"Run every fire in this existing session. The session's runtime and workdir are inherited."`
	UpdateExecution bool             `json:"update_execution,omitempty" jsonschema:"Set true to replace the execution parameter block with the values below."`
	WorkdirID       string           `json:"workdir_id,omitempty" jsonschema:"Bind fresh sessions to this workdir (id from list_workdirs)."`
}

type deleteScheduleArgs struct {
	ID string `json:"id" jsonschema:"Schedule ID"`
}
