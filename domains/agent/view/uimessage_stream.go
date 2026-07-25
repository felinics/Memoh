package view

import (
	"encoding/json"
	"strings"
)

type uiTextStreamState struct {
	ID      int
	Content string
}

type uiToolStreamState struct {
	Message UIMessage
}

// uiEmittedBlock records one block ID allocation in live-stream order so the
// terminal snapshot replay can line its blocks up with the ones the client
// already rendered (block order on the client is the ID order).
type uiEmittedBlock struct {
	Kind       UIMessageType
	ToolCallID string
	ID         int
	// TagOnly marks a text block whose entire content is inline agent tags
	// (<attachments>/<reactions>/<speech>). The terminal snapshot strips those
	// tags and drops empty text blocks, so such a live block has no terminal
	// counterpart and must be skipped during positional matching — otherwise it
	// shifts the alignment and the final reply overwrites the wrong block.
	TagOnly bool
}

// UIMessageStreamConverter converts low-level stream events into complete UI messages.
type UIMessageStreamConverter struct {
	nextID    int
	text      *uiTextStreamState
	reasoning *uiTextStreamState
	tools     map[string]*uiToolStreamState
	emitted   []uiEmittedBlock
}

// NewUIMessageStreamConverter creates a new UI stream converter.
func NewUIMessageStreamConverter() *UIMessageStreamConverter {
	return &UIMessageStreamConverter{
		tools: map[string]*uiToolStreamState{},
	}
}

