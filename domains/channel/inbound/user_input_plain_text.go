package inbound

import (
	"context"
	"fmt"
	"strings"

	agentdomain "github.com/memohai/memoh/domains/agent"
	userinput "github.com/memohai/memoh/domains/agent/decision/input"
	"github.com/memohai/memoh/domains/channel/gateway"
	"github.com/memohai/memoh/internal/i18n"
)

// handlePlainTextUserInput is the universal fallback for channels that do not
// own a native ask_user interaction. In groups it only consumes messages that
// explicitly target the bot; private conversations consume the next reply.
func (p *ChannelInboundProcessor) handlePlainTextUserInput(
	ctx context.Context,
	msg gateway.InboundMessage,
	sender gateway.StreamReplySender,
	identity InboundIdentity,
	routeID string,
	sessionID string,
	text string,
) (bool, error) {
	if p.channelCaps(msg.Channel).NativeUserInput || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(text) == "" || !isDirectedAtBot(msg) {
		return false, nil
	}
	if p.turnSvc == nil {
		return false, nil
	}
	advanceRunner := PlainTextUserInputRunner(p.turnSvc)
	responseRunner := UserInputRunner(p.turnSvc)
	replyExternalID := ""
	if msg.Message.Reply != nil {
		replyExternalID = strings.TrimSpace(msg.Message.Reply.MessageID)
	}
	result, err := advanceRunner.AdvancePlainTextUserInput(ctx, agentdomain.AdvanceTextInput{
		BotID:                  strings.TrimSpace(identity.BotID),
		SessionID:              strings.TrimSpace(sessionID),
		ReplyExternalMessageID: replyExternalID,
		Text:                   text,
	})
	if err != nil || !result.Handled {
		return result.Handled, err
	}
	loc := p.localizer(ctx, identity.BotID)
	if !result.Request.Interaction.Completed {
		return true, sender.Send(ctx, gateway.OutboundMessage{
			Target: strings.TrimSpace(msg.ReplyTarget),
			Message: plainTextUserInputMessage(
				result.Request,
				result.Invalid,
				loc,
				strings.TrimSpace(msg.Message.ID),
			),
		})
	}
	if err := sender.Send(ctx, gateway.OutboundMessage{
		Target:  strings.TrimSpace(msg.ReplyTarget),
		Message: plainTextUserInputSummary(result.Request, loc, strings.TrimSpace(msg.Message.ID)),
	}); err != nil {
		return true, err
	}
	return true, p.streamUserInputResponseCommand(ctx, msg, sender, identity, routeID, responseRunner, agentdomain.UserInputResponse{
		BotID:                  strings.TrimSpace(identity.BotID),
		ThreadID:               strings.TrimSpace(sessionID),
		ActorChannelIdentityID: strings.TrimSpace(identity.ChannelIdentityID),
		ActorUserID:            strings.TrimSpace(identity.UserID),
		ExplicitID:             result.Request.ID,
		Answers:                append([]agentdomain.QuestionAnswer(nil), result.Request.Interaction.Answers...),
		ChatToken:              p.issueChatToken(identity, routeID, msg),
	})
}

func plainTextUserInputMessage(req agentdomain.AdvanceTextRequest, invalid bool, loc *i18n.Localizer, replyMessageID string) gateway.Message {
	questions := req.UIPayload.Questions
	index := req.Interaction.QuestionIndex
	if index < 0 || index >= len(questions) {
		return gateway.Message{}
	}
	question := questions[index]
	lines := make([]string, 0, len(question.Options)+8)
	if invalid {
		lines = append(lines, loc.T("cmd.userInput.invalid"), "")
	}
	if len(questions) > 1 {
		lines = append(lines, fmt.Sprintf("%d/%d", index+1, len(questions)), "")
	}
	lines = append(lines, question.Text)
	for optionIndex, option := range question.Options {
		line := fmt.Sprintf("%d. %s", optionIndex+1, option.Label)
		if description := strings.TrimSpace(option.Description); description != "" {
			line += " - " + description
		}
		lines = append(lines, line)
	}
	lines = append(lines, "")
	switch question.Kind {
	case userinput.QuestionKindMultiSelect:
		lines = append(lines, loc.T("cmd.userInput.multiHint"))
	case userinput.QuestionKindText:
		lines = append(lines, loc.T("cmd.userInput.textHint"))
	default:
		lines = append(lines, loc.T("cmd.userInput.selectHint"))
	}
	lines = append(lines, loc.T("cmd.userInput.skipHint"))
	if index > 0 {
		lines = append(lines, loc.T("cmd.userInput.backHint"))
	}
	return replyTextMessage(strings.Join(lines, "\n"), replyMessageID)
}

func plainTextUserInputSummary(req agentdomain.AdvanceTextRequest, loc *i18n.Localizer, replyMessageID string) gateway.Message {
	answers := make(map[string]agentdomain.QuestionAnswer, len(req.Interaction.Answers))
	for _, answer := range req.Interaction.Answers {
		answers[answer.QuestionID] = answer
	}
	blocks := make([]string, 0, len(req.UIPayload.Questions))
	for index, question := range req.UIPayload.Questions {
		answer := answers[question.ID]
		value := plainTextAnswerLabel(question, answer, loc)
		blocks = append(blocks, fmt.Sprintf("%d. %s\n%s: %s", index+1, question.Text, loc.T("cmd.userInput.answerLabel"), value))
	}
	return replyTextMessage(strings.Join(blocks, "\n\n"), replyMessageID)
}

func plainTextAnswerLabel(question agentdomain.AdvanceTextUIQuestion, answer agentdomain.QuestionAnswer, loc *i18n.Localizer) string {
	if answer.Skipped {
		return loc.T("cmd.userInput.skipped")
	}
	if text := strings.TrimSpace(answer.Text); text != "" {
		return text
	}
	labels := make([]string, 0, len(answer.OptionIDs)+1)
	for _, optionID := range answer.OptionIDs {
		for _, option := range question.Options {
			if option.ID == optionID {
				labels = append(labels, option.Label)
				break
			}
		}
	}
	if custom := strings.TrimSpace(answer.CustomText); custom != "" {
		labels = append(labels, custom)
	}
	return strings.Join(labels, ", ")
}

func replyTextMessage(text string, replyMessageID string) gateway.Message {
	message := gateway.Message{Text: strings.TrimSpace(text)}
	if replyMessageID != "" {
		message.Reply = &gateway.ReplyRef{MessageID: replyMessageID}
	}
	return message
}

func isUserInputEvent(event *gateway.StreamEvent) bool {
	return event != nil && event.Type == gateway.StreamEventToolCallStart && event.ToolCall != nil && hasUserInputAction(event.ToolCall.Actions)
}
