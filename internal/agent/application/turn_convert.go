package application

import (
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/turn"
)

// chatRequestFromCommand translates the pure-data command into the
// application's request type, field for field. Function- and channel-typed
// fields (InjectCh, OutboundAssetCollector) are wired by StartTurn.
func chatRequestFromCommand(cmd turn.StartTurnCommand) ChatRequest {
	return ChatRequest{
		BotID:                     cmd.BotID,
		ChatID:                    cmd.ChatID,
		ThreadID:                  cmd.ThreadID,
		Token:                     cmd.Token,
		UserID:                    cmd.UserID,
		SourceChannelIdentityID:   cmd.SourceChannelIdentityID,
		DisplayName:               cmd.DisplayName,
		RouteID:                   cmd.RouteID,
		ChatToken:                 cmd.ChatToken,
		ExternalMessageID:         cmd.ExternalMessageID,
		ReplyTarget:               cmd.ReplyTarget,
		ConversationType:          cmd.ConversationType,
		ConversationName:          cmd.ConversationName,
		SourceReplyToMessageID:    cmd.SourceReplyToMessageID,
		ReplySender:               cmd.ReplySender,
		ReplyPreview:              cmd.ReplyPreview,
		ReplyAttachments:          cmd.ReplyAttachments,
		MentionsBot:               cmd.MentionsBot,
		RepliesToBot:              cmd.RepliesToBot,
		ForwardMessageID:          cmd.ForwardMessageID,
		ForwardFromUserID:         cmd.ForwardFromUserID,
		ForwardFromConversationID: cmd.ForwardFromConversationID,
		ForwardSender:             cmd.ForwardSender,
		ForwardDate:               cmd.ForwardDate,
		Query:                     cmd.Query,
		ModelQuery:                cmd.ModelQuery,
		UserMessageKind:           cmd.UserMessageKind,
		UserVisibleText:           cmd.UserVisibleText,
		SkillActivation:           cmd.SkillActivation,
		SkipMemoryExtraction:      cmd.SkipMemoryExtraction,
		SkipTitleGeneration:       cmd.SkipTitleGeneration,
		CurrentChannel:            cmd.CurrentChannel,
		Channels:                  cmd.Channels,
		UserMessagePersisted:      cmd.UserMessagePersisted,
		Attachments:               cmd.Attachments,
		RequestedSkills:           cmd.RequestedSkills,
		EventID:                   cmd.EventID,
		Model:                     cmd.Model,
		ReasoningEffort:           cmd.ReasoningEffort,
		WorkspaceTargetID:         cmd.WorkspaceTargetID,
	}
}

func questionAnswersToUserInput(in []turn.QuestionAnswer) []userinput.QuestionAnswer {
	if in == nil {
		return nil
	}
	out := make([]userinput.QuestionAnswer, len(in))
	for i := range in {
		out[i] = userinput.QuestionAnswer{
			QuestionID: in[i].QuestionID,
			OptionIDs:  in[i].OptionIDs,
			CustomText: in[i].CustomText,
			Text:       in[i].Text,
			Skipped:    in[i].Skipped,
		}
	}
	return out
}

func toolApprovalInputFromResponse(in turn.ToolApprovalResponse) ToolApprovalResponseInput {
	return ToolApprovalResponseInput{
		ControlID:                  in.ControlID,
		BotID:                      in.BotID,
		ThreadID:                   in.ThreadID,
		ActorChannelIdentityID:     in.ActorChannelIdentityID,
		ActorUserID:                in.ActorUserID,
		ApprovalID:                 in.ApprovalID,
		ExplicitID:                 in.ExplicitID,
		ReplyExternalMessageID:     in.ReplyExternalMessageID,
		Decision:                   in.Decision,
		OptionID:                   in.OptionID,
		Reason:                     in.Reason,
		ChatToken:                  in.ChatToken,
		SuppressActivePromptAttach: in.SuppressActivePromptAttach,
	}
}

func userInputInputFromResponse(in turn.UserInputResponse) UserInputResponseInput {
	return UserInputResponseInput{
		ControlID:                  in.ControlID,
		BotID:                      in.BotID,
		ThreadID:                   in.ThreadID,
		ActorChannelIdentityID:     in.ActorChannelIdentityID,
		ActorUserID:                in.ActorUserID,
		UserInputID:                in.UserInputID,
		ExplicitID:                 in.ExplicitID,
		ReplyExternalMessageID:     in.ReplyExternalMessageID,
		Answers:                    questionAnswersToUserInput(in.Answers),
		TextAnswer:                 in.TextAnswer,
		Canceled:                   in.Canceled,
		Reason:                     in.Reason,
		ChatToken:                  in.ChatToken,
		SuppressActivePromptAttach: in.SuppressActivePromptAttach,
	}
}