// HandleEvent updates converter state and returns zero or one complete UI messages.
func (c *UIMessageStreamConverter) HandleEvent(event UIMessageStreamEvent) []UIMessage {
	switch strings.ToLower(strings.TrimSpace(event.Type)) {
	case "retry":
		// A retried attempt regenerates its output from scratch, so the terminal
		// snapshot will contain only the surviving attempt's messages. Drop the
		// bookkeeping for the discarded attempt (including the emitted-block log)
		// so ConvertTerminalMessages aligns against the surviving attempt's
		// blocks only — otherwise an orphaned pre-retry text/reasoning block
		// shifts the positional match and the final reply overwrites the wrong
		// block. IDs (c.nextID) keep advancing so re-emitted blocks stay unique.
		c.text = nil
		c.reasoning = nil
		c.tools = map[string]*uiToolStreamState{}
		c.emitted = nil
		return nil

	case "text_start":
		c.text = &uiTextStreamState{ID: c.allocBlockID(UIMessageText, "")}
		return nil

	case "text_delta":
		if c.text == nil {
			c.text = &uiTextStreamState{ID: c.allocBlockID(UIMessageText, "")}
		}
		c.text.Content += event.Delta
		return []UIMessage{{
			ID:      c.text.ID,
			Type:    UIMessageText,
			Content: c.text.Content,
		}}

	case "text_end":
		c.finalizeTextBlock()
		return nil

	case "reasoning_start":
		c.reasoning = &uiTextStreamState{ID: c.allocBlockID(UIMessageReasoning, "")}
		return nil

	case "reasoning_delta":
		if c.reasoning == nil {
			c.reasoning = &uiTextStreamState{ID: c.allocBlockID(UIMessageReasoning, "")}
		}
		c.reasoning.Content += event.Delta
		return []UIMessage{{
			ID:      c.reasoning.ID,
			Type:    UIMessageReasoning,
			Content: c.reasoning.Content,
		}}

	case "reasoning_end":
		c.reasoning = nil
		return nil

	case "tool_call_start", "tool_call_input_start", "tool_call_metadata":
		state := c.findToolState(event.ToolCallID, event.ToolName)
		if state == nil {
			state = &uiToolStreamState{
				Message: UIMessage{
					ID:         c.allocBlockID(UIMessageTool, strings.TrimSpace(event.ToolCallID)),
					Type:       UIMessageTool,
					Name:       strings.TrimSpace(event.ToolName),
					Input:      event.Input,
					ToolCallID: strings.TrimSpace(event.ToolCallID),
					Running:    uiBoolPtr(true),
				},
			}
		}
		if trimmed := strings.TrimSpace(event.ToolName); trimmed != "" {
			state.Message.Name = trimmed
		}
		if event.Input != nil {
			state.Message.Input = event.Input
		}
		applyExecutionLocationMetadata(&state.Message, event.Metadata)
		if trimmed := strings.TrimSpace(event.ToolCallID); trimmed != "" {
			state.Message.ToolCallID = trimmed
			c.tools[trimmed] = state
		}
		state.Message.Running = uiBoolPtr(true)
		c.finalizeTextBlock()
		return []UIMessage{cloneToolStreamMessage(state.Message)}

	case "tool_call_progress":
		state := c.findToolState(event.ToolCallID, event.ToolName)
		if state == nil {
			state = &uiToolStreamState{
				Message: UIMessage{
					ID:         c.allocBlockID(UIMessageTool, strings.TrimSpace(event.ToolCallID)),
					Type:       UIMessageTool,
					Name:       strings.TrimSpace(event.ToolName),
					Input:      event.Input,
					ToolCallID: strings.TrimSpace(event.ToolCallID),
					Running:    uiBoolPtr(true),
				},
			}
			if state.Message.ToolCallID != "" {
				c.tools[state.Message.ToolCallID] = state
			}
		}
		state.Message.Progress = append(state.Message.Progress, event.Progress)
		if event.Input != nil {
			state.Message.Input = event.Input
		}
		applyExecutionLocationMetadata(&state.Message, event.Metadata)
		return []UIMessage{cloneToolStreamMessage(state.Message)}

	case "tool_approval_request":
		state := c.findToolState(event.ToolCallID, event.ToolName)
		if state == nil {
			state = &uiToolStreamState{
				Message: UIMessage{
					ID:         c.allocBlockID(UIMessageTool, strings.TrimSpace(event.ToolCallID)),
					Type:       UIMessageTool,
					Name:       strings.TrimSpace(event.ToolName),
					Input:      event.Input,
					ToolCallID: strings.TrimSpace(event.ToolCallID),
				},
			}
			if state.Message.ToolCallID != "" {
				c.tools[state.Message.ToolCallID] = state
			}
		}
		if event.Input != nil {
			state.Message.Input = event.Input
		}
		applyExecutionLocationMetadata(&state.Message, event.Metadata)
		if trimmed := strings.TrimSpace(event.ToolName); trimmed != "" {
			state.Message.Name = trimmed
		}
		if trimmed := strings.TrimSpace(event.ToolCallID); trimmed != "" {
			state.Message.ToolCallID = trimmed
			c.tools[trimmed] = state
		}
		status := strings.TrimSpace(event.Status)
		if status == "" {
			status = "pending"
		}
		state.Message.Running = uiBoolPtr(false)
		state.Message.Approval = &UIToolApproval{
			ApprovalID: strings.TrimSpace(event.ApprovalID),
			ShortID:    event.ShortID,
			Status:     status,
			CanApprove: strings.EqualFold(status, "pending"),
		}
		return []UIMessage{cloneToolStreamMessage(state.Message)}

	case "user_input_request":
		state := c.findToolState(event.ToolCallID, event.ToolName)
		if state == nil {
			state = &uiToolStreamState{
				Message: UIMessage{
					ID:         c.allocBlockID(UIMessageTool, strings.TrimSpace(event.ToolCallID)),
					Type:       UIMessageTool,
					Name:       strings.TrimSpace(event.ToolName),
					Input:      event.Input,
					ToolCallID: strings.TrimSpace(event.ToolCallID),
				},
			}
			if state.Message.ToolCallID != "" {
				c.tools[state.Message.ToolCallID] = state
			}
		}
		if event.Input != nil {
			state.Message.Input = event.Input
		}
		applyExecutionLocationMetadata(&state.Message, event.Metadata)
		if trimmed := strings.TrimSpace(event.ToolName); trimmed != "" {
			state.Message.Name = trimmed
		}
		if trimmed := strings.TrimSpace(event.ToolCallID); trimmed != "" {
			state.Message.ToolCallID = trimmed
			c.tools[trimmed] = state
		}
		status := strings.TrimSpace(event.Status)
		if status == "" {
			status = "pending"
		}
		userInputID := strings.TrimSpace(event.UserInputID)
		if userInputID == "" {
			userInputID = stringFromAny(event.Metadata["user_input_id"])
		}
		state.Message.Running = uiBoolPtr(false)
		state.Message.UserInput = uiUserInputFromPayload(
			userInputID,
			event.ShortID,
			status,
			event.Metadata["ui_payload"],
			status == "pending",
		)
		return []UIMessage{cloneToolStreamMessage(state.Message)}

	case "tool_call_end":
		state := c.findToolState(event.ToolCallID, event.ToolName)
		if state == nil {
			state = &uiToolStreamState{
				Message: UIMessage{
					ID:         c.allocBlockID(UIMessageTool, strings.TrimSpace(event.ToolCallID)),
					Type:       UIMessageTool,
					Name:       strings.TrimSpace(event.ToolName),
					Input:      event.Input,
					ToolCallID: strings.TrimSpace(event.ToolCallID),
				},
			}
		}
		if event.Input != nil {
			state.Message.Input = event.Input
		}
		applyExecutionLocationMetadata(&state.Message, event.Metadata)
		applyToolResultToUIMessage(&state.Message, event.Output)
		if state.Message.ToolCallID != "" && !isBackgroundToolStillRunning(state.Message) {
			delete(c.tools, state.Message.ToolCallID)
		}
		return []UIMessage{cloneToolStreamMessage(state.Message)}

	case "attachment_delta":
		if len(event.Attachments) == 0 {
			return nil
		}
		return []UIMessage{{
			ID:          c.allocBlockID(UIMessageAttachments, ""),
			Type:        UIMessageAttachments,
			Attachments: append([]UIAttachment(nil), event.Attachments...),
		}}

	default:
		return nil
	}
}

