package hooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultConfigPath = "/data/.memoh/hooks.json"
	DefaultWorkDir    = "/data"

	EventPreToolUse               = "PreToolUse"
	EventPostToolUse              = "PostToolUse"
	EventToolError                = "ToolError"
	EventSessionStart             = "SessionStart"
	EventUserMessageReceived      = "UserMessageReceived"
	EventBeforePromptBuild        = "BeforePromptBuild"
	EventAfterPromptBuild         = "AfterPromptBuild"
	EventBeforeModelCall          = "BeforeModelCall"
	EventAfterModelCall           = "AfterModelCall"
	EventTurnEnd                  = "TurnEnd"
	EventTurnError                = "TurnError"
	EventBeforeMemorySearch       = "BeforeMemorySearch"
	EventAfterMemorySearch        = "AfterMemorySearch"
	EventBeforeMemoryWrite        = "BeforeMemoryWrite"
	EventAfterMemoryWrite         = "AfterMemoryWrite"
	EventMemoryExtracted          = "MemoryExtracted"
	EventWorkspaceStart           = "WorkspaceStart"
	EventWorkspaceStop            = "WorkspaceStop"
	EventBeforeWorkspaceCommand   = "BeforeWorkspaceCommand"
	EventAfterWorkspaceCommand    = "AfterWorkspaceCommand"
	EventBeforeFileWrite          = "BeforeFileWrite"
	EventAfterFileWrite           = "AfterFileWrite"
	EventBeforeApprovalCreate     = "BeforeApprovalCreate"
	EventApprovalRequested        = "ApprovalRequested"
	EventApprovalResolved         = "ApprovalResolved"
	EventApprovalTimeout          = "ApprovalTimeout"
	EventInboundMessageNormalized = "InboundMessageNormalized"
	EventBeforeOutboundMessage    = "BeforeOutboundMessage"
	EventAfterOutboundMessage     = "AfterOutboundMessage"
	EventChannelDeliveryFailed    = "ChannelDeliveryFailed"
	EventPreCompact               = "PreCompact"
	EventPostCompact              = "PostCompact"
	EventSubagentStart            = "SubagentStart"
	EventSubagentStop             = "SubagentStop"
)

const (
	ActionCommand = "command"
	ActionTool    = "tool"
	ActionMCPTool = "mcp_tool"
)

const (
	DecisionAllow         = "allow"
	DecisionDeny          = "deny"
	DecisionAskApproval   = "ask_approval"
	DecisionAppendContext = "append_context"
)

const (
	OnErrorIgnore = "ignore"
	OnErrorFail   = "fail"
	OnErrorBlock  = "block"
)

var (
	ErrDenied      = errors.New("hook denied action")
	ErrUnsupported = errors.New("unsupported hook action")
)

var supportedEvents = map[string]struct{}{
	EventPreToolUse: {}, EventPostToolUse: {}, EventToolError: {},
	EventSessionStart: {}, EventUserMessageReceived: {}, EventBeforePromptBuild: {}, EventAfterPromptBuild: {},
	EventBeforeModelCall: {}, EventAfterModelCall: {}, EventTurnEnd: {}, EventTurnError: {},
	EventBeforeMemorySearch: {}, EventAfterMemorySearch: {}, EventBeforeMemoryWrite: {}, EventAfterMemoryWrite: {}, EventMemoryExtracted: {},
	EventWorkspaceStart: {}, EventWorkspaceStop: {}, EventBeforeWorkspaceCommand: {}, EventAfterWorkspaceCommand: {}, EventBeforeFileWrite: {}, EventAfterFileWrite: {},
	EventBeforeApprovalCreate: {}, EventApprovalRequested: {}, EventApprovalResolved: {}, EventApprovalTimeout: {},
	EventInboundMessageNormalized: {}, EventBeforeOutboundMessage: {}, EventAfterOutboundMessage: {}, EventChannelDeliveryFailed: {},
	EventPreCompact: {}, EventPostCompact: {}, EventSubagentStart: {}, EventSubagentStop: {},
}

var runtimeEvents = map[string]struct{}{
	EventPreToolUse: {}, EventPostToolUse: {}, EventToolError: {},
	EventSessionStart: {}, EventUserMessageReceived: {}, EventBeforePromptBuild: {}, EventAfterPromptBuild: {},
	EventBeforeModelCall: {}, EventAfterModelCall: {}, EventTurnEnd: {}, EventTurnError: {},
	EventBeforeMemorySearch: {}, EventAfterMemorySearch: {}, EventBeforeMemoryWrite: {}, EventAfterMemoryWrite: {}, EventMemoryExtracted: {},
	EventWorkspaceStart: {}, EventWorkspaceStop: {}, EventBeforeWorkspaceCommand: {}, EventAfterWorkspaceCommand: {}, EventBeforeFileWrite: {}, EventAfterFileWrite: {},
	EventBeforeApprovalCreate: {}, EventApprovalRequested: {}, EventApprovalResolved: {}, EventApprovalTimeout: {},
	EventPreCompact: {}, EventPostCompact: {}, EventSubagentStart: {}, EventSubagentStop: {},
}

