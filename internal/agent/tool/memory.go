package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/toolexec"
	"github.com/felinics/memoh/internal/hooks"
	"github.com/felinics/memoh/internal/mcp"
	memprovider "github.com/felinics/memoh/internal/memory/adapters"
	"github.com/felinics/memoh/internal/settings"
)

const maxVisibleMemorySourceRefs = memprovider.MaxSourceRefsPerToolResult

// maxMemoryToolBodyRunes bounds one written memory. A memory is one
// self-contained statement; the cap is what keeps the write tools from
// becoming a transcript dump with extra steps.
const maxMemoryToolBodyRunes = 2000

// MemorySettingsReader returns bot settings for memory provider resolution.
type MemorySettingsReader interface {
	GetBot(ctx context.Context, botID string) (settings.Settings, error)
}

// memoryHookService runs the memory write hooks. Tools own their own policy
// gate — the workspace tools do the same — because the hook service lives
// above the memory adapters and cannot be reached from inside them.
type memoryHookService interface {
	Run(ctx context.Context, req hooks.Request, runner hooks.ToolRunner) (hooks.Result, error)
}

type MemoryProvider struct {
	registry    *memprovider.Registry
	settings    MemorySettingsReader
	sessions    SessionLister
	hookService memoryHookService
	logger      *slog.Logger
}

func NewMemoryProvider(log *slog.Logger, registry *memprovider.Registry, settingsSvc MemorySettingsReader, sessions SessionLister, hookServices ...*hooks.Service) *MemoryProvider {
	if log == nil {
		log = slog.Default()
	}
	p := &MemoryProvider{
		registry: registry,
		settings: settingsSvc,
		sessions: sessions,
		logger:   log.With(slog.String("tool", "memory")),
	}
	if len(hookServices) > 0 && hookServices[0] != nil {
		p.hookService = hookServices[0]
	}
	return p
}

// SetHookService wires the hook gate after construction, keeping FX free of a
// cycle through the hook service.
func (p *MemoryProvider) SetHookService(h *hooks.Service) {
	if p == nil || h == nil {
		return
	}
	p.hookService = h
}

func (*MemoryProvider) Usage(_ context.Context, _ SessionContext, available AvailableTools) string {
	searchRef, hasSearch := available.Ref(ToolSearchMemory())
	createRef, hasCreate := available.Ref(ToolCreateMemory())
	if !hasSearch && !hasCreate {
		return ""
	}
	parts := make([]string, 0, 5)
	if hasSearch {
		parts = append(parts,
			"Use "+searchRef+" to recall durable user preferences, prior conversations, project context, and other long-term facts beyond the current context window.",
			"When retrieved memory conflicts with the latest user message or visible context, treat the latest user message and current context as authoritative.",
		)
		if historyRef, historyOK := available.Ref(ToolGetMessages()); historyOK {
			parts = append(parts, "When "+searchRef+" returns `source_refs`, verify exact supporting messages with "+historyRef+" by passing both `session_id` and `message_id` from a ref.")
		}
	}
	if hasCreate {
		line := "Nothing else writes memory for you: save a fact with " + createRef + " when you learn something a later conversation would need, or when the user asks you to remember it. One self-contained statement per call, and never transient task state or secrets."
		if hasSearch {
			line += " Check " + searchRef + " first — a fact already stored should be corrected in place, not saved twice."
		}
		parts = append(parts, line)
	}
	if updateRef, ok := available.Ref(ToolUpdateMemory()); ok {
		line := "When a remembered fact changes or turns out to be wrong, correct it with " + updateRef
		if deleteRef, deleteOK := available.Ref(ToolDeleteMemory()); deleteOK {
			line += ", or drop it with " + deleteRef + " when nothing replaces it"
		}
		parts = append(parts, line+". Saving a corrected copy instead leaves the stale fact in play, and both come back on the next recall.")
	}
	return usageSection("Long-term memory", parts)
}

