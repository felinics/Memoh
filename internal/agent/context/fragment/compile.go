package contextfrag

import (
	"fmt"
	"strings"

	sdk "github.com/felinics/twilight/sdk"
)

const (
	CollectorRunConfigFields = "run_config_fields"
	SourceRunConfig          = "run_config"
	SourceAgentToolUsage     = "agent_tool_usage"
)

// CompileInput contains the legacy RunConfig fields used as phase-1 sources.
type CompileInput struct {
	Source   string
	Scope    Scope
	System   string
	Messages []sdk.Message
	// CurrentUserMessageIndex identifies a current request already carried in
	// Messages so legacy manifests retain its typed slot.
	CurrentUserMessageIndex *int
	// MemoryMessageIndex identifies materialized memory recall that remains in
	// Messages for byte-equivalent provider ordering.
	MemoryMessageIndex *int
	Query              string
	InlineImages       []sdk.ImagePart
	ToolUsage          string
	View               ManifestView
	DynamicMutators    []DynamicMutator
	Existing           []ContextFrag
}

// CompileFrags builds the typed fragment list from the current SDK-shaped
// fields, preserving explicit non-derived fragments such as tool usage — the
// same fragment-construction `Compile` does, without also rendering the
// legacy System/Messages view or building the provenance manifest. For
// callers that only need the fragments themselves.
func CompileFrags(input CompileInput) []ContextFrag {
	source := strings.TrimSpace(input.Source)
	if source == "" {
		source = SourceRunConfig
	}
	scope := normalizeScope(input.Scope)

	explicit := preserveExplicit(input.Existing)
	frags := make([]ContextFrag, 0, len(explicit)+len(input.Messages)+4)
	derivedMessageIDs := make(map[string]struct{}, len(input.Messages))
	for i := range input.Messages {
		derivedMessageIDs[fmt.Sprintf("message.%03d", i)] = struct{}{}
	}
	messageOverrides := make(map[string]ContextFrag, len(explicit))
	for _, frag := range explicit {
		if id := strings.TrimSpace(frag.ID); id != "" && overridesDerivedMessage(frag) {
			if _, ok := derivedMessageIDs[id]; ok {
				messageOverrides[id] = frag
				continue
			}
		}
		frags = append(frags, frag)
	}
	if strings.TrimSpace(input.System) != "" {
		frags = append(frags, systemFrags(input.System, strings.TrimSpace(input.ToolUsage), scope, source)...)
	}
	for i, msg := range input.Messages {
		id := fmt.Sprintf("message.%03d", i)
		if frag, ok := messageOverrides[id]; ok {
			frags = append(frags, frag)
			continue
		}
		kind := kindForMessage(msg)
		slot := SlotHistory
		cacheClass := cacheForMessage(msg)
		trust := trustForMessage(msg)
		budget := BudgetPolicy{}
		messageScope := scope
		if isMemoryMessage(input.MemoryMessageIndex, i, msg) {
			kind = KindMemoryRecall
			cacheClass = CacheNever
			trust = TrustWorkspace
		}
		if isCurrentUserMessage(input.CurrentUserMessageIndex, i, msg) {
			kind = KindCurrentUserMessage
			slot = SlotCurrentUser
			cacheClass = CacheNever
			trust = TrustUser
			budget.Overflow = OverflowKeep
		}
		frags = append(frags, MessageFrag(MessageFragInput{
			ID:         id,
			Message:    msg,
			Kind:       kind,
			Slot:       slot,
			Priority:   PriorityForMessage(msg),
			CacheClass: cacheClass,
			Trust:      trust,
			Scope:      messageScope,
			Source:     source,
			Collector:  CollectorRunConfigFields,
			Index:      i,
			Budget:     budget,
		}))
	}
	if strings.TrimSpace(input.Query) != "" {
		frags = append(frags, TextFrag(TextFragInput{
			ID:         "current_user.message",
			Kind:       KindCurrentUserMessage,
			Role:       sdk.MessageRoleUser,
			Slot:       SlotCurrentUser,
			Text:       strings.TrimSpace(input.Query),
			Priority:   90,
			CacheClass: CacheNever,
			Trust:      TrustUser,
			Scope:      scope,
			Source:     source,
			Collector:  CollectorRunConfigFields,
			Render:     RenderPolicy{Format: RenderSDKMessage},
		}))
	}
	if len(input.InlineImages) > 0 {
		imageFrag := ImageFrag("current_user.images", input.InlineImages, scope, source)
		if len(imageFrag.Parts) > 0 {
			frags = append(frags, imageFrag)
		}
	}

	return normalizeContextRefs(frags)
}

