package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/prune"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

type ToolRunner interface {
	RunHookTool(ctx context.Context, toolName string, input map[string]any) (any, error)
}

type workspaceTargetIDResolver interface {
	CurrentWorkspaceTargetID(ctx context.Context, botID string) (string, error)
}

type Service struct {
	logger   *slog.Logger
	provider bridge.Provider
}

var emptyConfigFile = []byte("{\n  \"version\": 1,\n  \"enabled\": true,\n  \"hooks\": []\n}\n")

const (
	sourceKindUser = "user"

	maxAppendSystemSections = 16
)

func NewService(log *slog.Logger, provider bridge.Provider) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		logger:   log.With(slog.String("service", "hooks")),
		provider: provider,
	}
}

func (s *Service) Load(ctx context.Context, botID string) (Config, bool, error) {
	if s == nil || s.provider == nil {
		return Config{Version: 1, Enabled: boolPtr(false)}, false, nil
	}
	client, err := s.provider.MCPClient(ctx, strings.TrimSpace(botID))
	if err != nil {
		return Config{}, false, err
	}
	rc, err := client.ReadRaw(ctx, DefaultConfigPath)
	if err != nil {
		if errors.Is(err, bridge.ErrNotFound) {
			if err := client.WriteFile(ctx, DefaultConfigPath, emptyConfigFile); err != nil {
				return Config{}, false, err
			}
			cfg, err := ParseConfig(emptyConfigFile)
			if err != nil {
				return Config{}, false, err
			}
			return cfg, true, nil
		}
		return Config{}, false, err
	}
	defer func() { _ = rc.Close() }()
	raw, err := io.ReadAll(rc)
	if err != nil {
		return Config{}, false, err
	}
	cfg, err := ParseConfig(raw)
	if err != nil {
		return Config{}, true, err
	}
	return cfg, true, nil
}

func (s *Service) LoadEffective(ctx context.Context, botID string) (Config, bool, error) {
	targetID, err := s.currentWorkspaceTargetID(ctx, botID)
	if err != nil {
		return Config{}, false, err
	}
	ctx = bridge.WithWorkspaceTarget(ctx, targetID)
	userCfg, exists, err := s.Load(ctx, botID)
	if err != nil {
		return Config{}, exists, err
	}
	userCfg.applyDefaults()

	effective := Config{
		Version:  1,
		Enabled:  boolPtr(true),
		Defaults: userCfg.Defaults,
		Env:      cloneStringMap(userCfg.Env),
	}
	if userCfg.enabled() {
		for _, hook := range userCfg.Hooks {
			hook.source = hookSource{Kind: sourceKindUser}
			effective.Hooks = append(effective.Hooks, hook)
		}
	}
	return effective, exists, nil
}

func (s *Service) currentWorkspaceTargetID(ctx context.Context, botID string) (string, error) {
	if targetID := bridge.WorkspaceTargetFromContext(ctx); targetID != "" {
		return targetID, nil
	}
	if s == nil {
		return "native", nil
	}
	if resolver, ok := s.provider.(workspaceTargetIDResolver); ok {
		return resolver.CurrentWorkspaceTargetID(ctx, botID)
	}
	return "native", nil
}

func (s *Service) Run(ctx context.Context, req Request, runner ToolRunner) (Result, error) {
	req.Version = 1
	if strings.TrimSpace(req.Event) == "" {
		return Result{}, errors.New("hook event is required")
	}
	targetID, err := s.currentWorkspaceTargetID(ctx, req.BotID)
	if err != nil {
		return Result{}, err
	}
	ctx = bridge.WithWorkspaceTarget(ctx, targetID)
	cfg, _, err := s.LoadEffective(ctx, req.BotID)
	if err != nil {
		return Result{}, err
	}
	return s.RunConfig(ctx, cfg, req, runner)
}