func (p *MemoryProvider) Tools(ctx context.Context, session SessionContext) ([]toolexec.Tool, error) {
	provider := p.resolveProvider(ctx, session.BotID)
	if provider == nil {
		return nil, nil
	}
	mcpSession := toMCPSession(session)
	descriptors, err := provider.ListTools(ctx, mcpSession)
	if err != nil {
		return nil, nil
	}
	var tools []toolexec.Tool
	for _, desc := range descriptors {
		desc := desc
		prov := provider
		sess := mcpSession
		schema, err := toolexec.ResolveSchema(desc.InputSchema)
		if err != nil {
			p.logger.Warn("memory tool schema is not usable; tool skipped", slog.String("tool", desc.Name), slog.Any("error", err))
			continue
		}
		tools = append(tools, toolexec.Tool{
			Name:        desc.Name,
			Description: desc.Description,
			Parameters:  schema,
			Execute: func(ctx *toolexec.ToolExecContext, input sdk.ToolArguments) (sdk.ToolOutput, error) {
				args := inputAsMap(input)
				result, err := prov.CallTool(ctx.Context, sess, desc.Name, args)
				if err != nil {
					return sdk.ToolOutput{}, err
				}
				output := normalizeToolResult(result)
				if desc.Name == ToolSearchMemory().String() {
					output = p.filterSourceRefs(ctx.Context, session, output)
				}
				return toolexec.OutputFromValue(output), nil
			},
		})
	}
	return append(tools, p.writeTools(session, provider)...), nil
}

// writeTools are the agent-authored memory writes. They live here rather than
// behind the provider's own MCP descriptors because this is the layer that
// holds the session identity and the hook gate; a write issued from inside the
// memory adapter can reach neither. Both the native loop and the external
// runtimes reach these through this provider, so one definition covers both.
func (p *MemoryProvider) writeTools(session SessionContext, provider memprovider.Provider) []toolexec.Tool {
	return []toolexec.Tool{
		{
			Name: ToolCreateMemory().String(),
			Description: "Save one durable fact to long-term memory, so a later conversation can " +
				"use it without this one. Write a single self-contained statement in the third " +
				"person. Search memory first: when the fact is already stored, update that entry " +
				"instead of adding a second one. Skip transient task state, secrets, and anything " +
				"the user asked you not to keep.",
			Parameters: toolexec.SchemaFor[createMemoryArgs](toolexec.EnumStrings("layer", memprovider.MemoryLayers())),
			Execute: toolexec.Typed(func(ctx *toolexec.ToolExecContext, args createMemoryArgs) (sdk.ToolOutput, error) {
				memory, err := memoryWriteBody(args.Memory)
				if err != nil {
					return sdk.ToolOutput{}, err
				}
				if err := p.gateMemoryWrite(ctx.Context, session, ToolCreateMemory().String(), memory, ""); err != nil {
					return sdk.ToolOutput{}, err
				}
				resp, err := provider.Add(ctx.Context, memprovider.AddRequest{
					Message:  memory,
					BotID:    strings.TrimSpace(session.BotID),
					Metadata: p.memoryWriteMetadata(ctx.Context, session, args.Layer, args.Subject, args.Topic),
					Filters:  memprovider.BotScopeFilters(strings.TrimSpace(session.BotID)),
				})
				if err != nil {
					p.logger.WarnContext(ctx.Context, "create memory failed", slog.String("bot_id", session.BotID), slog.Any("error", err))
					return sdk.ToolOutput{}, errors.New("saving the memory failed")
				}
				out := map[string]any{"memory": memory}
				if len(resp.Results) > 0 {
					if id := strings.TrimSpace(resp.Results[0].ID); id != "" {
						out["id"] = id
					}
				}
				p.afterMemoryWrite(ctx.Context, session, ToolCreateMemory().String(), memory, stringField(out, "id"))
				return toolexec.OutputFromValue(out), nil
			}),
		},
		{
			Name: ToolUpdateMemory().String(),
			Description: "Replace the text of a memory you already stored, using an id from " +
				"searching memory. Use this whenever a remembered fact changed or turned out to " +
				"be wrong — saving a corrected copy instead leaves the stale one in play, and " +
				"both come back on the next recall.",
			Parameters: toolexec.SchemaFor[updateMemoryArgs](),
			Execute: toolexec.Typed(func(ctx *toolexec.ToolExecContext, args updateMemoryArgs) (sdk.ToolOutput, error) {
				memoryID := strings.TrimSpace(args.ID)
				if memoryID == "" {
					return sdk.ToolOutput{}, errors.New("id is required")
				}
				memory, err := memoryWriteBody(args.Memory)
				if err != nil {
					return sdk.ToolOutput{}, err
				}
				if err := p.gateMemoryWrite(ctx.Context, session, ToolUpdateMemory().String(), memory, memoryID); err != nil {
					return sdk.ToolOutput{}, err
				}
				item, err := provider.Update(ctx.Context, memprovider.UpdateRequest{
					BotID:    strings.TrimSpace(session.BotID),
					MemoryID: memoryID,
					Memory:   memory,
				})
				if err != nil {
					p.logger.WarnContext(ctx.Context, "update memory failed", slog.String("bot_id", session.BotID), slog.String("memory_id", memoryID), slog.Any("error", err))
					return sdk.ToolOutput{}, errors.New("updating the memory failed")
				}
				p.afterMemoryWrite(ctx.Context, session, ToolUpdateMemory().String(), memory, memoryID)
				return toolexec.OutputFromValue(map[string]any{"id": firstNonEmpty(strings.TrimSpace(item.ID), memoryID), "memory": memory}), nil
			}),
		},
		{
			Name: ToolDeleteMemory().String(),
			Description: "Delete a memory by id when the fact no longer holds and no replacement " +
				"belongs in its place. Prefer updating over deleting when the fact merely changed.",
			Parameters: toolexec.SchemaFor[deleteMemoryArgs](),
			Execute: toolexec.Typed(func(ctx *toolexec.ToolExecContext, args deleteMemoryArgs) (sdk.ToolOutput, error) {
				memoryID := strings.TrimSpace(args.ID)
				if memoryID == "" {
					return sdk.ToolOutput{}, errors.New("id is required")
				}
				if err := p.gateMemoryWrite(ctx.Context, session, ToolDeleteMemory().String(), "", memoryID); err != nil {
					return sdk.ToolOutput{}, err
				}
				if _, err := provider.Delete(ctx.Context, strings.TrimSpace(session.BotID), memoryID); err != nil {
					p.logger.WarnContext(ctx.Context, "delete memory failed", slog.String("bot_id", session.BotID), slog.String("memory_id", memoryID), slog.Any("error", err))
					return sdk.ToolOutput{}, errors.New("deleting the memory failed")
				}
				p.afterMemoryWrite(ctx.Context, session, ToolDeleteMemory().String(), "", memoryID)
				return toolexec.OutputFromValue(map[string]any{"id": memoryID, "deleted": true}), nil
			}),
		},
	}
}

