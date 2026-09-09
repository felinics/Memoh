package application

import (
	"context"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/sessionmode"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/contextview"
	"github.com/felinics/memoh/internal/hooks"
)

func TestBuildContextFragScopePreservesIMTopology(t *testing.T) {
	t.Parallel()

	scope := buildContextFragScope(ChatRequest{
		BotID:                     "bot-1",
		ChatID:                    "chat-1",
		ThreadID:                  "sess-1",
		SourceChannelIdentityID:   "identity-1",
		DisplayName:               "ignored",
		CurrentChannel:            "telegram",
		ConversationType:          turn.ConversationTypeGroup,
		ConversationName:          "Research Group",
		ReplyTarget:               "group-1",
		ExternalMessageID:         "msg-1",
		EventID:                   "evt-1",
		SourceReplyToMessageID:    "msg-0",
		ReplySender:               "Alice",
		MentionsBot:               true,
		RepliesToBot:              true,
		ForwardMessageID:          "fwd-1",
		ForwardFromUserID:         "user-2",
		ForwardFromConversationID: "source-chat",
		RawQuery:                  "/summarize this",
	}, "Bob", native.SessionContext{})

	if scope.BotID != "bot-1" || scope.ChatID != "chat-1" || scope.SessionID != "sess-1" {
		t.Fatalf("unexpected base scope: %#v", scope)
	}
	if scope.Platform != "telegram" || scope.ConversationType != turn.ConversationTypeGroup || scope.ConversationName != "Research Group" {
		t.Fatalf("unexpected conversation scope: %#v", scope)
	}
	if scope.CurrentMessageID != "msg-1" || scope.EventID != "evt-1" || scope.ReplyToMessageID != "msg-0" {
		t.Fatalf("unexpected message topology: %#v", scope)
	}
	if !scope.MentionsBot || !scope.RepliesToBot {
		t.Fatalf("expected structured directed-at-bot flags in scope: %#v", scope)
	}
	if scope.ForwardMessageID != "fwd-1" || scope.ForwardFromUserID != "user-2" || scope.ForwardFromConversationID != "source-chat" {
		t.Fatalf("unexpected forward topology: %#v", scope)
	}
	if !hasAttention(scope.Attention, contextfrag.AttentionReply) || !hasAttention(scope.Attention, contextfrag.AttentionCommand) {
		t.Fatalf("attention reasons = %#v, want reply and command", scope.Attention)
	}
	if !hasAttention(scope.Attention, contextfrag.AttentionMention) {
		t.Fatalf("attention reasons = %#v, want mention", scope.Attention)
	}
	if hasAttention(scope.Attention, contextfrag.AttentionPassive) {
		t.Fatalf("attention reasons should not include passive when reply/command are present: %#v", scope.Attention)
	}
}

func TestBuildContextFragScopeDoesNotInferDirectedReplyFromAnyReplyID(t *testing.T) {
	t.Parallel()

	scope := buildContextFragScope(ChatRequest{
		BotID:                  "bot-1",
		ChatID:                 "chat-1",
		ThreadID:               "sess-1",
		ConversationType:       turn.ConversationTypeGroup,
		SourceReplyToMessageID: "someone-elses-message",
		Query:                  "thread side comment",
	}, "Bob", native.SessionContext{})

	if scope.ReplyToMessageID != "someone-elses-message" {
		t.Fatalf("reply topology not preserved: %#v", scope)
	}
	if hasAttention(scope.Attention, contextfrag.AttentionReply) || hasAttention(scope.Attention, contextfrag.AttentionMention) {
		t.Fatalf("attention should not infer directed reply/mention without structured flags: %#v", scope.Attention)
	}
	if !hasAttention(scope.Attention, contextfrag.AttentionPassive) {
		t.Fatalf("group reply without directed flags should be passive attention: %#v", scope.Attention)
	}
}