type Config struct {
	Version  int               `json:"version"`
	Enabled  *bool             `json:"enabled,omitempty"`
	Defaults Defaults          `json:"defaults,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Hooks    []Hook            `json:"hooks,omitempty"`
}

type Defaults struct {
	Timeout            string `json:"timeout,omitempty"`
	OnError            string `json:"on_error,omitempty"`
	MaxOutputBytes     int    `json:"max_output_bytes,omitempty"`
	TriggerNestedHooks *bool  `json:"trigger_nested_hooks,omitempty"`
}

type Hook struct {
	Name       string       `json:"name,omitempty"`
	Event      string       `json:"event"`
	Matcher    string       `json:"matcher,omitempty"`
	Enabled    *bool        `json:"enabled,omitempty"`
	Priority   int          `json:"priority,omitempty"`
	Conditions []Condition  `json:"conditions,omitempty"`
	Actions    []HookAction `json:"actions,omitempty"`

	matcher *regexp.Regexp
	source  hookSource
}

type hookSource struct {
	Kind           string
	PluginID       string
	PluginDir      string
	Env            map[string]string
	MaxOutputBytes int
}

type Condition struct {
	Expr string `json:"expr,omitempty"`
}

type HookAction struct {
	Type               string         `json:"type"`
	Command            string         `json:"command,omitempty"`
	Tool               string         `json:"tool,omitempty"`
	Server             string         `json:"server,omitempty"`
	Input              map[string]any `json:"input,omitempty"`
	Timeout            string         `json:"timeout,omitempty"`
	OnError            string         `json:"on_error,omitempty"`
	WorkDir            string         `json:"work_dir,omitempty"`
	TriggerNestedHooks *bool          `json:"trigger_nested_hooks,omitempty"`
}

type Request struct {
	Version   int            `json:"version"`
	Event     string         `json:"event"`
	HookName  string         `json:"hook_name,omitempty"`
	BotID     string         `json:"bot_id,omitempty"`
	SessionID string         `json:"session_id,omitempty"`
	ChatID    string         `json:"chat_id,omitempty"`
	Workspace WorkspaceInfo  `json:"workspace,omitempty"`
	Tool      *ToolPayload   `json:"tool,omitempty"`
	Approval  map[string]any `json:"approval,omitempty"`
	Turn      map[string]any `json:"turn,omitempty"`
	Memory    map[string]any `json:"memory,omitempty"`
	Channel   map[string]any `json:"channel,omitempty"`
	Extra     map[string]any `json:"extra,omitempty"`
	Error     string         `json:"error,omitempty"`
}

type WorkspaceInfo struct {
	CWD     string `json:"cwd,omitempty"`
	Runtime string `json:"runtime,omitempty"`
}

type ToolPayload struct {
	Name   string `json:"name,omitempty"`
	CallID string `json:"call_id,omitempty"`
	Input  any    `json:"input,omitempty"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

type ActionResult struct {
	ActionType string         `json:"action_type,omitempty"`
	Name       string         `json:"name,omitempty"`
	Decision   string         `json:"decision,omitempty"`
	Reason     string         `json:"reason,omitempty"`
	Stdout     string         `json:"stdout,omitempty"`
	Stderr     string         `json:"stderr,omitempty"`
	ExitCode   int32          `json:"exit_code,omitempty"`
	Result     any            `json:"result,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	Error      string         `json:"error,omitempty"`

	appendContextRaw   string
	appendContextLimit int
}

type Result struct {
	Decision         string         `json:"decision,omitempty"`
	Reason           string         `json:"reason,omitempty"`
	AppendContext    string         `json:"append_context,omitempty"`
	HooksMatched     int            `json:"hooks_matched"`
	ActionsRun       int            `json:"actions_run"`
	RuntimeSupported bool           `json:"runtime_supported"`
	ActionResults    []ActionResult `json:"action_results,omitempty"`
	Metadata         map[string]any `json:"metadata,omitempty"`

	appendContextRaw   string
	appendContextLimit int
}

func ParseConfig(data []byte) (Config, error) {
	var cfg Config
	if len(strings.TrimSpace(string(data))) == 0 {
		return Config{Version: 1, Enabled: boolPtr(false)}, nil
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	if cfg.Version == 0 {
		cfg.Version = 1
	}
	if cfg.Version != 1 {
		return Config{}, fmt.Errorf("unsupported hooks config version %d", cfg.Version)
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Enabled == nil {
		c.Enabled = boolPtr(true)
	}
	if c.Defaults.Timeout == "" {
		c.Defaults.Timeout = "10s"
	}
	if c.Defaults.OnError == "" {
		c.Defaults.OnError = OnErrorFail
	}
	if c.Defaults.MaxOutputBytes <= 0 {
		c.Defaults.MaxOutputBytes = 64 * 1024
	}
	if c.Defaults.TriggerNestedHooks == nil {
		c.Defaults.TriggerNestedHooks = boolPtr(false)
	}
	for i := range c.Hooks {
		if c.Hooks[i].Enabled == nil {
			c.Hooks[i].Enabled = boolPtr(true)
		}
		for j := range c.Hooks[i].Actions {
			if c.Hooks[i].Actions[j].Timeout == "" {
				c.Hooks[i].Actions[j].Timeout = c.Defaults.Timeout
			}
			if c.Hooks[i].Actions[j].OnError == "" {
				c.Hooks[i].Actions[j].OnError = c.Defaults.OnError
			}
			if c.Hooks[i].Actions[j].TriggerNestedHooks == nil {
				c.Hooks[i].Actions[j].TriggerNestedHooks = c.Defaults.TriggerNestedHooks
			}
		}
	}
}

func (c *Config) validate() error {
	for i := range c.Hooks {
		h := &c.Hooks[i]
		if _, ok := supportedEvents[h.Event]; !ok {
			return fmt.Errorf("hook %q uses unsupported event %q", h.Name, h.Event)
		}
		if strings.TrimSpace(h.Matcher) != "" {
			re, err := regexp.Compile(h.Matcher)
			if err != nil {
				return fmt.Errorf("hook %q matcher: %w", h.Name, err)
			}
			h.matcher = re
		}
		for _, action := range h.Actions {
			switch strings.TrimSpace(action.Type) {
			case ActionCommand:
				if strings.TrimSpace(action.Command) == "" {
					return fmt.Errorf("hook %q has command action without command", h.Name)
				}
			case ActionTool:
				if strings.TrimSpace(action.Tool) == "" {
					return fmt.Errorf("hook %q has tool action without tool", h.Name)
				}
			case ActionMCPTool:
				return fmt.Errorf("%w: mcp_tool is reserved for a later version", ErrUnsupported)
			default:
				return fmt.Errorf("hook %q has unsupported action type %q", h.Name, action.Type)
			}
			if _, err := parseTimeout(action.Timeout); err != nil {
				return fmt.Errorf("hook %q action timeout: %w", h.Name, err)
			}
			switch normalizeOnError(action.OnError) {
			case OnErrorIgnore, OnErrorFail, OnErrorBlock:
			default:
				return fmt.Errorf("hook %q action on_error must be ignore, fail, or block", h.Name)
			}
		}
	}
	return nil
}

func (c Config) enabled() bool {
	return c.Enabled == nil || *c.Enabled
}

func (c Config) Match(req Request) []Hook {
	if !c.enabled() {
		return nil
	}
	target := req.matchText()
	var out []Hook
	for _, hook := range c.Hooks {
		if hook.Enabled != nil && !*hook.Enabled {
			continue
		}
		if hook.Event != req.Event {
			continue
		}
		if hook.matcher != nil && !hook.matcher.MatchString(target) {
			continue
		}
		out = append(out, hook)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Priority > out[j].Priority
	})
	return out
}

func (r Request) matchText() string {
	switch {
	case r.Tool != nil && strings.TrimSpace(r.Tool.Name) != "":
		return strings.TrimSpace(r.Tool.Name)
	case r.Approval != nil:
		if v, _ := r.Approval["tool_name"].(string); strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	case r.Channel != nil:
		if v, _ := r.Channel["platform"].(string); strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	case r.Memory != nil:
		if v, _ := r.Memory["scope"].(string); strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	case r.Extra != nil:
		for _, key := range []string{"command", "path", "operation", "scope"} {
			if v, _ := r.Extra[key].(string); strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
	}
	return r.Event
}

func EventCatalog() []string {
	out := make([]string, 0, len(supportedEvents))
	for event := range supportedEvents {
		out = append(out, event)
	}
	sort.Strings(out)
	return out
}

func RuntimeSupported(event string) bool {
	_, ok := runtimeEvents[event]
	return ok
}

func parseTimeout(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 10 * time.Second, nil
	}
	if d, err := time.ParseDuration(raw); err == nil {
		return d, nil
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil {
		return 0, err
	}
	if seconds <= 0 {
		return 0, errors.New("timeout must be positive")
	}
	return time.Duration(seconds) * time.Second, nil
}

func normalizeOnError(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case OnErrorIgnore:
		return OnErrorIgnore
	case OnErrorBlock:
		return OnErrorBlock
	default:
		return OnErrorFail
	}
}

func boolPtr(v bool) *bool {
	return &v
}