func memoryWriteBody(body string) (string, error) {
	memory := strings.TrimSpace(body)
	if memory == "" {
		return "", errors.New("memory is required")
	}
	if len([]rune(memory)) > maxMemoryToolBodyRunes {
		return "", fmt.Errorf("memory must be at most %d characters; save one self-contained fact per call", maxMemoryToolBodyRunes)
	}
	return memory, nil
}

// memoryWriteMetadata keys the write to the same profile formation would have
// used. resolveActorUserID is what keeps the two paths from splitting one
// person into two profiles.
func (p *MemoryProvider) memoryWriteMetadata(ctx context.Context, session SessionContext, layer, subject, topic string) map[string]any {
	channelIdentityID := strings.TrimSpace(session.ChannelIdentityID)
	metadata := memprovider.BuildProfileMetadata(p.resolveActorUserID(ctx, session), channelIdentityID, "")
	if metadata == nil {
		metadata = map[string]any{}
	}
	if layer := memprovider.NormalizeMemoryLayer(layer); layer != "" {
		metadata["layer"] = layer
	}
	if subject := strings.TrimSpace(subject); subject != "" {
		metadata["subject"] = subject
	}
	if topic := strings.TrimSpace(topic); topic != "" {
		metadata["topic"] = topic
	}
	return metadata
}

