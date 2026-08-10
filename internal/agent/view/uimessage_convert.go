package view

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/memohai/memoh/internal/agent/turn"
	messagepkg "github.com/memohai/memoh/internal/chat/message"
	"github.com/memohai/memoh/internal/textutil"
)

const uiReplyPreviewMaxRunes = 120

var (
	uiMessageYAMLHeaderRe        = regexp.MustCompile(`(?s)\A---\n.*?\n---\n?`)
	uiMessageAgentTagsRe         = regexp.MustCompile(`(?s)<attachments>.*?</attachments>|<reactions>.*?</reactions>|<speech>.*?</speech>`)
	uiMessageCollapsedNewlinesRe = regexp.MustCompile(`\n{3,}`)
	uiTaskNotificationRe         = regexp.MustCompile(`(?s)<task-notification>\s*(.*?)\s*</task-notification>`)
	uiMetadataParseKeys          = [][]byte{
		[]byte(`"forward"`),
		[]byte(`"model_requested_skills"`),
		[]byte(`"platform"`),
		[]byte(`"reply"`),
		[]byte(`"skill_activation"`),
		[]byte(`"user_message_kind"`),
	}
)

type uiContentPart struct {
	Type             string         `json:"type"`
	Text             string         `json:"text,omitempty"`
	URL              string         `json:"url,omitempty"`
	Emoji            string         `json:"emoji,omitempty"`
	ToolCallID       string         `json:"toolCallId,omitempty"`
	ToolName         string         `json:"toolName,omitempty"`
	Input            any            `json:"input,omitempty"`
	Output           any            `json:"output,omitempty"`
	Result           any            `json:"result,omitempty"`
	ProviderMetadata map[string]any `json:"providerMetadata,omitempty"`
}

type uiExtractedToolCall struct {
	ID                string
	Name              string
	Input             any
	Approval          *UIToolApproval
	ExecutionLocation *UIExecutionLocation
	UserInput         *UIUserInput
}

type uiExtractedToolResult struct {
	ToolCallID string
	Output     any
}

type uiBackgroundToolRef struct {
	TurnIndex    int
	MessageIndex int
}

type uiPendingAssistantTurn struct {
	Turn        UITurn
	NextID      int
	ToolIndexes map[string]int
}

type uiDecodedModelMessage struct {
	turn.ModelMessage

	contentText             string
	contentTextDecoded      bool
	contentParts            []uiContentPart
	contentPartsDecoded     bool
	toolResultOutput        any
	toolResultOutputDecoded bool
}

// ConvertRawModelMessagesToUIAssistantMessages converts terminal stream payload
// messages into frontend-friendly assistant UI messages.
func ConvertRawModelMessagesToUIAssistantMessages(raw json.RawMessage) []UIMessage {
	if len(raw) == 0 {
		return nil
	}

	var messages []turn.ModelMessage
	if err := json.Unmarshal(raw, &messages); err != nil {
		return nil
	}
	return ConvertModelMessagesToUIAssistantMessages(messages)
}

// ConvertModelMessagesToUIAssistantMessages converts assistant/tool output
// messages into frontend-friendly UI message blocks.
func ConvertModelMessagesToUIAssistantMessages(messages []turn.ModelMessage) []UIMessage {
	pending := &uiPendingAssistantTurn{
		ToolIndexes: map[string]int{},
	}

	for _, modelMessage := range messages {
		decoded := decodeUIModelMessage(modelMessage)
		switch strings.ToLower(strings.TrimSpace(decoded.Role)) {
		case "assistant":
			for _, reasoning := range extractPersistedReasoning(&decoded) {
				appendPendingAssistantMessage(pending, UIMessage{
					Type:    UIMessageReasoning,
					Content: reasoning,
				})
			}

			if text := extractAssistantStreamMessageText(&decoded); text != "" {
				appendPendingAssistantMessage(pending, UIMessage{
					Type:    UIMessageText,
					Content: text,
				})
			}

			for _, call := range extractPersistedToolCalls(&decoded) {
				appendPendingAssistantMessage(pending, UIMessage{
					Type:              UIMessageTool,
					Name:              call.Name,
					Input:             call.Input,
					ToolCallID:        call.ID,
					Running:           uiBoolPtr(true),
					Approval:          call.Approval,
					ExecutionLocation: call.ExecutionLocation,
					UserInput:         call.UserInput,
				})
				if call.ID != "" {
					pending.ToolIndexes[call.ID] = len(pending.Turn.Messages) - 1
				}
			}

		case "tool":
			for _, toolResult := range extractPersistedToolResults(&decoded) {
				idx, ok := pending.ToolIndexes[toolResult.ToolCallID]
				if !ok || idx < 0 || idx >= len(pending.Turn.Messages) {
					continue
				}

				applyToolResultToUIMessage(&pending.Turn.Messages[idx], toolResult.Output)
			}
		}
	}

	for _, idx := range pending.ToolIndexes {
		if idx >= 0 && idx < len(pending.Turn.Messages) {
			if !isBackgroundToolStillRunning(pending.Turn.Messages[idx]) {
				pending.Turn.Messages[idx].Running = uiBoolPtr(false)
			}
		}
	}

	return pending.Turn.Messages
}

