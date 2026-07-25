package application

import agentdomain "github.com/memohai/memoh/domains/agent"

// chatRequestFromCommand translates the pure-data command into the
// application's request type, field for field. Function- and channel-typed
// fields (InjectCh, OutboundAssetCollector) are wired by StartTurn.
func chatRequestFromCommand(cmd agentdomain.StartTurnCommand) ChatRequest {
	return ChatRequest{
		BotID:                     cmd.BotID,
		ChatID:                    cmd.ChatID,
		ThreadID:                  cmd.ThreadID,
		Token:                     cmd.Token,
		UserID:                    cmd.UserID,
		SourceChannelIdentityID:   cmd.SourceChannelIdentityID,
		DisplayName:               cmd.DisplayName,
		AvatarURL:                 cmd.AvatarURL,
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

func toolApprovalInputFromResponse(in agentdomain.ToolApprovalResponse) ToolApprovalResponseInput {
	return ToolApprovalResponseInput{
		BotID:                      in.BotID,
		ThreadID:                   in.ThreadID,
		ActorChannelIdentityID:     in.ActorChannelIdentityID,
		ActorUserID:                in.ActorUserID,
		ApprovalID:                 in.ApprovalID,
		ExplicitID:                 in.ExplicitID,
		ReplyExternalMessageID:     in.ReplyExternalMessageID,
		Decision:                   in.Decision,
		Reason:                     in.Reason,
		ChatToken:                  in.ChatToken,
		SuppressActivePromptAttach: in.SuppressActivePromptAttach,
	}
}

func userInputInputFromResponse(in agentdomain.UserInputResponse) UserInputResponseInput {
	return UserInputResponseInput{
		BotID:                      in.BotID,
		ThreadID:                   in.ThreadID,
		ActorChannelIdentityID:     in.ActorChannelIdentityID,
		ActorUserID:                in.ActorUserID,
		UserInputID:                in.UserInputID,
		ExplicitID:                 in.ExplicitID,
		ReplyExternalMessageID:     in.ReplyExternalMessageID,
		Answers:                    in.Answers,
		TextAnswer:                 in.TextAnswer,
		Canceled:                   in.Canceled,
		Reason:                     in.Reason,
		ChatToken:                  in.ChatToken,
		SuppressActivePromptAttach: in.SuppressActivePromptAttach,
	}
}
