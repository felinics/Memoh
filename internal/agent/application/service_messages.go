package application

import (
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
)

// sdkMessagesToModelMessages converts SDK messages to the persistence/API format
// for resolver call sites using the shared conversion helper. File parts are
// redacted to text placeholders first — document bytes never enter
// bot_history_messages (see redactFilePartsForStorage).
func sdkMessagesToModelMessages(msgs []sdk.Message) []ModelMessage {
	return historyfrag.ToStoredModelMessages(msgs)
}

// redactFilePartsForStorage replaces FileParts with text placeholders before
// persistence. Storing document base64 in message history would bloat rows,
// blow up the length-based token estimator (triggering history trimming), and
// deadlock replay after switching to a model without native file input. The
// file itself stays in the workspace: follow-up turns re-read it via the read
// tool, which re-injects it for whatever model is current.
func redactFilePartsForStorage(msgs []sdk.Message) []sdk.Message {
	return historyfrag.RedactFileParts(msgs)
}

// modelMessageToSDKMessage converts a persistence format message to SDK message
// at the resolver boundary using sdk.Message's native JSON deserialization.
func modelMessageToSDKMessage(mm ModelMessage) sdk.Message {
	return historyfrag.StoredModelMessageToSDKMessage(mm)
}

func prependTurnUserMessage(req ChatRequest, output []ModelMessage) []ModelMessage {
	if strings.TrimSpace(req.Query) == "" && req.UserMessageKind != UserMessageKindSkillActivation {
		return output
	}
	round := make([]ModelMessage, 0, 1+len(output))
	round = append(round, ModelMessage{
		Role:    "user",
		Content: newTextContent(req.Query),
	})
	return append(round, output...)
}

func modelQueryText(req ChatRequest) string {
	if strings.TrimSpace(req.ModelQuery) != "" {
		return req.ModelQuery
	}
	return req.Query
}

// modelMessagesToSDKMessages converts a slice of persistence messages to SDK messages.
func modelMessagesToSDKMessages(msgs []ModelMessage) []sdk.Message {
	return historyfrag.StoredModelMessagesToSDKMessages(msgs)
}
