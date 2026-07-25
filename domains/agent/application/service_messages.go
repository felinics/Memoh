package application

import (
	"strings"

	sdk "github.com/memohai/twilight-ai/sdk"

	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/agent/chat/convert"
)

// sdkMessagesToModelMessages converts SDK messages to the persistence/API format
// for resolver call sites using the shared conversion helper.
func sdkMessagesToModelMessages(msgs []sdk.Message) []agentdomain.ModelMessage {
	return convert.SDKMessagesToModelMessages(msgs)
}

// modelMessageToSDKMessage converts a persistence format message to SDK message
// at the resolver boundary using sdk.Message's native JSON deserialization.
func modelMessageToSDKMessage(mm agentdomain.ModelMessage) sdk.Message {
	return convert.ModelMessageToSDKMessage(mm)
}

// prependUserMessage prepends the user query as a agentdomain.ModelMessage to the output
// messages from the agent. The SDK only returns output messages (assistant + tool);
// user messages must be added back at the resolver boundary for persistence.
func prependUserMessage(query string, output []agentdomain.ModelMessage) []agentdomain.ModelMessage {
	return convert.PrependUserMessage(query, output)
}

func prependTurnUserMessage(req ChatRequest, output []agentdomain.ModelMessage) []agentdomain.ModelMessage {
	if strings.TrimSpace(req.Query) == "" && req.UserMessageKind != agentdomain.UserMessageKindSkillActivation {
		return output
	}
	round := make([]agentdomain.ModelMessage, 0, 1+len(output))
	round = append(round, agentdomain.ModelMessage{
		Role:    "user",
		Content: agentdomain.NewTextContent(req.Query),
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
func modelMessagesToSDKMessages(msgs []agentdomain.ModelMessage) []sdk.Message {
	return convert.ModelMessagesToSDKMessages(msgs)
}