// resolveActorUserID answers "whose memory is this". The session's own user id
// wins, because that is the speaker this turn — the same value formation reads
// off the chat request. External agent runtimes carry no user id on the tool
// path at all (their PromptInput only has a channel identity), so the thread's
// creator stands in; without it the same person is keyed user:<id> by
// formation and channel_identity:<id> by a tool write, splitting the profile
// and the same_profile edges that hang off it. Visibility resolution prefers
// the thread creator instead — it asks who owns the thread, not who is
// speaking.
func (p *MemoryProvider) resolveActorUserID(ctx context.Context, session SessionContext) string {
	if userID := strings.TrimSpace(session.UserID); userID != "" {
		return userID
	}
	sessionID := strings.TrimSpace(session.SessionID)
	botID := strings.TrimSpace(session.BotID)
	if p.sessions == nil || sessionID == "" || botID == "" {
		return ""
	}
	threads, err := p.sessions.ListByBot(ctx, botID)
	if err != nil {
		p.logger.WarnContext(ctx, "resolve memory actor failed", slog.String("bot_id", botID), slog.Any("error", err))
		return ""
	}
	for _, thread := range threads {
		if strings.TrimSpace(thread.ID) == sessionID {
			return strings.TrimSpace(thread.CreatedByUserID)
		}
	}
	return ""
}

// gateMemoryWrite runs BeforeMemoryWrite. A deny is reported back to the model
// as a tool error: the agent asked for this write explicitly, so dropping it
// silently — the way the post-turn path does — would read as success.
func (p *MemoryProvider) gateMemoryWrite(ctx context.Context, session SessionContext, toolName, memory, memoryID string) error {
	if p == nil || p.hookService == nil {
		return nil
	}
	res, err := p.hookService.Run(ctx, p.memoryHookRequest(session, hooks.EventBeforeMemoryWrite, toolName, memory, memoryID), nil)
	denied := res.Decision == hooks.DecisionDeny || errors.Is(err, hooks.ErrDenied)
	if err != nil && !denied {
		// A broken hook must not take memory down with it; the post-turn path
		// warns and proceeds for the same reason.
		p.logger.WarnContext(ctx, "before memory write hook failed",
			slog.String("bot_id", session.BotID), slog.String("tool", toolName), slog.Any("error", err))
		return nil
	}
	if denied {
		reason := strings.TrimSpace(res.Reason)
		if reason == "" {
			reason = "denied by hook"
		}
		return errors.New("memory write rejected: " + reason)
	}
	return nil
}

func (p *MemoryProvider) afterMemoryWrite(ctx context.Context, session SessionContext, toolName, memory, memoryID string) {
	if p == nil || p.hookService == nil {
		return
	}
	if _, err := p.hookService.Run(ctx, p.memoryHookRequest(session, hooks.EventAfterMemoryWrite, toolName, memory, memoryID), nil); err != nil {
		p.logger.WarnContext(ctx, "after memory write hook failed",
			slog.String("bot_id", session.BotID), slog.String("tool", toolName), slog.Any("error", err))
	}
}

func (*MemoryProvider) memoryHookRequest(session SessionContext, event, toolName, memory, memoryID string) hooks.Request {
	payload := map[string]any{
		"scope": "tool_write",
		"tool":  toolName,
	}
	if memory != "" {
		payload["memory"] = memory
	}
	if memoryID != "" {
		payload["memory_id"] = memoryID
	}
	return hooks.Request{
		Version:   1,
		Event:     event,
		BotID:     strings.TrimSpace(session.BotID),
		SessionID: strings.TrimSpace(session.SessionID),
		ChatID:    strings.TrimSpace(session.ChatID),
		Memory:    payload,
	}
}

func stringField(m map[string]any, key string) string {
	value, _ := m[key].(string)
	return value
}

func (p *MemoryProvider) filterSourceRefs(ctx context.Context, session SessionContext, output any) any {
	_, allowed, err := visibleHistorySessions(ctx, p.sessions, session)
	if err != nil {
		p.logger.WarnContext(ctx, "memory source ref scope lookup failed", slog.Any("error", err))
	}
	return filterSourceRefsValue(output, allowed)
}

func filterSourceRefsValue(value any, allowed map[string]struct{}) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			if key == "source_refs" {
				if filtered := visibleSourceRefs(item, allowed); len(filtered) > 0 {
					typed[key] = filtered
				} else {
					delete(typed, key)
				}
				continue
			}
			typed[key] = filterSourceRefsValue(item, allowed)
		}
		return typed
	case []map[string]any:
		for i := range typed {
			typed[i] = filterSourceRefsValue(typed[i], allowed).(map[string]any)
		}
		return typed
	case []any:
		for i := range typed {
			typed[i] = filterSourceRefsValue(typed[i], allowed)
		}
		return typed
	default:
		return value
	}
}