// ConvertMessagesToUITurns converts persisted message rows into frontend-friendly turns.
func ConvertMessagesToUITurns(messages []messagepkg.Message) []UITurn {
	result := make([]UITurn, 0, len(messages))
	var pending *uiPendingAssistantTurn
	var backgroundToolRefs map[string]uiBackgroundToolRef

	registerBackgroundTools := func(turnIndex int) {
		if turnIndex < 0 || turnIndex >= len(result) {
			return
		}
		for msgIndex, message := range result[turnIndex].Messages {
			if message.Background == nil {
				continue
			}
			taskID := strings.TrimSpace(message.Background.TaskID)
			if taskID == "" {
				continue
			}
			if backgroundToolRefs == nil {
				backgroundToolRefs = make(map[string]uiBackgroundToolRef, 1)
			}
			backgroundToolRefs[taskID] = uiBackgroundToolRef{TurnIndex: turnIndex, MessageIndex: msgIndex}
		}
	}

	completeBackgroundTool := func(task UIBackgroundTask) {
		if len(backgroundToolRefs) == 0 {
			return
		}
		taskID := strings.TrimSpace(task.TaskID)
		if taskID == "" {
			return
		}
		ref, ok := backgroundToolRefs[taskID]
		if !ok || ref.TurnIndex < 0 || ref.TurnIndex >= len(result) {
			return
		}
		turn := &result[ref.TurnIndex]
		if ref.MessageIndex < 0 || ref.MessageIndex >= len(turn.Messages) {
			return
		}
		mergeBackgroundTaskIntoTool(&turn.Messages[ref.MessageIndex], task)
	}

	flushPending := func() {
		if pending == nil {
			return
		}

		for _, idx := range pending.ToolIndexes {
			if idx < 0 || idx >= len(pending.Turn.Messages) {
				continue
			}
			if !isBackgroundToolStillRunning(pending.Turn.Messages[idx]) {
				pending.Turn.Messages[idx].Running = uiBoolPtr(false)
			}
		}

		if len(pending.Turn.Messages) > 0 {
			result = append(result, pending.Turn)
			registerBackgroundTools(len(result) - 1)
		}
		pending = nil
	}

	for i := range messages {
		raw := messages[i]
		rawTurnID := strings.TrimSpace(raw.TurnID)
		if rawTurnID == "" {
			continue
		}
		if pending != nil && pending.Turn.TurnID != rawTurnID {
			flushPending()
		}
		switch strings.ToLower(strings.TrimSpace(raw.Role)) {
		case "user":
			ensurePersistedMetadata(&raw)
			text := extractPersistedMessageText(raw, nil)
			attachments := uiAttachmentsFromMessageAssets(raw)
			reply := uiReplyFromMessage(raw)
			forward := uiForwardFromMessage(raw)
			activation := uiSkillActivationFromMessage(raw)
			userMessageKind := uiUserMessageKind(raw)
			if activation != nil && userMessageKind == "" {
				userMessageKind = turn.UserMessageKindSkillActivation
			}

			// A background-task completion notification becomes its own system
			// turn. Flush first so the originating background tool (in the pending
			// assistant turn) is registered before we complete it.
			if task, ok := parseBackgroundTaskNotification(text); ok {
				flushPending()
				completeBackgroundTool(task)
				result = append(result, UITurn{
					TurnID:         strings.TrimSpace(raw.TurnID),
					Role:           "system",
					Kind:           "background_task",
					BackgroundTask: &task,
					Timestamp:      raw.CreatedAt,
					Platform:       resolveUIPersistencePlatform(raw),
					ID:             strings.TrimSpace(raw.ID),
				})
				continue
			}

			// Invisible user-role messages must NOT end their assistant turn. The
			// agent loop injects user messages that never render: Computer Use /
			// Browser Use feed screenshots back as image-only user messages (empty
			// display text, inline base64, no stored asset), plus
			// "[background notification]" pings. Their persisted turn_id already
			// decided whether surrounding assistant/tool rows belong together.
			if text == "" && len(attachments) == 0 && reply == nil && forward == nil && activation == nil {
				continue
			}
			if strings.EqualFold(strings.TrimSpace(text), "[background notification]") {
				continue
			}

			flushPending()

			turn := UITurn{
				TurnID:            strings.TrimSpace(raw.TurnID),
				Role:              "user",
				Text:              text,
				UserMessageKind:   userMessageKind,
				SkillActivation:   activation,
				Attachments:       attachments,
				Reply:             reply,
				Forward:           forward,
				Timestamp:         raw.CreatedAt,
				Platform:          resolveUIPersistencePlatform(raw),
				ExternalMessageID: strings.TrimSpace(raw.ExternalMessageID),
				ID:                strings.TrimSpace(raw.ID),
			}
			if turn.Platform != "" {
				turn.SenderDisplayName = strings.TrimSpace(raw.SenderDisplayName)
				turn.SenderAvatarURL = strings.TrimSpace(raw.SenderAvatarURL)
				turn.SenderUserID = strings.TrimSpace(raw.SenderUserID)
			}
			result = append(result, turn)

		case "assistant":
			if strings.TrimSpace(raw.Platform) == "" {
				ensurePersistedMetadata(&raw)
			}
			modelMessage := decodePersistedModelMessage(raw)
			toolCalls := extractPersistedToolCalls(&modelMessage)
			text := extractPersistedMessageText(raw, &modelMessage)
			reasonings := extractPersistedReasoning(&modelMessage)
			attachments := uiAttachmentsFromMessageAssets(raw)

			// A persisted turn_id is the only grouping key. Plain-text assistant
			// messages and tool calls with that same id remain one reply.
			if len(toolCalls) == 0 && text == "" && len(reasonings) == 0 && len(attachments) == 0 {
				continue
			}

			if pending == nil {
				pending = newPendingAssistantTurn(raw)
			}

			for _, reasoning := range reasonings {
				appendPendingAssistantMessage(pending, UIMessage{
					ID:      pending.NextID,
					Type:    UIMessageReasoning,
					Content: reasoning,
				})
			}
			if text != "" {
				appendPendingAssistantMessage(pending, UIMessage{
					ID:      pending.NextID,
					Type:    UIMessageText,
					Content: text,
				})
			}
			for _, call := range toolCalls {
				upsertPendingToolCall(pending, call)
			}
			if len(attachments) > 0 {
				appendPendingAssistantMessage(pending, UIMessage{
					ID:          pending.NextID,
					Type:        UIMessageAttachments,
					Attachments: attachments,
				})
			}

		case "tool":
			if pending == nil {
				continue
			}

			modelMessage := decodePersistedModelMessage(raw)
			for _, toolResult := range extractPersistedToolResults(&modelMessage) {
				idx, ok := pending.ToolIndexes[toolResult.ToolCallID]
				if !ok || idx < 0 || idx >= len(pending.Turn.Messages) {
					continue
				}

				applyToolResultToUIMessage(&pending.Turn.Messages[idx], toolResult.Output)
			}
		}
	}

	flushPending()
	return result
}