func TestPrepareRunConfigDoesNotDoubleCountPipelineInlineImages(t *testing.T) {
	t.Parallel()

	image := sdk.ImagePart{Image: "data:image/png;base64,abc", MediaType: "image/png"}
	resolver := &Service{}
	currentIndex := 0
	memoryIndex := 1
	cfg := native.RunConfig{
		Messages: []sdk.Message{
			sdk.UserMessage("pipeline current user"),
			sdk.UserMessage("memory recall"),
		},
		InlineImages:                   []sdk.ImagePart{image},
		ContextCurrentUserMessageIndex: &currentIndex,
		ContextMemoryMessageIndex:      &memoryIndex,
	}

	got := resolver.prepareRunConfig(context.Background(), cfg)

	if got.ContextManifest.Counts.Images != 1 {
		t.Fatalf("manifest image count = %d, want only image injected into SDK message: %#v", got.ContextManifest.Counts.Images, got.ContextManifest.Items)
	}
	rendered := contextfrag.Render(got.ContextFrags)
	if len(rendered.InlineImages) != 0 {
		t.Fatalf("rendered inline images = %#v, want images only inside pipeline SDK message", rendered.InlineImages)
	}
	if !messagesContainImage(got.Messages) {
		t.Fatalf("prepared messages do not contain injected image: %#v", got.Messages)
	}
	if got.ContextCurrentUserMessageIndex == nil || *got.ContextCurrentUserMessageIndex != 0 {
		t.Fatalf("current user index = %#v, want pipeline current 0", got.ContextCurrentUserMessageIndex)
	}
	if got.ContextMemoryMessageIndex == nil || *got.ContextMemoryMessageIndex != 1 {
		t.Fatalf("memory index = %#v, want 1", got.ContextMemoryMessageIndex)
	}
	wantMessages := []sdk.Message{
		sdk.UserMessage("pipeline current user"),
		sdk.UserMessage("memory recall", image),
	}
	if !reflect.DeepEqual(got.Messages, wantMessages) {
		t.Fatalf("provider messages changed: got %#v want %#v", got.Messages, wantMessages)
	}
	if len(got.ContextSourceFrags) == 0 {
		t.Fatal("prepared config did not build authoritative source fragments")
	}
}

func TestPrepareRunConfigPreservesPipelineFileAttachmentBytes(t *testing.T) {
	t.Parallel()

	file := sdk.FilePart{
		Data:      "JVBERi0xLjQ=",
		MediaType: "application/pdf",
		Filename:  "report.pdf",
	}
	currentIndex := 0
	cfg := native.RunConfig{
		Messages:                       []sdk.Message{sdk.UserMessage("pipeline current user")},
		InlineAttachments:              []sdk.MessagePart{file},
		ContextCurrentUserMessageIndex: &currentIndex,
	}

	prepared := (&Service{}).prepareRunConfig(context.Background(), cfg)
	got := contextview.ApplyProviderRunConfig(context.Background(), nil, prepared)
	want := []sdk.Message{sdk.UserMessage("pipeline current user", file)}

	if !reflect.DeepEqual(got.Messages, want) {
		t.Fatalf("provider messages changed: got %#v want %#v", got.Messages, want)
	}
}

func TestPrepareRunConfigClearsStaleContextHookText(t *testing.T) {
	t.Parallel()

	resolver := &Service{}
	got := resolver.prepareRunConfig(context.Background(), native.RunConfig{
		Identity:        native.SessionContext{BotID: "bot-1"},
		ContextHookText: "[Hook Context: BeforePromptBuild]\nstale text",
	})
	if got.ContextHookText != "" {
		t.Fatalf("ContextHookText = %q, want stale carrier cleared", got.ContextHookText)
	}
	for _, frag := range got.ContextSourceFrags {
		if frag.Kind == contextfrag.KindHookContext {
			t.Fatalf("stale hook fragment survived prepareRunConfig: %#v", frag)
		}
	}
}