func (s *Service) RunConfig(ctx context.Context, cfg Config, req Request, runner ToolRunner) (Result, error) {
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return Result{}, err
	}
	result := Result{
		Decision:         DecisionAllow,
		RuntimeSupported: RuntimeSupported(req.Event),
	}
	matches := cfg.Match(req)
	result.HooksMatched = len(matches)
	if len(matches) == 0 {
		return result, nil
	}
	result.Metadata = map[string]any{"hook_sources": hookSourceSummaries(matches)}
	for hookOrder, hook := range matches {
		hookReq := req
		hookReq.Version = 1
		hookReq.HookName = hook.Name
		for _, action := range hook.Actions {
			actionResult, err := s.runAction(ctx, cfg, hookReq, action, hook.source, runner)
			annotateActionResult(&actionResult, hook, hookOrder)
			result.ActionResults = append(result.ActionResults, actionResult)
			result.ActionsRun++
			if err != nil {
				onError := normalizeOnError(action.OnError)
				if onError == OnErrorIgnore {
					if s != nil && s.logger != nil {
						s.logger.Warn("hook action failed but was ignored",
							slog.String("event", req.Event),
							slog.String("hook", hook.Name),
							slog.String("action", action.Type),
							slog.String("error", err.Error()),
						)
					}
					continue
				}
				if onError == OnErrorBlock {
					result.Decision = DecisionDeny
					result.Reason = firstNonEmpty(actionResult.Reason, err.Error())
					return result, fmt.Errorf("%w: %s", ErrDenied, result.Reason)
				}
				return result, err
			}
			mergeDecision(&result, actionResult, cfg.Defaults.MaxOutputBytes)
			if result.Decision == DecisionDeny {
				return result, fmt.Errorf("%w: %s", ErrDenied, result.Reason)
			}
		}
	}
	sortAppendSystemSections(result.AppendSystemSections)
	return result, nil
}

func (s *Service) runAction(ctx context.Context, cfg Config, req Request, action HookAction, _ hookSource, runner ToolRunner) (ActionResult, error) {
	switch strings.TrimSpace(action.Type) {
	case ActionCommand:
		return s.runCommand(ctx, cfg, req, action)
	case ActionTool:
		return s.runTool(ctx, cfg, req, action, runner)
	case ActionMCPTool:
		return ActionResult{ActionType: action.Type, Error: ErrUnsupported.Error()}, ErrUnsupported
	default:
		err := fmt.Errorf("unsupported hook action type %q", action.Type)
		return ActionResult{ActionType: action.Type, Error: err.Error()}, err
	}
}

func (s *Service) runCommand(ctx context.Context, cfg Config, req Request, action HookAction) (ActionResult, error) {
	res := ActionResult{ActionType: ActionCommand, Name: action.Command}
	if s == nil || s.provider == nil {
		err := errors.New("hooks workspace provider is not configured")
		res.Error = err.Error()
		return res, err
	}
	timeout, err := parseTimeout(action.Timeout)
	if err != nil {
		res.Error = err.Error()
		return res, err
	}
	client, err := s.provider.MCPClient(ctx, req.BotID)
	if err != nil {
		res.Error = err.Error()
		return res, err
	}
	payload, err := json.Marshal(req)
	if err != nil {
		res.Error = err.Error()
		return res, err
	}
	workDir := strings.TrimSpace(action.WorkDir)
	if workDir == "" {
		// Hook commands and their config belong to the provider's primary
		// workspace. req.Workspace describes the operation being inspected and
		// may point at a different runtime.
		workDir = DefaultWorkDir
	}
	envMap := cfg.Env
	env := make([]string, 0, len(envMap)+6)
	for key, value := range envMap {
		if strings.TrimSpace(key) == "" {
			continue
		}
		env = append(env, key+"="+value)
	}
	env = append(env,
		"MEMOH_HOOK_EVENT="+req.Event,
		"MEMOH_HOOK_NAME="+req.HookName,
		"MEMOH_BOT_ID="+req.BotID,
		"MEMOH_SESSION_ID="+req.SessionID,
	)
	timeoutUnits := timeout.Round(time.Second) / time.Second
	if timeoutUnits <= 0 {
		timeoutUnits = 1
	}
	if timeoutUnits > math.MaxInt32 {
		timeoutUnits = math.MaxInt32
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout+time.Second)
	defer cancel()
	execResult, err := client.ExecWithStdinEnv(execCtx, action.Command, workDir, int32(timeoutUnits), append(payload, '\n'), env)
	maxOutputBytes := hookMaxOutputBytes(cfg)
	if execResult != nil {
		res.Stdout = limitHookOutputText(execResult.Stdout, maxOutputBytes)
		res.Stderr = limitHookOutputText(execResult.Stderr, maxOutputBytes)
		res.ExitCode = execResult.ExitCode
	}
	if err != nil {
		res.Error = err.Error()
		return res, err
	}
	if execResult != nil && execResult.ExitCode != 0 {
		err := fmt.Errorf("hook command exited with code %d", execResult.ExitCode)
		res.Error = err.Error()
		return res, err
	}
	if execResult != nil {
		applyActionOutput(&res, execResult.Stdout, maxOutputBytes, cfg.Defaults.MaxOutputBytes)
	} else {
		applyActionOutput(&res, "", maxOutputBytes, cfg.Defaults.MaxOutputBytes)
	}
	return res, nil
}