func (c *UIMessageStreamConverter) nextMessageID() int {
	id := c.nextID
	c.nextID++
	return id
}

// allocBlockID allocates the next block ID and records the allocation so
// ConvertTerminalMessages can align the terminal snapshot with the live blocks.
func (c *UIMessageStreamConverter) allocBlockID(kind UIMessageType, toolCallID string) int {
	id := c.nextMessageID()
	c.emitted = append(c.emitted, uiEmittedBlock{Kind: kind, ToolCallID: toolCallID, ID: id})
	return id
}

// finalizeTextBlock closes the active text block, marking its emitted entry as
// tag-only when stripping inline agent tags leaves no visible content (the
// terminal snapshot drops such blocks, so they must not take part in
// positional matching).
func (c *UIMessageStreamConverter) finalizeTextBlock() {
	if c.text == nil {
		return
	}
	if stripPersistedAgentTags(c.text.Content) == "" {
		for i := range c.emitted {
			if c.emitted[i].Kind == UIMessageText && c.emitted[i].ID == c.text.ID {
				c.emitted[i].TagOnly = true
				break
			}
		}
	}
	c.text = nil
}

// ConvertTerminalMessages converts the terminal snapshot into UI messages whose
// IDs line up with the blocks this converter emitted during the live stream.
// The client orders and upserts blocks by ID, and the snapshot regenerates only
// text/reasoning/tool blocks from raw model messages — attachments exist solely
// as stream events. A plain sequential renumbering would therefore shift onto
// the IDs of live attachment blocks and overwrite them at stream end (the
// generated image flashes away, then reappears after the history refresh).
// Instead each snapshot block reuses the ID of its live counterpart — tools are
// matched by tool call ID, text/reasoning positionally within their kind — and
// only blocks without one get fresh IDs.
func (c *UIMessageStreamConverter) ConvertTerminalMessages(raw json.RawMessage) []UIMessage {
	c.finalizeTextBlock()
	blocks := ConvertRawModelMessagesToUIAssistantMessages(raw)
	if len(blocks) == 0 {
		return nil
	}
	consumed := make([]bool, len(c.emitted))
	for i := range blocks {
		if id, ok := c.reuseEmittedBlockID(blocks[i].Type, blocks[i].ToolCallID, consumed); ok {
			blocks[i].ID = id
			continue
		}
		blocks[i].ID = c.nextMessageID()
	}
	return blocks
}

func (c *UIMessageStreamConverter) reuseEmittedBlockID(kind UIMessageType, toolCallID string, consumed []bool) (int, bool) {
	toolCallID = strings.TrimSpace(toolCallID)
	if kind == UIMessageTool && toolCallID != "" {
		for i, blk := range c.emitted {
			if !consumed[i] && blk.Kind == kind && blk.ToolCallID == toolCallID {
				consumed[i] = true
				return blk.ID, true
			}
		}
	}
	for i, blk := range c.emitted {
		if consumed[i] || blk.Kind != kind || blk.TagOnly {
			continue
		}
		if kind == UIMessageTool && blk.ToolCallID != "" && toolCallID != "" && blk.ToolCallID != toolCallID {
			continue
		}
		consumed[i] = true
		return blk.ID, true
	}
	return 0, false
}

func (c *UIMessageStreamConverter) findToolState(toolCallID, toolName string) *uiToolStreamState {
	if trimmed := strings.TrimSpace(toolCallID); trimmed != "" {
		if state, ok := c.tools[trimmed]; ok {
			return state
		}
		// An explicit but unknown tool_call_id means this is a new call,
		// not a continuation of an in-flight one. Falling back to a
		// name-based match here would merge unrelated calls of the same
		// tool (e.g. three sequential `search` invocations) into one UI
		// message, which is exactly what we want to avoid.
		return nil
	}

	normalizedName := strings.TrimSpace(toolName)
	for _, state := range c.tools {
		if strings.TrimSpace(state.Message.Name) == normalizedName {
			return state
		}
	}
	return nil
}

func cloneToolStreamMessage(message UIMessage) UIMessage {
	clone := message
	if message.ExecutionLocation != nil {
		location := *message.ExecutionLocation
		clone.ExecutionLocation = &location
	}
	if len(message.Progress) > 0 {
		clone.Progress = append([]any(nil), message.Progress...)
	}
	return clone
}

func applyExecutionLocationMetadata(message *UIMessage, metadata map[string]any) {
	if message == nil {
		return
	}
	if location := extractExecutionLocationMetadata(metadata); location != nil {
		message.ExecutionLocation = location
	}
}