func isCurrentUserMessage(index *int, candidate int, msg sdk.Message) bool {
	return index != nil && *index == candidate && msg.Role == sdk.MessageRoleUser
}

func isMemoryMessage(index *int, candidate int, msg sdk.Message) bool {
	return index != nil && *index == candidate && msg.Role == sdk.MessageRoleUser
}

// Compile builds typed fragments from the current SDK-shaped fields, preserving
// explicit non-derived fragments such as tool usage.
func Compile(input CompileInput) AssembledContext {
	frags := CompileFrags(input)
	assembled := Render(frags)
	assembled.Frags = frags
	assembled.Manifest = BuildManifest(frags)
	assembled.Manifest.View = input.View
	if assembled.Manifest.View == "" {
		assembled.Manifest.View = ViewRunConfigPreProvider
	}
	assembled.Manifest.DynamicMutators = normalizeDynamicMutators(input.DynamicMutators)
	return assembled
}

func systemFrags(system string, toolUsage string, scope Scope, source string) []ContextFrag {
	system = strings.TrimSpace(system)
	if system == "" {
		return nil
	}
	toolStart := -1
	if toolUsage != "" {
		toolStart = strings.Index(system, toolUsage)
	}
	if toolStart < 0 {
		return []ContextFrag{systemTextFrag("system.prompt", KindSystemPrompt, system, 20, CacheStable, scope, source, 0)}
	}

	var frags []ContextFrag
	if prefix := strings.TrimSpace(system[:toolStart]); prefix != "" {
		frags = append(frags, systemTextFrag("system.prompt", KindSystemPrompt, prefix, 20, CacheStable, scope, source, 0))
	}

	rest := strings.TrimSpace(system[toolStart:])
	toolEnd := len(toolUsage)
	toolUsageText := strings.TrimSpace(rest[:toolEnd])
	if toolUsageText != "" {
		frags = append(frags, systemTextFrag("system.tool_usage", KindToolUsage, toolUsageText, 45, CacheStable, scope, SourceAgentToolUsage, 1))
	}
	if suffix := strings.TrimSpace(rest[toolEnd:]); suffix != "" {
		kind := KindSystemPrompt
		id := "system.prompt.tail"
		if strings.HasPrefix(suffix, "## Workspace instruction files") {
			kind = KindWorkspaceInstruction
			id = "system.workspace_instructions"
		}
		frags = append(frags, systemTextFrag(id, kind, suffix, 50, CacheStable, scope, source, 2))
	}
	return frags
}

func systemTextFrag(id string, kind Kind, text string, priority int, cacheClass CacheClass, scope Scope, source string, index int) ContextFrag {
	return TextFrag(TextFragInput{
		ID:            id,
		Kind:          kind,
		Role:          sdk.MessageRoleSystem,
		Slot:          SlotSystem,
		Text:          text,
		Priority:      priority,
		RetentionTier: RetentionRequired,
		CacheClass:    cacheClass,
		Trust:         TrustSystem,
		Scope:         scope,
		Source:        source,
		Collector:     CollectorRunConfigFields,
		Index:         index,
		Render:        RenderPolicy{Format: RenderMarkdown},
	})
}