func (*Service) runTool(ctx context.Context, cfg Config, _ Request, action HookAction, runner ToolRunner) (ActionResult, error) {
	res := ActionResult{ActionType: ActionTool, Name: action.Tool}
	if runner == nil {
		err := errors.New("hook tool runner is not configured")
		res.Error = err.Error()
		return res, err
	}
	timeout, err := parseTimeout(action.Timeout)
	if err != nil {
		res.Error = err.Error()
		return res, err
	}
	input := action.Input
	if input == nil {
		input = map[string]any{}
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, err := runner.RunHookTool(runCtx, action.Tool, input)
	res.Result = output
	if err != nil {
		res.Error = err.Error()
		return res, err
	}
	applyToolOutput(&res, output, hookMaxOutputBytes(cfg), cfg.Defaults.MaxOutputBytes)
	return res, nil
}

func applyActionOutput(
	result *ActionResult,
	stdout string,
	maxOutputBytes int,
	appendSystemSectionLimits ...int,
) {
	result.appendContextLimit = maxOutputBytes
	appendSystemSectionLimit := resolveAppendSystemSectionLimit(maxOutputBytes, appendSystemSectionLimits)
	result.appendSystemLimit = appendSystemSectionLimit
	raw := strings.TrimSpace(stdout)
	if raw == "" {
		result.Decision = DecisionAllow
		return
	}
	var output struct {
		Decision            string          `json:"decision"`
		Reason              string          `json:"reason"`
		AppendContext       string          `json:"append_context"`
		AppendSystemSection json.RawMessage `json:"append_system_section"`
		Metadata            map[string]any  `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(raw), &output); err != nil {
		result.Decision = DecisionAllow
		result.Metadata = map[string]any{"raw_stdout": limitHookOutputText(raw, maxOutputBytes)}
		return
	}
	result.Decision = normalizeDecision(output.Decision)
	result.Reason = limitHookOutputText(output.Reason, maxOutputBytes)
	if output.Metadata != nil {
		if result.Metadata == nil {
			result.Metadata = map[string]any{}
		}
		for k, v := range output.Metadata {
			if k == "append_context" {
				if text, ok := v.(string); ok {
					result.appendContextRaw = text
					v = limitHookOutputText(text, maxOutputBytes)
				}
			}
			result.Metadata[k] = v
		}
	}
	if output.AppendContext != "" {
		if result.Metadata == nil {
			result.Metadata = map[string]any{}
		}
		result.appendContextRaw = output.AppendContext
		result.Metadata["append_context"] = limitHookOutputText(output.AppendContext, maxOutputBytes)
	}
	if len(output.AppendSystemSection) > 0 {
		sections, warnings := parseAppendSystemSections(output.AppendSystemSection, appendSystemSectionLimit)
		result.AppendSystemSections = append(result.AppendSystemSections, sections...)
		result.Warnings = append(result.Warnings, warnings...)
	}
}

func applyToolOutput(
	result *ActionResult,
	output any,
	maxOutputBytes int,
	appendSystemSectionLimits ...int,
) {
	result.appendContextLimit = maxOutputBytes
	appendSystemSectionLimit := resolveAppendSystemSectionLimit(maxOutputBytes, appendSystemSectionLimits)
	result.appendSystemLimit = appendSystemSectionLimit
	m, ok := output.(map[string]any)
	if !ok {
		raw, err := json.Marshal(output)
		if err == nil {
			_ = json.Unmarshal(raw, &m)
		}
	}
	if m == nil {
		result.Decision = DecisionAllow
		return
	}
	if decision, _ := m["decision"].(string); decision != "" {
		result.Decision = normalizeDecision(decision)
	}
	if reason, _ := m["reason"].(string); reason != "" {
		result.Reason = limitHookOutputText(reason, maxOutputBytes)
	}
	if appendContext, _ := m["append_context"].(string); appendContext != "" {
		result.appendContextRaw = appendContext
		result.Metadata = map[string]any{"append_context": limitHookOutputText(appendContext, maxOutputBytes)}
	}
	if value, ok := m["append_system_section"]; ok {
		sections, warnings := parseAppendSystemSections(value, appendSystemSectionLimit)
		result.AppendSystemSections = append(result.AppendSystemSections, sections...)
		result.Warnings = append(result.Warnings, warnings...)
	}
}

func mergeDecision(result *Result, actionResult ActionResult, maxOutputBytes int) {
	decision := normalizeDecision(actionResult.Decision)
	if decision == "" {
		decision = DecisionAllow
	}
	appendContext := actionResult.appendContextRaw
	if actionResult.Metadata != nil {
		if strings.TrimSpace(appendContext) == "" {
			appendContext, _ = actionResult.Metadata["append_context"].(string)
		}
	}
	if strings.TrimSpace(appendContext) != "" {
		fragmentLimit := actionResult.appendContextLimit
		if fragmentLimit <= 0 {
			fragmentLimit = maxOutputBytes
		}
		result.appendContextLimit = minPositiveLimit(result.appendContextLimit, maxOutputBytes, fragmentLimit)
		if result.appendContextRaw != "" {
			result.appendContextRaw += "\n"
		}
		result.appendContextRaw += appendContext
		result.AppendContext = limitHookOutputText(result.appendContextRaw, firstPositive(result.appendContextLimit, maxOutputBytes))
	}
	sectionOffset := result.appendSystemOrder
	result.appendSystemOrder += len(actionResult.AppendSystemSections)
	for i := range actionResult.AppendSystemSections {
		actionResult.AppendSystemSections[i].sectionOrder = sectionOffset + i
	}
	for i := range actionResult.Warnings {
		if actionResult.Warnings[i].sectionOrder >= 0 {
			actionResult.Warnings[i].sectionOrder += sectionOffset
		}
	}
	result.AppendSystemSections = append(result.AppendSystemSections, actionResult.AppendSystemSections...)
	result.Warnings = append(result.Warnings, actionResult.Warnings...)
	if len(actionResult.AppendSystemSections) > 0 {
		result.appendSystemLimit = minPositiveLimit(
			result.appendSystemLimit,
			maxOutputBytes,
			actionResult.appendSystemLimit,
		)
		enforceAppendSystemSectionOutputLimit(result)
	}
	switch decision {
	case DecisionDeny:
		result.Decision = DecisionDeny
		result.Reason = firstNonEmpty(actionResult.Reason, "hook denied action")
	case DecisionAskApproval:
		if result.Decision != DecisionDeny {
			result.Decision = DecisionAskApproval
			result.Reason = firstNonEmpty(actionResult.Reason, result.Reason)
		}
	case DecisionAppendContext:
		if result.Decision == "" || result.Decision == DecisionAllow {
			result.Decision = DecisionAppendContext
		}
		result.Reason = firstNonEmpty(actionResult.Reason, result.Reason)
	case DecisionAllow:
		if result.Decision == "" {
			result.Decision = DecisionAllow
		}
	}
}

func parseAppendSystemSections(value any, maxOutputBytes int) ([]SystemSectionOutput, []OutputWarning) {
	data, err := appendSystemSectionJSON(value)
	if err != nil {
		return nil, []OutputWarning{invalidAppendSystemSectionWarning()}
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, []OutputWarning{invalidAppendSystemSectionWarning()}
	}

	var entries []json.RawMessage
	switch data[0] {
	case '{':
		entries = []json.RawMessage{data}
	case '[':
		if err := json.Unmarshal(data, &entries); err != nil {
			return nil, []OutputWarning{invalidAppendSystemSectionWarning()}
		}
	default:
		return nil, []OutputWarning{invalidAppendSystemSectionWarning()}
	}

	sections := make([]SystemSectionOutput, 0, len(entries))
	warnings := make([]OutputWarning, 0)
	remainingBytes := maxOutputBytes
	for entryIndex, entry := range entries {
		if entryIndex >= maxAppendSystemSections {
			warnings = append(warnings, appendSystemSectionOutputLimitedWarning(""))
			break
		}
		var raw struct {
			ID        string `json:"id"`
			Text      string `json:"text"`
			Retention string `json:"retention"`
			Cache     string `json:"cache"`
		}
		entry = bytes.TrimSpace(entry)
		if len(entry) == 0 || entry[0] != '{' || json.Unmarshal(entry, &raw) != nil {
			warnings = append(warnings, invalidAppendSystemSectionWarning())
			continue
		}

		rawText := strings.TrimSpace(raw.Text)
		if rawText == "" {
			warnings = append(warnings, invalidAppendSystemSectionWarning())
			continue
		}
		retention := SystemSectionRetention(strings.ToLower(strings.TrimSpace(raw.Retention)))
		clampedRequired := false
		switch retention {
		case "":
			retention = SystemSectionRetentionOptional
		case SystemSectionRetentionOptional, SystemSectionRetentionPreferred:
		case "required":
			retention = SystemSectionRetentionPreferred
			clampedRequired = true
		default:
			warnings = append(warnings, invalidAppendSystemSectionWarning())
			continue
		}
		cache := SystemSectionCache(strings.ToLower(strings.TrimSpace(raw.Cache)))
		switch cache {
		case "":
			cache = SystemSectionCacheDynamic
		case SystemSectionCacheDynamic, SystemSectionCacheStable:
		default:
			warnings = append(warnings, invalidAppendSystemSectionWarning())
			continue
		}

		textLimit := maxOutputBytes
		if maxOutputBytes > 0 {
			if remainingBytes <= 0 {
				warnings = append(warnings, appendSystemSectionOutputLimitedWarning(strings.TrimSpace(raw.ID)))
				continue
			}
			textLimit = remainingBytes
		}
		text := limitHookOutputText(rawText, textLimit)
		if text == "" {
			warnings = append(warnings, appendSystemSectionOutputLimitedWarning(strings.TrimSpace(raw.ID)))
			continue
		}
		section := SystemSectionOutput{
			ID:           strings.TrimSpace(raw.ID),
			Text:         text,
			Retention:    retention,
			Cache:        cache,
			sectionOrder: len(sections),
		}
		outputLimited := text != rawText
		if outputLimited {
			appendSystemSectionWarningCode(&section, WarningAppendSystemSectionOutputLimited)
		}
		if clampedRequired {
			appendSystemSectionWarningCode(&section, WarningSystemSectionRequiredClamped)
		}
		sections = append(sections, section)
		if maxOutputBytes > 0 {
			remainingBytes -= len(text)
		}
		if outputLimited {
			warning := appendSystemSectionOutputLimitedWarning(section.ID)
			warning.sectionOrder = section.sectionOrder
			warnings = append(warnings, warning)
		}
		if clampedRequired {
			warnings = append(warnings, OutputWarning{
				Code:         WarningSystemSectionRequiredClamped,
				Message:      "hook system section retention was clamped from required to preferred",
				SectionID:    section.ID,
				sectionOrder: section.sectionOrder,
			})
		}
	}
	return sections, warnings
}

func enforceAppendSystemSectionOutputLimit(result *Result) {
	if result == nil || result.appendSystemLimit <= 0 {
		return
	}
	remaining := result.appendSystemLimit
	kept := result.AppendSystemSections[:0]
	for _, section := range result.AppendSystemSections {
		if len(kept) >= maxAppendSystemSections || remaining <= 0 {
			appendSystemSectionOutputWarning(result, section)
			continue
		}
		text := limitHookOutputText(section.Text, remaining)
		if text == "" {
			appendSystemSectionOutputWarning(result, section)
			continue
		}
		if text != section.Text {
			section.Text = text
			appendSystemSectionWarningCode(&section, WarningAppendSystemSectionOutputLimited)
			appendSystemSectionOutputWarning(result, section)
		}
		kept = append(kept, section)
		remaining -= len(text)
	}
	result.AppendSystemSections = kept
}

func appendSystemSectionOutputWarning(result *Result, section SystemSectionOutput) {
	warning := appendSystemSectionOutputLimitedWarning(section.ID)
	warning.HookName = section.HookName
	warning.hookOrder = section.hookOrder
	warning.sectionOrder = section.sectionOrder
	for _, existing := range result.Warnings {
		if existing.Code == warning.Code &&
			existing.hookOrder == warning.hookOrder &&
			existing.sectionOrder == warning.sectionOrder {
			return
		}
	}
	result.Warnings = append(result.Warnings, warning)
}

func appendSystemSectionWarningCode(section *SystemSectionOutput, code string) {
	if section == nil || code == "" {
		return
	}
	for _, existing := range section.WarningCodes {
		if existing == code {
			return
		}
	}
	section.WarningCodes = append(section.WarningCodes, code)
}

func sortAppendSystemSections(sections []SystemSectionOutput) {
	sort.SliceStable(sections, func(i, j int) bool {
		if sections[i].hookOrder != sections[j].hookOrder {
			return sections[i].hookOrder < sections[j].hookOrder
		}
		if sections[i].ID != sections[j].ID {
			return sections[i].ID < sections[j].ID
		}
		return sections[i].sectionOrder < sections[j].sectionOrder
	})
}

func appendSystemSectionJSON(value any) ([]byte, error) {
	if raw, ok := value.(json.RawMessage); ok {
		return raw, nil
	}
	return json.Marshal(value)
}

func appendSystemSectionOutputLimitedWarning(sectionID string) OutputWarning {
	return OutputWarning{
		Code:         WarningAppendSystemSectionOutputLimited,
		Message:      "append_system_section text was limited by max_output_bytes",
		SectionID:    sectionID,
		sectionOrder: -1,
	}
}

func invalidAppendSystemSectionWarning() OutputWarning {
	return OutputWarning{
		Code:         WarningInvalidAppendSystemSection,
		Message:      "append_system_section must contain an object or array of objects with valid text and policy fields",
		sectionOrder: -1,
	}
}

func normalizeDecision(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case DecisionDeny:
		return DecisionDeny
	case DecisionAskApproval:
		return DecisionAskApproval
	case DecisionAppendContext:
		return DecisionAppendContext
	case DecisionAllow, "":
		return DecisionAllow
	default:
		return DecisionAllow
	}
}

func hookMaxOutputBytes(cfg Config) int {
	return cfg.Defaults.MaxOutputBytes
}

func resolveAppendSystemSectionLimit(actionLimit int, globalLimits []int) int {
	if len(globalLimits) == 0 {
		return actionLimit
	}
	return minPositiveLimit(actionLimit, globalLimits[0])
}

func minPositiveLimit(values ...int) int {
	limit := 0
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if limit == 0 || value < limit {
			limit = value
		}
	}
	return limit
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func limitHookOutputText(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 {
		return text
	}
	limited := prune.PruneWithEdges(text, "hook output", prune.Config{
		MaxBytes:  limit,
		MaxLines:  prune.DefaultMaxLines,
		HeadBytes: limit * 3 / 4,
		TailBytes: limit / 4,
		HeadLines: prune.DefaultMaxLines * 3 / 4,
		TailLines: prune.DefaultMaxLines / 4,
	})
	if len(limited) > limit {
		return trimOutput(limited, limit)
	}
	return limited
}

func trimOutput(raw string, limit int) string {
	if limit <= 0 || len(raw) <= limit {
		return raw
	}
	return raw[:limit]
}

func hookSourceSummaries(hooks []Hook) []map[string]any {
	out := make([]map[string]any, 0, len(hooks))
	for _, hook := range hooks {
		source := normalizeHookSource(hook.source)
		item := map[string]any{
			"hook_name":   hook.Name,
			"source_kind": source.Kind,
		}
		out = append(out, item)
	}
	return out
}

func annotateActionResult(result *ActionResult, hook Hook, hookOrder int) {
	if result == nil {
		return
	}
	if result.Metadata == nil {
		result.Metadata = map[string]any{}
	}
	source := normalizeHookSource(hook.source)
	result.Metadata["hook_name"] = hook.Name
	result.Metadata["hook_source_kind"] = source.Kind
	for i := range result.AppendSystemSections {
		result.AppendSystemSections[i].HookName = hook.Name
		result.AppendSystemSections[i].hookOrder = hookOrder
	}
	for i := range result.Warnings {
		result.Warnings[i].HookName = hook.Name
		result.Warnings[i].hookOrder = hookOrder
	}
}

func normalizeHookSource(source hookSource) hookSource {
	if strings.TrimSpace(source.Kind) == "" {
		source.Kind = sourceKindUser
	}
	return source
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