func TestNormalizeContextMessagesRemapsCurrentAndMemory(t *testing.T) {
	t.Parallel()

	webCall := sdkMessagesToModelMessages([]sdk.Message{{
		Role: sdk.MessageRoleAssistant,
		Content: []sdk.MessagePart{sdk.ToolCallPart{
			ToolCallID: "web-call", ToolName: "web_fetch",
		}},
	}})[0]
	webResult := sdkMessagesToModelMessages([]sdk.Message{sdk.ToolMessage(sdk.ToolResultPart{
		ToolCallID: "web-call", ToolName: "web_fetch", Result: "discarded",
	})})[0]
	askCall := sdkMessagesToModelMessages([]sdk.Message{{
		Role: sdk.MessageRoleAssistant,
		Content: []sdk.MessagePart{sdk.ToolCallPart{
			ToolCallID: "ask-call", ToolName: "ask_user",
		}},
	}})[0]
	messages := []ModelMessage{
		{Content: newTextContent("missing role")},
		{Role: "user", Content: newTextContent("history 0")},
		{Role: "assistant", Content: newTextContent("answer 0")},
		{Role: "user", Content: newTextContent("history 1")},
		webCall,
		webResult,
		{Role: "assistant", Content: newTextContent("answer 1")},
		{Role: "user", Content: newTextContent("history 2")},
		{Role: "assistant", Content: newTextContent("answer 2")},
		{Role: "user", Content: newTextContent("history 3")},
		askCall,
		{Role: "user", Content: newTextContent("pipeline current")},
		{Role: "user", Content: newTextContent("memory recall")},
	}
	currentIndex := 11
	memoryIndex := 12

	want := sanitizeMessages(messages)
	if len(want) <= 10 {
		t.Fatalf("fixture did not activate tool stripping: %d messages", len(want))
	}
	want = stripToolMessages(want)
	want = repairToolCallClosures(want, syntheticToolClosureError)
	got, gotCurrent, gotMemory := normalizeContextMessages(messages, &currentIndex, &memoryIndex)

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized messages changed: got %#v want %#v", got, want)
	}
	if gotCurrent == nil || *gotCurrent != 9 || got[*gotCurrent].TextContent() != "pipeline current" {
		t.Fatalf("current index = %#v in %#v, want pipeline current at 9", gotCurrent, got)
	}
	if gotMemory == nil || *gotMemory != 10 || got[*gotMemory].TextContent() != "memory recall" {
		t.Fatalf("memory index = %#v in %#v, want memory recall at 10", gotMemory, got)
	}
}

func TestPrependContextMessagesShiftsTrackedCurrentUser(t *testing.T) {
	t.Parallel()

	prefix := []ModelMessage{
		{Role: "user", Content: newTextContent("parent fork context")},
		{Role: "assistant", Content: newTextContent("parent answer")},
	}
	messages := []ModelMessage{
		{Role: "assistant", Content: newTextContent("thread history")},
		{Role: "user", Content: newTextContent("pipeline current")},
	}
	currentIndex := 1

	got, gotCurrent := prependContextMessages(prefix, messages, &currentIndex)

	if gotCurrent == nil || *gotCurrent != 3 {
		t.Fatalf("current index = %#v, want shifted index 3", gotCurrent)
	}
	if got[*gotCurrent].TextContent() != "pipeline current" {
		t.Fatalf("tracked message = %q, want pipeline current", got[*gotCurrent].TextContent())
	}
}

func TestBuildProviderSourceFragsPlacesLegacyHookBeforeCurrent(t *testing.T) {
	t.Parallel()
	params := native.SystemPromptParams{SessionType: sessionmode.Chat, Timezone: "UTC"}
	hookTexts := []string{
		formatServiceHookContext(hooks.EventBeforePromptBuild, "before bytes"),
		formatServiceHookContext(hooks.EventAfterPromptBuild, "after bytes"),
	}
	system := native.GenerateSystemPrompt(params)
	messages := []sdk.Message{
		sdk.UserMessage("raw memory recall\n\n[Hook Context: AfterMemorySearch]\nraw memory hook"),
		sdk.UserMessage("  current request  "),
	}
	index := 1
	cfg := native.RunConfig{
		System: system, Messages: messages, ContextCurrentUserMessageIndex: &index,
		ContextQueryMaterialized: true, ContextScope: contextfrag.Scope{BotID: "bot-1"},
		ContextHookText: strings.Join(hookTexts, "\n\n"),
	}
	cfg.ContextSourceFrags = buildProviderSourceFrags(context.Background(), cfg, native.GenerateSystemSections(params), nil)

	hookCount := 0
	for _, frag := range cfg.ContextSourceFrags {
		if frag.Kind == contextfrag.KindHookContext {
			hookCount++
		}
	}
	if hookCount != 1 {
		t.Fatalf("hook fragment count = %d, want one combined user message", hookCount)
	}
	got := contextview.ApplyProviderRunConfig(context.Background(), nil, cfg)
	wantMessages := []sdk.Message{messages[0], sdk.UserMessage(cfg.ContextHookText), messages[1]}
	if got.System != system || !reflect.DeepEqual(got.Messages, wantMessages) {
		t.Fatalf("provider placement changed: system=%q messages=%#v want=%#v", got.System, got.Messages, wantMessages)
	}
}

func hasAttention(reasons []contextfrag.AttentionReason, want contextfrag.AttentionReason) bool {
	for _, reason := range reasons {
		if reason == want {
			return true
		}
	}
	return false
}

func messagesContainImage(messages []sdk.Message) bool {
	for _, message := range messages {
		for _, part := range message.Content {
			if _, ok := part.(sdk.ImagePart); ok {
				return true
			}
		}
	}
	return false
}