// TextFragInput describes a text fragment to construct.
type TextFragInput struct {
	ID                 string
	Kind               Kind
	Role               sdk.MessageRole
	Slot               Slot
	Text               string
	Priority           int
	RetentionTier      RetentionTier
	DropPriority       DropPriority
	RequiredCapability string
	CacheClass         CacheClass
	Trust              TrustLevel
	Scope              Scope
	Source             string
	SourceID           string
	Collector          string
	Index              int
	Render             RenderPolicy
	Budget             BudgetPolicy
	ConflictKey        string
}

// TextFrag creates a text-backed fragment.
func TextFrag(input TextFragInput) ContextFrag {
	return ContextFrag{
		ID:                 strings.TrimSpace(input.ID),
		Kind:               input.Kind,
		Role:               input.Role,
		Slot:               input.Slot,
		Priority:           input.Priority,
		RetentionTier:      input.RetentionTier,
		DropPriority:       input.DropPriority,
		RequiredCapability: strings.TrimSpace(input.RequiredCapability),
		CacheClass:         input.CacheClass,
		Trust:              input.Trust,
		Scope:              normalizeScope(input.Scope),
		Budget:             input.Budget,
		Render:             input.Render,
		ConflictKey:        input.ConflictKey,
		Provenance: Provenance{
			Source:    strings.TrimSpace(input.Source),
			SourceID:  strings.TrimSpace(input.SourceID),
			Collector: strings.TrimSpace(input.Collector),
			Index:     input.Index,
		},
		Parts: []Part{{
			Type: PartText,
			Text: RenderText(input.Text, input.Render),
		}},
	}
}

// MessageFragInput describes an SDK message fragment.
type MessageFragInput struct {
	ID                 string
	Message            sdk.Message
	Kind               Kind
	Slot               Slot
	Priority           int
	RetentionTier      RetentionTier
	DropPriority       DropPriority
	RequiredCapability string
	CacheClass         CacheClass
	Trust              TrustLevel
	Scope              Scope
	Source             string
	SourceID           string
	Collector          string
	Index              int
	TokenEstimate      int
	Budget             BudgetPolicy
	Render             RenderPolicy
}

// MessageFrag creates a message-backed fragment.
func MessageFrag(input MessageFragInput) ContextFrag {
	msg := cloneMessage(input.Message)
	renderPolicy := input.Render
	if renderPolicy.Format == "" {
		renderPolicy.Format = RenderSDKMessage
	}
	return ContextFrag{
		TokenEstimate:      input.TokenEstimate,
		Budget:             input.Budget,
		ID:                 strings.TrimSpace(input.ID),
		Kind:               input.Kind,
		Role:               input.Message.Role,
		Slot:               input.Slot,
		Priority:           input.Priority,
		RetentionTier:      input.RetentionTier,
		DropPriority:       input.DropPriority,
		RequiredCapability: strings.TrimSpace(input.RequiredCapability),
		CacheClass:         input.CacheClass,
		Trust:              input.Trust,
		Scope:              normalizeScope(input.Scope),
		Render:             renderPolicy,
		Provenance: Provenance{
			Source:    strings.TrimSpace(input.Source),
			SourceID:  strings.TrimSpace(input.SourceID),
			Collector: strings.TrimSpace(input.Collector),
			Index:     input.Index,
		},
		Parts: []Part{{
			Type:       PartSDKMessage,
			Message:    &msg,
			SDKMessage: &msg,
		}},
	}
}

// ImageFrag creates one fragment for native image parts.
func ImageFrag(id string, images []sdk.ImagePart, scope Scope, source string) ContextFrag {
	parts := make([]Part, 0, len(images))
	for _, image := range images {
		if strings.TrimSpace(image.Image) == "" {
			continue
		}
		img := image
		parts = append(parts, Part{
			Type: PartImage,
			Image: ImageRef{
				MediaType: strings.TrimSpace(image.MediaType),
				Source:    "inline",
			},
			ImagePart: &img,
			SDKImage:  &img,
		})
	}
	return ContextFrag{
		ID:         strings.TrimSpace(id),
		Kind:       KindNativeImage,
		Role:       sdk.MessageRoleUser,
		Slot:       SlotCurrentUser,
		Priority:   90,
		CacheClass: CacheNever,
		Trust:      TrustUser,
		Scope:      normalizeScope(scope),
		Render:     RenderPolicy{Format: RenderNativePart},
		Provenance: Provenance{
			Source:    strings.TrimSpace(source),
			Collector: CollectorRunConfigFields,
		},
		Parts: parts,
	}
}