func visibleSourceRefs(value any, allowed map[string]struct{}) []map[string]any {
	var refs []map[string]any
	switch typed := value.(type) {
	case []map[string]any:
		refs = typed
	case []any:
		refs = make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if ref, ok := item.(map[string]any); ok {
				refs = append(refs, ref)
			}
		}
	default:
		return nil
	}
	filtered := make([]map[string]any, 0, len(refs))
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		sessionID, _ := ref["session_id"].(string)
		messageID, _ := ref["message_id"].(string)
		sessionID = strings.TrimSpace(sessionID)
		messageID = strings.TrimSpace(messageID)
		if !historySessionVisible(allowed, sessionID) || messageID == "" {
			continue
		}
		key := memprovider.EncodeSourceRef(sessionID, messageID)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		filtered = append(filtered, map[string]any{"session_id": sessionID, "message_id": messageID})
	}
	if len(filtered) > maxVisibleMemorySourceRefs {
		filtered = filtered[len(filtered)-maxVisibleMemorySourceRefs:]
	}
	return filtered
}

func (p *MemoryProvider) resolveProvider(ctx context.Context, botID string) memprovider.Provider {
	if p.registry == nil || p.settings == nil {
		return nil
	}
	botID = strings.TrimSpace(botID)
	if botID == "" {
		return nil
	}
	botSettings, err := p.settings.GetBot(ctx, botID)
	if err != nil {
		return nil
	}
	providerID := strings.TrimSpace(botSettings.MemoryProviderID)
	if providerID == "" {
		return nil
	}
	prov, err := p.registry.Get(ctx, providerID)
	if err != nil {
		return nil
	}
	return prov
}

func toMCPSession(s SessionContext) mcp.ToolSessionContext {
	return mcp.ToolSessionContext{
		BotID:                    s.BotID,
		UserID:                   s.UserID,
		ChatID:                   s.ChatID,
		SessionID:                s.SessionID,
		SessionType:              s.SessionType,
		ChannelIdentityID:        s.ChannelIdentityID,
		SessionToken:             s.SessionToken,
		CurrentPlatform:          s.CurrentPlatform,
		ReplyTarget:              s.ReplyTarget,
		ConversationType:         s.ConversationType,
		IsSubagent:               s.IsSubagent,
		ReasoningStoredEffort:    s.ReasoningStoredEffort,
		ReasoningRequestedEffort: s.ReasoningRequestedEffort,
	}
}

// normalizeToolResult extracts structuredContent from MCP-style results
// so the LLM sees clean data instead of the MCP wrapper.
func normalizeToolResult(result map[string]any) any {
	if result == nil {
		return map[string]any{"ok": true}
	}
	if sc, ok := result["structuredContent"]; ok && sc != nil {
		return sc
	}
	if content, ok := result["content"]; ok {
		if items, ok := content.([]map[string]any); ok && len(items) == 1 {
			if text, ok := items[0]["text"].(string); ok {
				var parsed any
				if json.Unmarshal([]byte(text), &parsed) == nil {
					return parsed
				}
				return text
			}
		}
	}
	return result
}

type createMemoryArgs struct {
	Layer   string `json:"layer,omitempty" jsonschema:"What kind of fact this is. Defaults to note."`
	Memory  string `json:"memory" jsonschema:"The fact to remember, as one self-contained statement."`
	Subject string `json:"subject,omitempty" jsonschema:"Who or what the fact is about, when it is not the user."`
	Topic   string `json:"topic,omitempty" jsonschema:"Short topic label used to relate this memory to others."`
}

type updateMemoryArgs struct {
	ID     string `json:"id" jsonschema:"Id of the memory to replace, as returned by memory search."`
	Memory string `json:"memory" jsonschema:"The corrected fact, as one self-contained statement."`
}

type deleteMemoryArgs struct {
	ID string `json:"id" jsonschema:"Id of the memory to delete, as returned by memory search."`
}