func newPendingAssistantTurn(raw messagepkg.Message) *uiPendingAssistantTurn {
	return &uiPendingAssistantTurn{
		Turn: UITurn{
			TurnID:            strings.TrimSpace(raw.TurnID),
			Role:              "assistant",
			Timestamp:         raw.CreatedAt,
			Platform:          resolveUIPersistencePlatform(raw),
			ExternalMessageID: strings.TrimSpace(raw.ExternalMessageID),
			ID:                strings.TrimSpace(raw.ID),
		},
	}
}

func appendPendingAssistantMessage(pending *uiPendingAssistantTurn, message UIMessage) {
	if pending == nil {
		return
	}
	message.ID = pending.NextID
	pending.NextID++
	pending.Turn.Messages = append(pending.Turn.Messages, message)
}

func upsertPendingToolCall(pending *uiPendingAssistantTurn, call uiExtractedToolCall) {
	if pending == nil {
		return
	}
	if call.ID != "" {
		if idx, ok := pending.ToolIndexes[call.ID]; ok && idx >= 0 && idx < len(pending.Turn.Messages) {
			msg := &pending.Turn.Messages[idx]
			if call.Name != "" {
				msg.Name = call.Name
			}
			if call.Input != nil {
				msg.Input = call.Input
			}
			if call.Approval != nil {
				msg.Approval = call.Approval
			}
			if call.ExecutionLocation != nil {
				msg.ExecutionLocation = call.ExecutionLocation
			}
			if call.UserInput != nil {
				msg.UserInput = call.UserInput
			}
			msg.Running = uiBoolPtr(true)
			return
		}
	}
	block := UIMessage{
		Type:              UIMessageTool,
		Name:              call.Name,
		Input:             call.Input,
		ToolCallID:        call.ID,
		Running:           uiBoolPtr(true),
		Approval:          call.Approval,
		ExecutionLocation: call.ExecutionLocation,
		UserInput:         call.UserInput,
	}
	appendPendingAssistantMessage(pending, block)
	if call.ID != "" {
		if pending.ToolIndexes == nil {
			pending.ToolIndexes = make(map[string]int, 1)
		}
		pending.ToolIndexes[call.ID] = len(pending.Turn.Messages) - 1
	}
}

func decodeUIModelMessage(message turn.ModelMessage) uiDecodedModelMessage {
	return uiDecodedModelMessage{ModelMessage: message}
}

func decodePersistedModelMessage(raw messagepkg.Message) uiDecodedModelMessage {
	if firstJSONByte(raw.Content) != '{' {
		return uiDecodedModelMessage{ModelMessage: turn.ModelMessage{
			Role:    raw.Role,
			Content: raw.Content,
		}}
	}

	var message turn.ModelMessage
	if err := json.Unmarshal(raw.Content, &message); err != nil {
		return uiDecodedModelMessage{ModelMessage: turn.ModelMessage{
			Role:    raw.Role,
			Content: raw.Content,
		}}
	}
	message.Role = raw.Role
	return uiDecodedModelMessage{ModelMessage: message}
}

func ensurePersistedMetadata(raw *messagepkg.Message) {
	if raw == nil || raw.Metadata != nil || len(raw.RawMetadata) == 0 {
		return
	}
	if !mayContainUIMetadata(raw.RawMetadata) {
		return
	}
	var metadata map[string]any
	if err := json.Unmarshal(raw.RawMetadata, &metadata); err == nil && len(metadata) > 0 {
		raw.Metadata = metadata
	}
}

func mayContainUIMetadata(metadata json.RawMessage) bool {
	for _, key := range uiMetadataParseKeys {
		if bytes.Contains(metadata, key) {
			return true
		}
	}
	return false
}

func extractPersistedMessageText(raw messagepkg.Message, message *uiDecodedModelMessage) string {
	if strings.EqualFold(raw.Role, "user") {
		if activation := uiSkillActivationFromMessage(raw); activation != nil {
			if prompt := strings.TrimSpace(activation.Prompt); prompt != "" {
				return prompt
			}
			if text := strings.TrimSpace(raw.DisplayContent); text != "" {
				return skillActivationPromptFromPersistedText(text, activation)
			}
			content := raw.Content
			if message != nil {
				content = message.Content
			}
			if text := strings.TrimSpace(extractTextFromPersistedContent(content)); text != "" {
				return skillActivationPromptFromPersistedText(text, activation)
			}
			return ""
		}
		if text := strings.TrimSpace(raw.DisplayContent); text != "" {
			return text
		}
	}

	if message == nil {
		decoded := decodePersistedModelMessage(raw)
		message = &decoded
	}
	text := strings.TrimSpace(message.textContent())
	if text == "" {
		return ""
	}

	if strings.EqualFold(raw.Role, "user") {
		return strings.TrimSpace(stripPersistedUserStructuredContext(text))
	}
	return strings.TrimSpace(stripPersistedAgentTags(text))
}

func extractTextFromPersistedContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	if firstJSONByte(raw) == '{' {
		var modelMessage turn.ModelMessage
		if err := json.Unmarshal(raw, &modelMessage); err == nil && len(modelMessage.Content) > 0 {
			decoded := decodeUIModelMessage(modelMessage)
			return decoded.textContent()
		}
	}

	decoded := decodeUIModelMessage(turn.ModelMessage{Content: raw})
	return decoded.textContent()
}

func skillActivationPromptFromPersistedText(text string, activation *turn.SkillActivation) string {
	text = strings.TrimSpace(stripPersistedUserStructuredContext(text))
	if text == "" {
		return ""
	}
	if isSkillActivationModelMarker(text) {
		return ""
	}
	if !strings.HasPrefix(text, "/") {
		return text
	}
	head, rest := cutSlashHeadRest(text)
	selector := strings.TrimPrefix(strings.TrimSpace(head), "/")
	if before, _, ok := strings.Cut(selector, "@"); ok {
		selector = before
	}
	for _, skill := range activation.Skills {
		if selector == strings.TrimSpace(skill.Name) {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

func isSkillActivationModelMarker(text string) bool {
	text = strings.TrimSpace(text)
	return strings.HasPrefix(text, "The user activated the following skill for this turn without an additional prompt:")
}

func cutSlashHeadRest(text string) (string, string) {
	text = strings.TrimSpace(text)
	for i, r := range text {
		if unicode.IsSpace(r) {
			return text[:i], text[i:]
		}
	}
	return text, ""
}

func stripPersistedUserStructuredContext(text string) string {
	text = strings.TrimSpace(stripPersistedYAMLHeader(text))
	if text == "" {
		return ""
	}

	text = stripPersistedMessageEnvelope(text)
	lines := strings.Split(text, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if isPersistedUserContextLine(strings.TrimSpace(line)) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

func stripPersistedMessageEnvelope(text string) string {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "<message") {
		return text
	}

	openEnd := strings.IndexByte(text, '>')
	if openEnd < 0 {
		return text
	}
	openTag := strings.TrimSpace(text[:openEnd+1])
	if strings.HasSuffix(openTag, "/>") {
		return ""
	}

	body := strings.TrimSpace(text[openEnd+1:])
	if !strings.HasSuffix(body, "</message>") {
		return text
	}
	return strings.TrimSpace(strings.TrimSuffix(body, "</message>"))
}

func isPersistedUserContextLine(line string) bool {
	if line == "" {
		return false
	}
	if strings.HasPrefix(line, "<attachment ") && strings.HasSuffix(line, "/>") {
		return true
	}
	if strings.HasPrefix(line, "<image ") && strings.HasSuffix(line, "</image>") {
		return true
	}
	return strings.HasPrefix(line, "<in-reply-to ") && strings.HasSuffix(line, "</in-reply-to>")
}

func extractAssistantStreamMessageText(message *uiDecodedModelMessage) string {
	return strings.TrimSpace(stripPersistedAgentTags(message.textContent()))
}

func (message *uiDecodedModelMessage) textContent() string {
	if message == nil || len(message.Content) == 0 {
		return ""
	}
	if message.contentTextDecoded {
		return message.contentText
	}
	message.contentTextDecoded = true

	jsonKind := firstJSONByte(message.Content)
	switch jsonKind {
	case '"':
		var text string
		if err := json.Unmarshal(message.Content, &text); err == nil {
			message.contentText = strings.TrimSpace(text)
			return message.contentText
		}
	case '[', '{':
		if parts := message.parts(); len(parts) > 0 {
			message.contentText = textFromUIParts(parts)
			return message.contentText
		}
		if jsonKind == '{' {
			var object struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(message.Content, &object); err == nil {
				message.contentText = strings.TrimSpace(object.Text)
				return message.contentText
			}
		}
	}

	return ""
}

func textFromUIParts(parts []uiContentPart) string {
	var builder strings.Builder
	for _, part := range parts {
		partType := strings.TrimSpace(part.Type)
		if strings.EqualFold(partType, "reasoning") {
			continue
		}

		var text string
		switch {
		case strings.EqualFold(partType, "text") && strings.TrimSpace(part.Text) != "":
			text = strings.TrimSpace(part.Text)
		case strings.EqualFold(partType, "link") && strings.TrimSpace(part.URL) != "":
			text = strings.TrimSpace(part.URL)
		case strings.EqualFold(partType, "emoji") && strings.TrimSpace(part.Emoji) != "":
			text = strings.TrimSpace(part.Emoji)
		case strings.TrimSpace(part.Text) != "":
			text = strings.TrimSpace(part.Text)
		}
		if text == "" {
			continue
		}
		if builder.Len() > 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString(text)
	}
	return builder.String()
}

func extractPersistedReasoning(message *uiDecodedModelMessage) []string {
	parts := message.parts()
	if len(parts) == 0 {
		return nil
	}

	reasonings := make([]string, 0, len(parts))
	for _, part := range parts {
		if !strings.EqualFold(strings.TrimSpace(part.Type), "reasoning") {
			continue
		}
		if text := strings.TrimSpace(part.Text); text != "" {
			reasonings = append(reasonings, text)
		}
	}
	return reasonings
}

func extractPersistedToolCalls(message *uiDecodedModelMessage) []uiExtractedToolCall {
	parts := message.parts()
	calls := make([]uiExtractedToolCall, 0, len(parts)+len(message.ToolCalls))
	for _, part := range parts {
		if !strings.EqualFold(strings.TrimSpace(part.Type), "tool-call") {
			continue
		}
		calls = append(calls, uiExtractedToolCall{
			ID:                strings.TrimSpace(part.ToolCallID),
			Name:              strings.TrimSpace(part.ToolName),
			Input:             part.Input,
			Approval:          extractApprovalMetadata(part.ProviderMetadata),
			ExecutionLocation: extractExecutionLocationMetadata(part.ProviderMetadata),
			UserInput:         extractUserInputMetadata(part.ProviderMetadata),
		})
	}
	if len(calls) > 0 {
		return calls
	}

	for _, toolCall := range message.ToolCalls {
		input := any(nil)
		if rawArgs := strings.TrimSpace(toolCall.Function.Arguments); rawArgs != "" {
			if err := json.Unmarshal([]byte(rawArgs), &input); err != nil {
				input = rawArgs
			}
		}
		calls = append(calls, uiExtractedToolCall{
			ID:    strings.TrimSpace(toolCall.ID),
			Name:  strings.TrimSpace(toolCall.Function.Name),
			Input: input,
		})
	}
	return calls
}

func extractApprovalMetadata(metadata map[string]any) *UIToolApproval {
	if metadata == nil {
		return nil
	}
	raw, ok := metadata["approval"]
	if !ok {
		return nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	approvalID, _ := obj["approval_id"].(string)
	approvalID = strings.TrimSpace(approvalID)
	if approvalID == "" {
		return nil
	}
	status, _ := obj["status"].(string)
	status = strings.TrimSpace(status)
	if status == "" {
		status = "pending"
	}
	return &UIToolApproval{
		ApprovalID:     approvalID,
		ShortID:        intFromAny(obj["short_id"]),
		Status:         status,
		DecisionReason: stringFromAny(obj["decision_reason"]),
		CanApprove:     boolFromAny(obj["can_approve"], true),
	}
}

func extractExecutionLocationMetadata(metadata map[string]any) *UIExecutionLocation {
	if metadata == nil {
		return nil
	}
	raw, ok := metadata["execution_location"]
	if !ok {
		return nil
	}
	location := &UIExecutionLocation{}
	if obj, ok := raw.(map[string]any); ok {
		location.Kind = stringFromAny(obj["kind"])
		location.Name = stringFromAny(obj["name"])
	} else {
		encoded, err := json.Marshal(raw)
		if err != nil || json.Unmarshal(encoded, location) != nil {
			return nil
		}
	}
	if location.Kind == "" || location.Name == "" {
		return nil
	}
	return location
}

func extractUserInputMetadata(metadata map[string]any) *UIUserInput {
	if metadata == nil {
		return nil
	}
	raw, ok := metadata["user_input"]
	if !ok {
		return nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	userInputID := stringFromAny(obj["user_input_id"])
	if userInputID == "" {
		return nil
	}
	status := stringFromAny(obj["status"])
	if status == "" {
		status = "pending"
	}
	return uiUserInputFromPayload(
		userInputID,
		intFromAny(obj["short_id"]),
		status,
		obj["ui_payload"],
		obj["answers"],
		status == "pending",
	)
}

func intFromAny(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	default:
		return 0
	}
}

func stringFromAny(value any) string {
	if s, ok := value.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func boolFromAny(value any, fallback bool) bool {
	if b, ok := value.(bool); ok {
		return b
	}
	return fallback
}

func extractPersistedToolResults(message *uiDecodedModelMessage) []uiExtractedToolResult {
	parts := message.parts()
	results := make([]uiExtractedToolResult, 0, len(parts))
	for _, part := range parts {
		if !strings.EqualFold(strings.TrimSpace(part.Type), "tool-result") {
			continue
		}
		output := part.Output
		if output == nil {
			output = part.Result
		}
		results = append(results, uiExtractedToolResult{
			ToolCallID: strings.TrimSpace(part.ToolCallID),
			Output:     output,
		})
	}
	if len(results) > 0 {
		return results
	}

	if strings.TrimSpace(message.ToolCallID) == "" {
		return nil
	}

	return []uiExtractedToolResult{{
		ToolCallID: strings.TrimSpace(message.ToolCallID),
		Output:     message.decodedToolResultOutput(),
	}}
}

func (message *uiDecodedModelMessage) decodedToolResultOutput() any {
	if message == nil {
		return nil
	}
	if message.toolResultOutputDecoded {
		return message.toolResultOutput
	}
	message.toolResultOutputDecoded = true

	var output any
	if err := json.Unmarshal(message.Content, &output); err != nil {
		output = strings.TrimSpace(string(message.Content))
	}
	message.toolResultOutput = output
	return message.toolResultOutput
}

func (message *uiDecodedModelMessage) parts() []uiContentPart {
	if message == nil || len(message.Content) == 0 {
		return nil
	}
	if message.contentPartsDecoded {
		return message.contentParts
	}
	message.contentParts = extractPersistedContentParts(message.Content)
	message.contentPartsDecoded = true
	return message.contentParts
}

func extractPersistedContentParts(raw json.RawMessage) []uiContentPart {
	if len(raw) == 0 {
		return nil
	}

	switch firstJSONByte(raw) {
	case '[':
		var parts []uiContentPart
		if err := json.Unmarshal(raw, &parts); err == nil {
			return parts
		}
	case '"':
		var encoded string
		if err := json.Unmarshal(raw, &encoded); err == nil {
			trimmed := strings.TrimSpace(encoded)
			if strings.HasPrefix(trimmed, "[") {
				var parts []uiContentPart
				if json.Unmarshal([]byte(trimmed), &parts) == nil {
					return parts
				}
			}
		}
	case '{':
		var object struct {
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(raw, &object); err == nil && len(object.Content) > 0 {
			return extractPersistedContentParts(object.Content)
		}
	}

	return nil
}

func firstJSONByte(raw json.RawMessage) byte {
	for _, b := range raw {
		switch b {
		case ' ', '\n', '\r', '\t':
			continue
		default:
			return b
		}
	}
	return 0
}

func uiAttachmentsFromMessageAssets(raw messagepkg.Message) []UIAttachment {
	if len(raw.Assets) == 0 {
		return nil
	}

	attachments := make([]UIAttachment, 0, len(raw.Assets))
	for _, asset := range raw.Assets {
		attachments = append(attachments, UIAttachment{
			ID:          strings.TrimSpace(asset.ContentHash),
			Type:        normalizeUIAttachmentType("", asset.Mime),
			Name:        strings.TrimSpace(asset.Name),
			ContentHash: strings.TrimSpace(asset.ContentHash),
			BotID:       strings.TrimSpace(raw.BotID),
			Mime:        strings.TrimSpace(asset.Mime),
			Size:        asset.SizeBytes,
			StorageKey:  strings.TrimSpace(asset.StorageKey),
			Metadata:    asset.Metadata,
		})
	}
	return attachments
}

func uiUserMessageKind(raw messagepkg.Message) string {
	return stringFromAny(raw.Metadata["user_message_kind"])
}

func uiSkillActivationFromMessage(raw messagepkg.Message) *turn.SkillActivation {
	explicitKind := uiUserMessageKind(raw) == turn.UserMessageKindSkillActivation
	meta, _ := raw.Metadata["skill_activation"].(map[string]any)
	if !explicitKind && len(meta) == 0 && raw.Metadata["model_requested_skills"] == nil {
		return nil
	}

	activation := turn.SkillActivation{
		Prompt: stringFromAny(meta["prompt"]),
	}
	activation.Skills = append(activation.Skills, skillActivationMetadataSkills(meta["skills"])...)
	if len(activation.Skills) == 0 {
		activation.Skills = append(activation.Skills, skillActivationMetadataSkills(raw.Metadata["model_requested_skills"])...)
	}
	if len(activation.Skills) == 0 && activation.Prompt == "" {
		return nil
	}
	return &activation
}

func skillActivationMetadataSkills(value any) []turn.SkillActivationSkill {
	var skills []turn.SkillActivationSkill
	for _, item := range skillActivationMetadataItems(value) {
		name := stringFromAny(item["name"])
		if name == "" {
			continue
		}
		skills = append(skills, turn.SkillActivationSkill{
			Name:        name,
			DisplayName: stringFromAny(item["display_name"]),
			Description: stringFromAny(item["description"]),
			SourceKind:  stringFromAny(item["source_kind"]),
			State:       stringFromAny(item["state"]),
		})
	}
	return skills
}

func skillActivationMetadataItems(value any) []map[string]any {
	switch items := value.(type) {
	case []any:
		result := make([]map[string]any, 0, len(items))
		for _, raw := range items {
			if item, ok := raw.(map[string]any); ok {
				result = append(result, item)
				continue
			}
			if name, ok := raw.(string); ok && strings.TrimSpace(name) != "" {
				result = append(result, map[string]any{"name": name})
			}
		}
		return result
	case []map[string]any:
		return items
	case []string:
		result := make([]map[string]any, 0, len(items))
		for _, name := range items {
			if strings.TrimSpace(name) != "" {
				result = append(result, map[string]any{"name": name})
			}
		}
		return result
	default:
		return nil
	}
}

func uiReplyFromMessage(raw messagepkg.Message) *UIReplyRef {
	reply := UIReplyRef{MessageID: strings.TrimSpace(raw.SourceReplyToMessageID)}
	if meta, ok := raw.Metadata["reply"].(map[string]any); ok {
		if v, ok := meta["message_id"].(string); ok && strings.TrimSpace(v) != "" {
			reply.MessageID = strings.TrimSpace(v)
		}
		if v, ok := meta["sender"].(string); ok {
			reply.Sender = strings.TrimSpace(v)
		}
		if v, ok := meta["preview"].(string); ok {
			reply.Preview = truncateUIReplyPreview(v)
		}
		reply.Attachments = uiAttachmentsFromReplyMetadata(meta["attachments"], raw.BotID)
	}
	if reply.MessageID == "" && reply.Sender == "" && reply.Preview == "" && len(reply.Attachments) == 0 {
		return nil
	}
	return &reply
}

func uiAttachmentsFromReplyMetadata(value any, botID string) []UIAttachment {
	rawItems := replyAttachmentMetadataItems(value)
	if len(rawItems) == 0 {
		return nil
	}
	attachments := make([]UIAttachment, 0, len(rawItems))
	for _, item := range rawItems {
		att := UIAttachment{
			Type:        normalizeUIAttachmentType(stringFromAny(item["type"]), stringFromAny(item["mime"])),
			Path:        stringFromAny(item["path"]),
			URL:         stringFromAny(item["url"]),
			Base64:      stringFromAny(item["base64"]),
			Name:        stringFromAny(item["name"]),
			ContentHash: stringFromAny(item["content_hash"]),
			BotID:       strings.TrimSpace(botID),
			Mime:        stringFromAny(item["mime"]),
			Size:        int64FromAny(item["size"]),
			StorageKey:  stringFromAny(item["storage_key"]),
		}
		if meta, ok := item["metadata"].(map[string]any); ok {
			att.Metadata = meta
			if att.BotID == "" {
				att.BotID = stringFromAny(meta["bot_id"])
			}
			if att.StorageKey == "" {
				att.StorageKey = stringFromAny(meta["storage_key"])
			}
		}
		if att.Type == "" {
			att.Type = "file"
		}
		attachments = append(attachments, att)
	}
	if len(attachments) == 0 {
		return nil
	}
	return attachments
}

func replyAttachmentMetadataItems(value any) []map[string]any {
	switch items := value.(type) {
	case []any:
		result := make([]map[string]any, 0, len(items))
		for _, raw := range items {
			if item, ok := raw.(map[string]any); ok {
				result = append(result, item)
			}
		}
		return result
	case []map[string]any:
		return items
	default:
		return nil
	}
}

func truncateUIReplyPreview(value string) string {
	return textutil.TruncateRunesWithSuffix(strings.TrimSpace(value), uiReplyPreviewMaxRunes, "...")
}

func uiForwardFromMessage(raw messagepkg.Message) *UIForwardRef {
	meta, ok := raw.Metadata["forward"].(map[string]any)
	if !ok {
		return nil
	}
	forward := UIForwardRef{}
	if v, ok := meta["message_id"].(string); ok {
		forward.MessageID = strings.TrimSpace(v)
	}
	if v, ok := meta["from_user_id"].(string); ok {
		forward.FromUserID = strings.TrimSpace(v)
	}
	if v, ok := meta["from_conversation_id"].(string); ok {
		forward.FromConversationID = strings.TrimSpace(v)
	}
	if v, ok := meta["sender"].(string); ok {
		forward.Sender = strings.TrimSpace(v)
	}
	forward.Date = int64FromAny(meta["date"])
	if forward.MessageID == "" && forward.FromUserID == "" && forward.FromConversationID == "" && forward.Sender == "" && forward.Date == 0 {
		return nil
	}
	return &forward
}

func int64FromAny(value any) int64 {
	switch v := value.(type) {
	case int:
		return int64(v)
	case int32:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	default:
		return 0
	}
}

func resolveUIPersistencePlatform(raw messagepkg.Message) string {
	direct := strings.ToLower(strings.TrimSpace(raw.Platform))
	if direct == "local" {
		return ""
	}
	if direct != "" {
		return direct
	}

	if raw.Metadata != nil {
		if platform, ok := raw.Metadata["platform"].(string); ok {
			trimmed := strings.ToLower(strings.TrimSpace(platform))
			if trimmed == "local" {
				return ""
			}
			return trimmed
		}
	}
	return ""
}

func stripPersistedYAMLHeader(text string) string {
	if !strings.HasPrefix(text, "---\n") {
		return text
	}
	return strings.TrimSpace(uiMessageYAMLHeaderRe.ReplaceAllString(text, ""))
}

func stripPersistedAgentTags(text string) string {
	if !strings.Contains(text, "<") {
		trimmed := strings.TrimSpace(text)
		if !strings.Contains(trimmed, "\n\n\n") {
			return trimmed
		}
		return strings.TrimSpace(uiMessageCollapsedNewlinesRe.ReplaceAllString(trimmed, "\n\n"))
	}
	stripped := uiMessageAgentTagsRe.ReplaceAllString(text, "")
	return strings.TrimSpace(uiMessageCollapsedNewlinesRe.ReplaceAllString(stripped, "\n\n"))
}

func applyToolResultToUIMessage(message *UIMessage, output any) {
	if message == nil {
		return
	}
	message.Output = output
	// Finalize before any early return below: a background handoff result is
	// still a tool result, and replaying a pending user input would re-offer
	// an already answered form.
	finalizeInteractiveRequests(message, output)
	if task, ok := backgroundTaskFromToolResult(output); ok {
		if task.Command == "" {
			task.Command = stringFromMap(message.Input, "command")
		}
		mergeBackgroundTaskIntoTool(message, task)
		return
	}
	message.Running = uiBoolPtr(false)
}

// finalizeInteractiveRequests closes any pending ask_user state on a tool
// block once its result exists. Every exit path of result application must go
// through this, or stale pending requests resurface in the UI.
func finalizeInteractiveRequests(message *UIMessage, output any) {
	if message.UserInput != nil {
		if payload, ok := toolResultMap(output); ok {
			if status := stringFromMap(payload, "status"); status != "" {
				message.UserInput.Status = status
			}
		}
		message.UserInput.CanRespond = false
	}
}

// backgroundTaskFromToolResult detects a background handoff in a tool result
// by payload shape — a task_id plus a background-start status marker —
// regardless of which tool produced it. Terminal statuses from inspection
// tools intentionally do not match.
func backgroundTaskFromToolResult(output any) (UIBackgroundTask, bool) {
	payload, ok := toolResultMap(output)
	if !ok {
		return UIBackgroundTask{}, false
	}

	taskID := stringFromMap(payload, "task_id")
	if taskID == "" {
		return UIBackgroundTask{}, false
	}

	status := strings.ToLower(strings.TrimSpace(stringFromMap(payload, "status")))
	switch status {
	case "background_started", "auto_backgrounded", "started", "queued":
	default:
		return UIBackgroundTask{}, false
	}

	task := UIBackgroundTask{
		TaskID:         taskID,
		Status:         normalizeBackgroundTaskStatus(status),
		Command:        firstNonEmptyString(stringFromMap(payload, "command"), stringFromMap(payload, "description"), stringFromMap(payload, "message")),
		AgentID:        stringFromMap(payload, "agent_id"),
		AgentSessionID: stringFromMap(payload, "agent_session_id", "session_id"),
		OutputFile:     stringFromMap(payload, "output_file"),
		OutputTail:     firstNonEmptyString(stringFromMap(payload, "output_tail"), stringFromMap(payload, "tail")),
	}
	if task.Status == "" {
		task.Status = "running"
	}
	return task, true
}

func mergeBackgroundTaskIntoTool(message *UIMessage, task UIBackgroundTask) {
	if message == nil {
		return
	}
	merged := UIBackgroundTask{}
	if message.Background != nil {
		merged = *message.Background
	}
	if task.TaskID != "" {
		merged.TaskID = task.TaskID
	}
	if task.Status != "" {
		merged.Status = normalizeBackgroundTaskStatus(task.Status)
		if merged.Status == "" {
			merged.Status = task.Status
		}
	}
	if task.Command != "" {
		merged.Command = task.Command
	}
	if task.AgentID != "" {
		merged.AgentID = task.AgentID
	}
	if task.AgentSessionID != "" {
		merged.AgentSessionID = task.AgentSessionID
	}
	if task.OutputFile != "" {
		merged.OutputFile = task.OutputFile
	}
	if task.ExitCode != 0 || isBackgroundTerminalStatus(task.Status) {
		merged.ExitCode = task.ExitCode
	}
	if task.Duration != "" {
		merged.Duration = task.Duration
	}
	if task.OutputTail != "" {
		merged.OutputTail = task.OutputTail
	}
	if task.Stream != "" {
		merged.Stream = task.Stream
	}
	if task.Chunk != "" {
		merged.Chunk = task.Chunk
	}
	if task.Stalled {
		merged.Stalled = true
	}
	if merged.Status == "" {
		merged.Status = "running"
	}
	message.Background = &merged
	message.Running = uiBoolPtr(isBackgroundToolStillRunning(*message))
}

func isBackgroundToolStillRunning(message UIMessage) bool {
	if message.Type != UIMessageTool || message.Background == nil {
		return false
	}
	status := normalizeBackgroundTaskStatus(message.Background.Status)
	return status == "running" || status == "queued" || status == "stalled"
}

func parseBackgroundTaskNotification(text string) (UIBackgroundTask, bool) {
	if !strings.Contains(text, "<task-notification>") {
		return UIBackgroundTask{}, false
	}
	match := uiTaskNotificationRe.FindStringSubmatch(text)
	if len(match) < 2 {
		return UIBackgroundTask{}, false
	}
	body := match[1]
	taskID := strings.TrimSpace(extractUITaskNotificationTag(body, "task-id"))
	if taskID == "" {
		return UIBackgroundTask{}, false
	}

	status := normalizeBackgroundTaskStatus(extractUITaskNotificationTag(body, "status"))
	if status == "" {
		status = "completed"
	}
	task := UIBackgroundTask{
		TaskID: taskID,
		Status: status,
		Command: firstNonEmptyString(
			strings.TrimSpace(extractUITaskNotificationTag(body, "command")),
			strings.TrimSpace(extractUITaskNotificationTag(body, "description")),
			strings.TrimSpace(extractUITaskNotificationTag(body, "message")),
		),
		AgentID:        strings.TrimSpace(extractUITaskNotificationTag(body, "agent-id")),
		AgentSessionID: strings.TrimSpace(extractUITaskNotificationTag(body, "session-id")),
		OutputFile:     strings.TrimSpace(extractUITaskNotificationTag(body, "output-file")),
		Duration:       strings.TrimSpace(extractUITaskNotificationTag(body, "duration")),
		OutputTail: firstNonEmptyString(
			strings.TrimSpace(extractUITaskNotificationTag(body, "output-tail")),
			strings.TrimSpace(extractUITaskNotificationTag(body, "report")),
			strings.TrimSpace(extractUITaskNotificationTag(body, "error")),
		),
		Stalled: status == "stalled",
	}
	if rawExitCode := strings.TrimSpace(extractUITaskNotificationTag(body, "exit-code")); rawExitCode != "" {
		if exitCode, err := strconv.ParseInt(rawExitCode, 10, 32); err == nil {
			task.ExitCode = int32(exitCode)
		}
	}
	return task, true
}

func extractUITaskNotificationTag(body, tag string) string {
	open := "<" + tag + ">"
	closeTag := "</" + tag + ">"
	start := strings.Index(body, open)
	if start < 0 {
		return ""
	}
	start += len(open)
	end := strings.Index(body[start:], closeTag)
	if end < 0 {
		return ""
	}
	return body[start : start+end]
}

func toolResultMap(output any) (map[string]any, bool) {
	typed, ok := output.(map[string]any)
	if !ok {
		if raw, rawOK := output.(json.RawMessage); rawOK {
			var decoded map[string]any
			if err := json.Unmarshal(raw, &decoded); err == nil {
				typed = decoded
				ok = true
			}
		}
		if !ok {
			if text, textOK := output.(string); textOK {
				var decoded map[string]any
				if err := json.Unmarshal([]byte(text), &decoded); err == nil {
					typed = decoded
					ok = true
				}
			}
		}
	}
	if !ok {
		return nil, false
	}

	for _, key := range []string{"structuredContent", "structured_content"} {
		if nested, nestedOK := typed[key].(map[string]any); nestedOK {
			return nested, true
		}
	}
	return typed, true
}

func stringFromMap(value any, keys ...string) string {
	typed, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	for _, key := range keys {
		if value := stringFromAny(typed[key]); value != "" {
			return value
		}
	}
	return ""
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func normalizeBackgroundTaskStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "background_started", "auto_backgrounded", "started", "running":
		return "running"
	case "queued", "queue":
		return "queued"
	case "completed", "complete", "success", "succeeded":
		return "completed"
	case "failed", "failure", "error":
		return "failed"
	case "stalled":
		return "stalled"
	case "killed", "cancelled", "canceled":
		return "killed"
	default:
		return ""
	}
}

func isBackgroundTerminalStatus(status string) bool {
	switch normalizeBackgroundTaskStatus(status) {
	case "completed", "failed", "killed":
		return true
	default:
		return false
	}
}