// Upsert returns frags with next inserted or replacing an existing fragment
// with the same ID.
func Upsert(frags []ContextFrag, next ContextFrag) []ContextFrag {
	next.ID = strings.TrimSpace(next.ID)
	if next.ID == "" {
		return frags
	}
	out := make([]ContextFrag, 0, len(frags)+1)
	replaced := false
	for _, frag := range frags {
		if frag.ID == next.ID {
			if !replaced {
				out = append(out, next)
				replaced = true
			}
			continue
		}
		out = append(out, frag)
	}
	if !replaced {
		out = append(out, next)
	}
	return out
}

func preserveExplicit(frags []ContextFrag) []ContextFrag {
	if len(frags) == 0 {
		return nil
	}
	out := make([]ContextFrag, 0, len(frags))
	for _, frag := range frags {
		if frag.Provenance.Collector == CollectorRunConfigFields {
			continue
		}
		out = append(out, frag)
	}
	return out
}

func overridesDerivedMessage(frag ContextFrag) bool {
	if frag.Slot != SlotHistory {
		return false
	}
	for _, part := range frag.Parts {
		if part.Type == PartSDKMessage && partMessage(part) != nil {
			return true
		}
	}
	return false
}

func kindForMessage(msg sdk.Message) Kind {
	switch msg.Role {
	case sdk.MessageRoleSystem:
		return KindSystemPolicy
	case sdk.MessageRoleUser:
		return KindConversationEvent
	case sdk.MessageRoleAssistant:
		return KindConversationEvent
	case sdk.MessageRoleTool:
		return KindConversationEvent
	default:
		return KindConversationEvent
	}
}

func PriorityForMessage(msg sdk.Message) int {
	switch msg.Role {
	case sdk.MessageRoleSystem:
		return 30
	case sdk.MessageRoleTool:
		return 55
	case sdk.MessageRoleUser, sdk.MessageRoleAssistant:
		return 70
	default:
		return 70
	}
}

func cacheForMessage(msg sdk.Message) CacheClass {
	switch msg.Role {
	case sdk.MessageRoleSystem:
		return CacheDynamic
	case sdk.MessageRoleUser, sdk.MessageRoleAssistant, sdk.MessageRoleTool:
		return CacheStable
	default:
		return CacheNever
	}
}

func trustForMessage(msg sdk.Message) TrustLevel {
	switch msg.Role {
	case sdk.MessageRoleSystem:
		return TrustSystem
	case sdk.MessageRoleAssistant, sdk.MessageRoleTool:
		return TrustWorkspace
	case sdk.MessageRoleUser:
		return TrustExternal
	default:
		return TrustExternal
	}
}

func normalizeScope(scope Scope) Scope {
	if len(scope.Attention) == 0 {
		scope.Attention = nil
	}
	if len(scope.Metadata) == 0 {
		scope.Metadata = nil
	}
	return scope
}

func normalizeContextRefs(frags []ContextFrag) []ContextFrag {
	if len(frags) == 0 {
		return nil
	}
	out := make([]ContextFrag, 0, len(frags))
	for _, frag := range frags {
		normalized := WithContextRef(frag, frag.Ref)
		out = append(out, normalized)
	}
	return out
}

func normalizeDynamicMutators(mutators []DynamicMutator) []DynamicMutator {
	if len(mutators) == 0 {
		return nil
	}
	out := make([]DynamicMutator, 0, len(mutators))
	seen := make(map[DynamicMutator]bool, len(mutators))
	for _, mutator := range mutators {
		if mutator == "" || seen[mutator] {
			continue
		}
		seen[mutator] = true
		out = append(out, mutator)
	}
	return out
}
