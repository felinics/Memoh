package tools

import (
	"context"
	"regexp"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/sessionmode"
	"github.com/felinics/memoh/internal/messaging"
)

type usageTestResolver struct{}

func (usageTestResolver) ParseChannelType(raw string) (messaging.Platform, error) {
	return messaging.Platform(raw), nil
}

type usageTestSender struct{}

func (usageTestSender) Send(context.Context, string, messaging.Platform, messaging.SendRequest) error {
	return nil
}

type usageTestReactor struct{}

func (usageTestReactor) React(context.Context, string, messaging.Platform, messaging.ReactRequest) error {
	return nil
}

func availableToolsForTest(names ...ToolName) AvailableTools {
	sdkTools := make([]sdk.Tool, 0, len(names))
	for _, name := range names {
		sdkTools = append(sdkTools, sdk.Tool{Name: name.String()})
	}
	return NewAvailableTools(sdkTools)
}

func assertUsageItemsAreBulleted(t *testing.T, usage string) {
	t.Helper()
	usage = strings.TrimSpace(usage)
	if usage == "" {
		t.Fatal("usage is empty")
	}
	lines := strings.Split(usage, "\n")
	inItems := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "### ") {
			continue
		}
		if !strings.HasPrefix(line, "- ") {
			t.Fatalf("usage item should be a bullet line, got %q in:\n%s", line, usage)
		}
		inItems = true
	}
	if !inItems {
		t.Fatalf("usage should contain at least one bullet item, got:\n%s", usage)
	}
}

func assertNoMultipleToolSentencesOnOneLine(t *testing.T, usage string) {
	t.Helper()
	toolToken := regexp.MustCompile("`[a-z_]+`")
	for _, line := range strings.Split(usage, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "- ") {
			continue
		}
		if len(toolToken.FindAllString(line, -1)) > 1 && strings.Count(line, ". Use `") > 0 {
			t.Fatalf("usage should split separate tool instructions into separate bullet lines, got %q in:\n%s", line, usage)
		}
	}
}

func toolByNameForTest(t *testing.T, tools []sdk.Tool, name ToolName) sdk.Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == name.String() {
			return tool
		}
	}
	t.Fatalf("tool %s not found", name)
	return sdk.Tool{}
}

func requiredToolFieldsForTest(t *testing.T, tool sdk.Tool) []string {
	t.Helper()
	params, ok := tool.Parameters.(map[string]any)
	if !ok {
		t.Fatalf("tool %s parameters = %T, want map[string]any", tool.Name, tool.Parameters)
	}
	raw, ok := params["required"].([]string)
	if ok {
		return raw
	}
	rawAny, ok := params["required"].([]any)
	if !ok {
		t.Fatalf("tool %s required = %T, want []string or []any", tool.Name, params["required"])
	}
	out := make([]string, 0, len(rawAny))
	for _, item := range rawAny {
		text, ok := item.(string)
		if !ok {
			t.Fatalf("tool %s required item = %T, want string", tool.Name, item)
		}
		out = append(out, text)
	}
	return out
}

func requiredContainsForTest(required []string, field string) bool {
	for _, item := range required {
		if item == field {
			return true
		}
	}
	return false
}

func requiredAnyContainsForTest(required []any, field string) bool {
	for _, item := range required {
		if item == field {
			return true
		}
	}
	return false
}

func assertEnumContainsForTest(t *testing.T, raw any, values ...string) {
	t.Helper()
	items, ok := raw.([]any)
	if !ok {
		t.Fatalf("enum = %T, want []any", raw)
	}
	have := make(map[string]bool, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			t.Fatalf("enum item = %T, want string", item)
		}
		have[text] = true
	}
	for _, value := range values {
		if !have[value] {
			t.Fatalf("enum missing %q in %#v", value, items)
		}
	}
}

func TestBuiltInToolsHaveUsageGuidanceOrExplicitExemption(t *testing.T) {
	t.Parallel()

	covered := map[ToolName]string{
		ToolRead():                   "container",
		ToolWrite():                  "container",
		ToolList():                   "container",
		ToolEdit():                   "container",
		ToolExec():                   "container",
		ToolApplyPatch():             "container",
		ToolListExecutionLocations(): "container",
		ToolListBackground():         "background",
		ToolGetBackgroundStatus():    "background",
		ToolKillBackground():         "background",
		ToolWait():                   "background",
		ToolWaitUntil():              "background",

		ToolSend():  "messaging",
		ToolReact(): "messaging",
		ToolSpeak(): "tts",

		ToolGetContacts():    "contacts",
		ToolListSessions():   "history",
		ToolGetMessages():    "history",
		ToolSearchMessages(): "history",
		ToolSearchMemory():   "memory",
		ToolListSkills():     "skills",
		ToolUseSkill():       "skills",

		ToolSpawnAgent():  "subagents",
		ToolSendMessage(): "subagents",
		ToolListAgents():  "subagents",
		ToolListModels():  "subagents",

		ToolListSchedule():   "schedule",
		ToolGetSchedule():    "schedule",
		ToolCreateSchedule(): "schedule",
		ToolUpdateSchedule(): "schedule",
		ToolDeleteSchedule(): "schedule",

		ToolBrowserAction():        "browser",
		ToolBrowserObserve():       "browser",
		ToolComputerObserve():      "browser",
		ToolComputerAction():       "browser",
		ToolBrowserRemoteSession(): "browser",

		ToolAskUser(): "user-input",

		ToolGenerateImage(): "image-gen",
	}
	exempt := map[ToolName]string{
		ToolWebSearch():         "self-describing one-shot search tool",
		ToolWebFetch():          "self-describing one-shot fetch tool",
		ToolListWorkdirs():      "self-describing one-shot listing tool; schedule Usage references it",
		ToolListACPAgents():     "self-describing two-tier listing tool; schedule Usage references it",
		ToolGenerateVideo():     "self-describing media generation tool",
		ToolTranscribeAudio():   "self-describing media transcription tool",
		ToolListEmailAccounts(): "email tool descriptions carry account/read/write semantics",
		ToolSendEmail():         "email tool descriptions carry account/read/write semantics",
		ToolListEmail():         "email tool descriptions carry account/read/write semantics",
		ToolReadEmail():         "email tool descriptions carry account/read/write semantics",
	}

	for _, name := range BuiltInToolNames() {
		if _, ok := covered[name]; ok {
			continue
		}
		if _, ok := exempt[name]; ok {
			continue
		}
		t.Fatalf("built-in tool %s must have Usage guidance or an explicit no-guidance exemption", name.String())
	}
}

func TestMessageProviderUsageGatesRegisteredTools(t *testing.T) {
	t.Parallel()

	provider := NewMessageProvider(nil, usageTestSender{}, usageTestReactor{}, usageTestResolver{}, nil)
	session := SessionContext{SessionType: sessionmode.Chat}

	if got := provider.Usage(context.Background(), session, AvailableTools{}); got != "" {
		t.Fatalf("Usage without available tools = %q, want empty", got)
	}

	got := provider.Usage(context.Background(), session, availableToolsForTest(ToolSend()))
	assertUsageItemsAreBulleted(t, got)
	if !strings.Contains(got, "`send`") {
		t.Fatalf("Usage with send should mention send, got:\n%s", got)
	}
	if strings.Contains(got, "`react`") {
		t.Fatalf("Usage with only send should not mention react, got:\n%s", got)
	}

	got = provider.Usage(context.Background(), session, availableToolsForTest(ToolReact()))
	assertUsageItemsAreBulleted(t, got)
	if !strings.Contains(got, "`react`") {
		t.Fatalf("Usage with react should mention react, got:\n%s", got)
	}
	if strings.Contains(got, "`send`") {
		t.Fatalf("Usage with only react should not mention send, got:\n%s", got)
	}

	currentSession := SessionContext{SessionType: sessionmode.Chat, CurrentPlatform: "telegram", ReplyTarget: "chat-1"}
	got = provider.Usage(context.Background(), currentSession, availableToolsForTest(ToolSend(), ToolReact()))
	if !strings.Contains(got, "Use ordinary assistant text for normal replies") || !strings.Contains(got, "Omit `target` to react") {
		t.Fatalf("Usage with an explicit current conversation should distinguish normal replies from local reactions, got:\n%s", got)
	}
	for _, want := range []string{"`message.parts`", "link/code_block/mention", "list_item"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Usage should expose structured message parts guidance containing %q, got:\n%s", want, got)
		}
	}
	for _, want := range []string{"$...$", "$$...$$", "display LaTeX"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Usage should expose Markdown math guidance containing %q, got:\n%s", want, got)
		}
	}

	nonTelegramSession := SessionContext{SessionType: sessionmode.Chat, CurrentPlatform: "discord", ReplyTarget: "channel-1"}
	got = provider.Usage(context.Background(), nonTelegramSession, availableToolsForTest(ToolSend()))
	if strings.Contains(got, "display LaTeX") || strings.Contains(got, "$$...$$") {
		t.Fatalf("Usage should not expose Telegram Markdown math guidance for non-Telegram sessions, got:\n%s", got)
	}

	backgroundSession := SessionContext{SessionType: sessionmode.Schedule, CurrentPlatform: "telegram", ReplyTarget: "chat-1"}
	got = provider.Usage(context.Background(), backgroundSession, availableToolsForTest(ToolReact()))
	if strings.Contains(got, "Omit `target`") || strings.Contains(got, "unless the current conversation target is explicit") || !strings.Contains(got, "Specify `platform` and `target`") {
		t.Fatalf("Usage for background reactions should require explicit target, got:\n%s", got)
	}
}

func TestMessageProviderToolDescriptionsGateCurrentConversationTarget(t *testing.T) {
	t.Parallel()

	provider := NewMessageProvider(nil, usageTestSender{}, usageTestReactor{}, usageTestResolver{}, nil)

	currentTools, err := provider.Tools(context.Background(), SessionContext{
		SessionType:     sessionmode.Chat,
		CurrentPlatform: "telegram",
		ReplyTarget:     "chat-1",
	})
	if err != nil {
		t.Fatalf("Tools current session: %v", err)
	}
	currentSend := toolByNameForTest(t, currentTools, ToolSend())
	if !strings.Contains(currentSend.Description, "Use ordinary assistant text for normal replies") {
		t.Fatalf("send description with explicit current conversation should reserve normal replies for assistant text, got:\n%s", currentSend.Description)
	}
	if requiredContainsForTest(requiredToolFieldsForTest(t, currentSend), "target") {
		t.Fatalf("send should not require target when current conversation is explicit, required=%v", requiredToolFieldsForTest(t, currentSend))
	}

	backgroundTools, err := provider.Tools(context.Background(), SessionContext{
		SessionType:     sessionmode.Schedule,
		CurrentPlatform: "telegram",
		ReplyTarget:     "chat-1",
	})
	if err != nil {
		t.Fatalf("Tools background session: %v", err)
	}
	backgroundSend := toolByNameForTest(t, backgroundTools, ToolSend())
	if strings.Contains(backgroundSend.Description, "current conversation") {
		t.Fatalf("send description for background sessions should not allow omitted target, got:\n%s", backgroundSend.Description)
	}
	required := requiredToolFieldsForTest(t, backgroundSend)
	for _, field := range []string{"platform", "target"} {
		if !requiredContainsForTest(required, field) {
			t.Fatalf("send should require %s for background sessions, required=%v", field, required)
		}
	}

	backgroundReact := toolByNameForTest(t, backgroundTools, ToolReact())
	if strings.Contains(backgroundReact.Description, "omitted") || strings.Contains(backgroundReact.Description, "unless the current conversation target is explicit") {
		t.Fatalf("react description for background sessions should not allow omitted target/platform, got:\n%s", backgroundReact.Description)
	}
	required = requiredToolFieldsForTest(t, backgroundReact)
	for _, field := range []string{"message_id", "platform", "target"} {
		if !requiredContainsForTest(required, field) {
			t.Fatalf("react should require %s for background sessions, required=%v", field, required)
		}
	}
}

func TestMessageProviderSendToolExposesStructuredMessagePartsSchema(t *testing.T) {
	t.Parallel()

	provider := NewMessageProvider(nil, usageTestSender{}, usageTestReactor{}, usageTestResolver{}, nil)
	tools, err := provider.Tools(context.Background(), SessionContext{
		SessionType:     sessionmode.Chat,
		CurrentPlatform: "telegram",
		ReplyTarget:     "chat-1",
	})
	if err != nil {
		t.Fatalf("Tools returned error: %v", err)
	}

	send := toolByNameForTest(t, tools, ToolSend())
	params, ok := send.Parameters.(map[string]any)
	if !ok {
		t.Fatalf("send parameters = %T, want map[string]any", send.Parameters)
	}
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("send properties = %T, want map[string]any", params["properties"])
	}
	if params["additionalProperties"] != false {
		t.Fatalf("send root schema should be strict, got %#v", params["additionalProperties"])
	}
	message, ok := props["message"].(map[string]any)
	if !ok {
		t.Fatalf("message schema = %T, want map[string]any", props["message"])
	}
	messageProps, ok := message["properties"].(map[string]any)
	if !ok {
		t.Fatalf("message schema should expose properties, got %#v", message)
	}
	parts, ok := messageProps["parts"].(map[string]any)
	if !ok {
		t.Fatalf("message.parts schema missing in %#v", messageProps)
	}
	items, ok := parts["items"].(map[string]any)
	if !ok {
		t.Fatalf("message.parts.items schema = %T, want map[string]any", parts["items"])
	}
	partProps, ok := items["properties"].(map[string]any)
	if !ok {
		t.Fatalf("part properties missing in %#v", items)
	}
	typeSchema, ok := partProps["type"].(map[string]any)
	if !ok {
		t.Fatalf("part type schema missing in %#v", partProps)
	}
	assertEnumContainsForTest(t, typeSchema["enum"], "text", "link", "code_block", "mention", "emoji", "heading", "blockquote", "list_item")
	partRequired, ok := items["required"].([]any)
	if !ok || !requiredAnyContainsForTest(partRequired, "type") || requiredAnyContainsForTest(partRequired, "text") {
		t.Fatalf("message part schema should require type but not text, required=%#v", items["required"])
	}
	styles, ok := partProps["styles"].(map[string]any)
	if !ok {
		t.Fatalf("part styles schema missing in %#v", partProps)
	}
	styleItems, ok := styles["items"].(map[string]any)
	if !ok {
		t.Fatalf("part styles items schema = %T, want map[string]any", styles["items"])
	}
	assertEnumContainsForTest(t, styleItems["enum"], "bold", "italic", "strikethrough", "code", "underline", "spoiler")
	actions, ok := messageProps["actions"].(map[string]any)
	if !ok {
		t.Fatalf("message.actions schema missing in %#v", messageProps)
	}
	actionItems, ok := actions["items"].(map[string]any)
	if !ok {
		t.Fatalf("message.actions.items schema = %T, want map[string]any", actions["items"])
	}
	if actionItems["additionalProperties"] != false {
		t.Fatalf("message action schema should be strict, got %#v", actionItems["additionalProperties"])
	}
	actionRequired, ok := actionItems["required"].([]string)
	if !ok || !requiredContainsForTest(actionRequired, "label") || !requiredContainsForTest(actionRequired, "url") {
		t.Fatalf("message action schema should require label and url, required=%#v", actionItems["required"])
	}
	actionProps, ok := actionItems["properties"].(map[string]any)
	if !ok {
		t.Fatalf("message action properties missing in %#v", actionItems)
	}
	if _, ok := actionProps["value"]; ok {
		t.Fatalf("message action schema must not expose callback value to model: %#v", actionProps["value"])
	}
	if _, ok := actionItems["anyOf"]; ok {
		t.Fatalf("message action schema should not expose value/url anyOf, got %#v", actionItems["anyOf"])
	}
	reply, ok := messageProps["reply"].(map[string]any)
	if !ok {
		t.Fatalf("message.reply schema missing in %#v", messageProps)
	}
	if reply["additionalProperties"] != false {
		t.Fatalf("message reply schema should be strict, got %#v", reply["additionalProperties"])
	}
	replyRequired, ok := reply["required"].([]string)
	if !ok || !requiredContainsForTest(replyRequired, "message_id") {
		t.Fatalf("message reply schema should require message_id, required=%#v", reply["required"])
	}
	replyProps, ok := reply["properties"].(map[string]any)
	if !ok {
		t.Fatalf("message reply properties missing in %#v", reply)
	}
	if _, ok := replyProps["message_id"]; !ok {
		t.Fatalf("message reply schema missing message_id in %#v", replyProps)
	}

	topLevelAttachments, ok := props["attachments"].(map[string]any)
	if !ok {
		t.Fatalf("top-level attachments schema missing in %#v", props)
	}
	messageAttachments, ok := messageProps["attachments"].(map[string]any)
	if !ok {
		t.Fatalf("message.attachments schema missing in %#v", messageProps)
	}
	for label, attachments := range map[string]map[string]any{
		"top-level attachments": topLevelAttachments,
		"message.attachments":   messageAttachments,
	} {
		attachmentItems, ok := attachments["items"].(map[string]any)
		if !ok {
			t.Fatalf("%s.items schema = %T, want map[string]any", label, attachments["items"])
		}
		anyOf, ok := attachmentItems["anyOf"].([]any)
		if !ok || len(anyOf) != 2 {
			t.Fatalf("%s.items should accept string or strict object, got %#v", label, attachmentItems["anyOf"])
		}
		objectSchema, ok := anyOf[1].(map[string]any)
		if !ok {
			t.Fatalf("%s object schema = %T, want map[string]any", label, anyOf[1])
		}
		if objectSchema["additionalProperties"] != false {
			t.Fatalf("%s object schema should be strict, got %#v", label, objectSchema["additionalProperties"])
		}
		objectProps, ok := objectSchema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s object properties missing in %#v", label, objectSchema)
		}
		for _, field := range []string{"path", "url", "base64", "content_hash", "platform_key"} {
			if _, ok := objectProps[field]; !ok {
				t.Fatalf("%s object schema missing %q in %#v", label, field, objectProps)
			}
		}
		attachmentType, ok := objectProps["type"].(map[string]any)
		if !ok {
			t.Fatalf("%s object type schema missing in %#v", label, objectProps)
		}
		assertEnumContainsForTest(t, attachmentType["enum"], "image", "audio", "video", "voice", "file", "gif")
		if _, ok := objectSchema["anyOf"].([]any); !ok {
			t.Fatalf("%s object schema should require a reference field via anyOf, got %#v", label, objectSchema["anyOf"])
		}
	}
}

func TestContainerProviderUsageGatesRegisteredTools(t *testing.T) {
	t.Parallel()

	provider := NewContainerProvider(nil, nil, nil, "")
	if got := provider.Usage(context.Background(), SessionContext{}, AvailableTools{}); got != "" {
		t.Fatalf("Usage without file tools = %q, want empty", got)
	}

	got := provider.Usage(context.Background(), SessionContext{SupportsImageInput: true}, availableToolsForTest(ToolRead(), ToolWrite(), ToolList(), ToolEdit(), ToolApplyPatch(), ToolExec(), ToolListExecutionLocations()))
	assertUsageItemsAreBulleted(t, got)
	for _, want := range []string{"`read`", "`write`", "`list`", "`edit`", "`apply_patch`", "`exec`", "`list_execution_locations`", "does not switch locations or folders", "also supports images"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Usage with basic tools should contain %q, got:\n%s", want, got)
		}
	}

	got = provider.Usage(context.Background(), SessionContext{
		WorkspaceTargetID:   "target-1",
		WorkspaceTargetKind: "remote",
		WorkspaceTargetName: "Office PC",
	}, availableToolsForTest(ToolRead(), ToolExec(), ToolListExecutionLocations(), ToolBrowserObserve(), ToolComputerObserve()))
	assertUsageItemsAreBulleted(t, got)
	for _, want := range []string{"request-selected connected computer", "Office PC", "explicit `target_id` still takes precedence", "request-selected default for this turn", "Browser Use and Computer Use", "native Server Workspace", "`browser_observe`", "`computer_observe`", "`target_id` `native`"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Usage with a request target should contain %q, got:\n%s", want, got)
		}
	}

	got = provider.Usage(context.Background(), SessionContext{}, availableToolsForTest(ToolRead()))
	assertUsageItemsAreBulleted(t, got)
	if !strings.Contains(got, "`read`") {
		t.Fatalf("Usage with read should mention it, got:\n%s", got)
	}
	if strings.Contains(got, "also supports images") {
		t.Fatalf("Usage without image support should not mention image read support, got:\n%s", got)
	}
	for _, absent := range []string{"`write`", "`list`", "`edit`", "`apply_patch`", "`exec`", "`list_execution_locations`", "`list_background`", "`get_background_status`", "`kill_background`"} {
		if strings.Contains(got, absent) {
			t.Fatalf("Usage without %s should not mention it, got:\n%s", absent, got)
		}
	}

	got = provider.Usage(context.Background(), SessionContext{}, availableToolsForTest(ToolApplyPatch()))
	assertUsageItemsAreBulleted(t, got)
	if !strings.Contains(got, "`apply_patch`") {
		t.Fatalf("Usage with patch should mention it, got:\n%s", got)
	}
	for _, absent := range []string{"`read`", "`write`", "`exec`", "`list_execution_locations`"} {
		if strings.Contains(got, absent) {
			t.Fatalf("Usage without %s should not mention it, got:\n%s", absent, got)
		}
	}
}

func TestBackgroundProviderUsageGatesRegisteredTools(t *testing.T) {
	t.Parallel()

	provider := NewBackgroundProvider(nil, nil)
	if got := provider.Usage(context.Background(), SessionContext{}, AvailableTools{}); got != "" {
		t.Fatalf("Usage without background tools = %q, want empty", got)
	}

	got := provider.Usage(context.Background(), SessionContext{}, availableToolsForTest(ToolListBackground(), ToolWait(), ToolWaitUntil(), ToolGetBackgroundStatus(), ToolKillBackground()))
	assertUsageItemsAreBulleted(t, got)
	for _, want := range []string{"`list_background`", "`wait`", "`wait_until`", "`get_background_status`", "`kill_background`", "read `result`"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Usage with background tools should contain %q, got:\n%s", want, got)
		}
	}

	got = provider.Usage(context.Background(), SessionContext{}, availableToolsForTest(ToolWaitUntil(), ToolGetBackgroundStatus()))
	assertUsageItemsAreBulleted(t, got)
	for _, want := range []string{"`wait_until`", "`get_background_status`"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Usage with %s should mention it, got:\n%s", want, got)
		}
	}
	for _, absent := range []string{"`kill_background`", "`list_background`"} {
		if strings.Contains(got, absent) {
			t.Fatalf("Usage without %s should not mention it, got:\n%s", absent, got)
		}
	}
}

func TestAskUserProviderUsageGatesAskUser(t *testing.T) {
	t.Parallel()

	provider := NewAskUserProvider(nil)
	if got := provider.Usage(context.Background(), SessionContext{}, AvailableTools{}); got != "" {
		t.Fatalf("Usage without ask_user = %q, want empty", got)
	}

	got := provider.Usage(context.Background(), SessionContext{CanRequestUserInput: true}, availableToolsForTest(ToolAskUser()))
	assertUsageItemsAreBulleted(t, got)
	for _, want := range []string{"`ask_user`", "multiple-choice question", "allow_custom", "`multi_select`", "（多选）"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Usage with ask_user should contain %q, got:\n%s", want, got)
		}
	}

	got = provider.Usage(context.Background(), SessionContext{SessionType: sessionmode.Chat}, availableToolsForTest(ToolAskUser()))
	if got != "" {
		t.Fatalf("Usage without user input delivery = %q, want empty", got)
	}

	got = provider.Usage(context.Background(), SessionContext{SessionType: sessionmode.Chat, CanListUserInput: true}, availableToolsForTest(ToolAskUser()))
	assertUsageItemsAreBulleted(t, got)
	for _, want := range []string{"`ask_user`", "multiple-choice question", "allow_custom", "`multi_select`", "（多选）"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Usage with list-only discovery should contain %q, got:\n%s", want, got)
		}
	}

	got = provider.Usage(context.Background(), SessionContext{SessionType: sessionmode.Schedule, CanListUserInput: true}, availableToolsForTest(ToolAskUser()))
	if got != "" {
		t.Fatalf("Usage with non-ACP list-only user input discovery = %q, want empty", got)
	}

	got = provider.Usage(context.Background(), SessionContext{SessionType: sessionmode.Schedule, CanRequestUserInput: true}, availableToolsForTest(ToolAskUser()))
	if got != "" {
		t.Fatalf("Usage in schedule = %q, want empty", got)
	}

	got = provider.Usage(context.Background(), SessionContext{SessionType: sessionmode.Discuss, CanRequestUserInput: true}, availableToolsForTest(ToolAskUser()))
	if got != "" {
		t.Fatalf("Usage in discuss = %q, want empty", got)
	}

	chatTools, err := provider.Tools(context.Background(), SessionContext{SessionType: sessionmode.Chat, CanRequestUserInput: true})
	if err != nil {
		t.Fatalf("Tools in chat: %v", err)
	}
	if len(chatTools) != 1 || chatTools[0].Name != ToolAskUser().String() {
		t.Fatalf("chat tools = %#v, want ask_user", chatTools)
	}
	if !strings.Contains(chatTools[0].Description, "（多选）") {
		t.Fatalf("ask_user description must tell the model to label multi-select questions: %q", chatTools[0].Description)
	}

	noDeliveryTools, err := provider.Tools(context.Background(), SessionContext{SessionType: sessionmode.Chat})
	if err != nil {
		t.Fatalf("Tools without user input delivery: %v", err)
	}
	if len(noDeliveryTools) != 0 {
		t.Fatalf("tools without user input delivery = %#v, want none", noDeliveryTools)
	}

	listOnlyChatTools, err := provider.Tools(context.Background(), SessionContext{SessionType: sessionmode.Chat, CanListUserInput: true})
	if err != nil {
		t.Fatalf("Tools with list-only user input discovery: %v", err)
	}
	if len(listOnlyChatTools) != 1 || listOnlyChatTools[0].Name != ToolAskUser().String() {
		t.Fatalf("list-only chat tools = %#v, want ask_user", listOnlyChatTools)
	}

	listOnlyACPTools, err := provider.Tools(context.Background(), SessionContext{SessionType: sessionmode.ACPAgent, CanListUserInput: true})
	if err != nil {
		t.Fatalf("Tools with ACP list-only user input discovery: %v", err)
	}
	if len(listOnlyACPTools) != 1 || listOnlyACPTools[0].Name != ToolAskUser().String() {
		t.Fatalf("ACP list-only tools = %#v, want ask_user", listOnlyACPTools)
	}

	backgroundTools, err := provider.Tools(context.Background(), SessionContext{SessionType: sessionmode.Schedule, CanRequestUserInput: true})
	if err != nil {
		t.Fatalf("Tools in schedule: %v", err)
	}
	if len(backgroundTools) != 0 {
		t.Fatalf("schedule tools = %#v, want none", backgroundTools)
	}

	discussTools, err := provider.Tools(context.Background(), SessionContext{SessionType: sessionmode.Discuss, CanRequestUserInput: true})
	if err != nil {
		t.Fatalf("Tools in discuss: %v", err)
	}
	if len(discussTools) != 0 {
		t.Fatalf("discuss tools = %#v, want none", discussTools)
	}
}

func TestTTSProviderUsageGatesSpeak(t *testing.T) {
	t.Parallel()

	provider := NewTTSProvider(nil, nil, nil, nil, nil)
	if got := provider.Usage(context.Background(), SessionContext{}, AvailableTools{}); got != "" {
		t.Fatalf("Usage without speak = %q, want empty", got)
	}

	got := provider.Usage(context.Background(), SessionContext{}, availableToolsForTest(ToolSpeak()))
	assertUsageItemsAreBulleted(t, got)
	if !strings.Contains(got, "`speak`") {
		t.Fatalf("Usage with speak should mention speak, got:\n%s", got)
	}
	if strings.Contains(got, "Omit `target`") || !strings.Contains(got, "Specify `platform` and `target`") {
		t.Fatalf("Usage without an explicit current conversation should require explicit target, got:\n%s", got)
	}
	if strings.Contains(got, "unless the current conversation target is explicit") {
		t.Fatalf("Usage without an explicit current conversation should not contain a contradictory exception, got:\n%s", got)
	}
	if strings.Contains(got, "`send`") || strings.Contains(got, "`react`") {
		t.Fatalf("Usage with only speak should not mention send/react, got:\n%s", got)
	}

	got = provider.Usage(context.Background(), SessionContext{SessionType: sessionmode.Chat, CurrentPlatform: "telegram", ReplyTarget: "chat-1"}, availableToolsForTest(ToolSpeak()))
	if !strings.Contains(got, "Omit `target` to speak") {
		t.Fatalf("Usage with an explicit current conversation should allow speaking without target, got:\n%s", got)
	}
}

func TestSpeakToolPromptMetadataGatesCurrentConversationTarget(t *testing.T) {
	t.Parallel()

	description, _, _, required := speakToolPromptMetadata(SessionContext{
		SessionType:     sessionmode.Chat,
		CurrentPlatform: "telegram",
		ReplyTarget:     "chat-1",
	})
	if !strings.Contains(description, "When target is omitted") {
		t.Fatalf("speak description with explicit current conversation should allow omitted target, got:\n%s", description)
	}
	if requiredContainsForTest(required, "target") {
		t.Fatalf("speak should not require target when current conversation is explicit, required=%v", required)
	}

	description, _, _, required = speakToolPromptMetadata(SessionContext{
		SessionType:     sessionmode.Schedule,
		CurrentPlatform: "telegram",
		ReplyTarget:     "chat-1",
	})
	if strings.Contains(description, "When target is omitted") {
		t.Fatalf("speak description for background sessions should not allow omitted target, got:\n%s", description)
	}
	for _, field := range []string{"text", "platform", "target"} {
		if !requiredContainsForTest(required, field) {
			t.Fatalf("speak should require %s for background sessions, required=%v", field, required)
		}
	}
}

func TestMemoryProviderUsageGatesSearchMemory(t *testing.T) {
	t.Parallel()

	provider := NewMemoryProvider(nil, nil, nil, nil)
	if got := provider.Usage(context.Background(), SessionContext{}, AvailableTools{}); got != "" {
		t.Fatalf("Usage without search_memory = %q, want empty", got)
	}
	got := provider.Usage(context.Background(), SessionContext{}, availableToolsForTest(ToolSearchMemory()))
	assertUsageItemsAreBulleted(t, got)
	for _, want := range []string{"`search_memory`", "durable user preferences", "prior conversations", "latest user message"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Usage with search_memory should mention %q, got:\n%s", want, got)
		}
	}
	got = provider.Usage(context.Background(), SessionContext{}, availableToolsForTest(ToolSearchMemory(), ToolGetMessages()))
	for _, want := range []string{"`source_refs`", "`get_messages`", "`session_id`", "`message_id`"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Usage with memory/history drill-down should mention %q, got:\n%s", want, got)
		}
	}
}

func TestSkillProviderUsageGatesUseSkill(t *testing.T) {
	t.Parallel()

	provider := NewSkillProvider(nil)
	if got := provider.Usage(context.Background(), SessionContext{}, AvailableTools{}); got != "" {
		t.Fatalf("Usage without use_skill = %q, want empty", got)
	}
	got := provider.Usage(context.Background(), SessionContext{}, availableToolsForTest(ToolUseSkill()))
	assertUsageItemsAreBulleted(t, got)
	if !strings.Contains(got, "`use_skill`") {
		t.Fatalf("Usage with use_skill should mention it, got:\n%s", got)
	}
}

func TestHistoryProviderUsageGatesRegisteredTools(t *testing.T) {
	t.Parallel()

	provider := NewHistoryProvider(nil, nil, nil, nil)
	if got := provider.Usage(context.Background(), SessionContext{}, AvailableTools{}); got != "" {
		t.Fatalf("Usage without history tools = %q, want empty", got)
	}

	got := provider.Usage(context.Background(), SessionContext{}, availableToolsForTest(ToolListSessions()))
	assertUsageItemsAreBulleted(t, got)
	assertNoMultipleToolSentencesOnOneLine(t, got)
	if !strings.Contains(got, "`list_sessions`") {
		t.Fatalf("Usage with list_sessions should mention it, got:\n%s", got)
	}
	if strings.Contains(got, "`search_messages`") {
		t.Fatalf("Usage with only list_sessions should not mention search_messages, got:\n%s", got)
	}

	got = provider.Usage(context.Background(), SessionContext{}, availableToolsForTest(ToolGetMessages()))
	assertUsageItemsAreBulleted(t, got)
	assertNoMultipleToolSentencesOnOneLine(t, got)
	if !strings.Contains(got, "`get_messages`") {
		t.Fatalf("Usage with get_messages should mention it, got:\n%s", got)
	}
	for _, absent := range []string{"`list_sessions`", "`search_messages`"} {
		if strings.Contains(got, absent) {
			t.Fatalf("Usage with only get_messages should not mention %s, got:\n%s", absent, got)
		}
	}

	got = provider.Usage(context.Background(), SessionContext{}, availableToolsForTest(ToolSearchMessages()))
	assertUsageItemsAreBulleted(t, got)
	assertNoMultipleToolSentencesOnOneLine(t, got)
	if !strings.Contains(got, "`search_messages`") {
		t.Fatalf("Usage with search_messages should mention it, got:\n%s", got)
	}
	if strings.Contains(got, "`list_sessions`") {
		t.Fatalf("Usage with only search_messages should not mention list_sessions, got:\n%s", got)
	}

	got = provider.Usage(context.Background(), SessionContext{}, availableToolsForTest(ToolListSessions(), ToolGetMessages()))
	assertUsageItemsAreBulleted(t, got)
	if !strings.Contains(got, "as `session_id` for `get_messages`") {
		t.Fatalf("Usage with list_sessions/get_messages should explain session_id dependency, got:\n%s", got)
	}
	if strings.Contains(got, "`search_messages`") {
		t.Fatalf("Usage without search_messages should not mention it, got:\n%s", got)
	}

	got = provider.Usage(context.Background(), SessionContext{}, availableToolsForTest(ToolListSessions(), ToolSearchMessages()))
	assertUsageItemsAreBulleted(t, got)
	if !strings.Contains(got, "as `session_id` for `search_messages`") {
		t.Fatalf("Usage with list_sessions/search_messages should explain session_id dependency, got:\n%s", got)
	}
	if strings.Contains(got, "`get_messages`") {
		t.Fatalf("Usage without get_messages should not mention it, got:\n%s", got)
	}
}

func TestContactsProviderUsageGatesGetContacts(t *testing.T) {
	t.Parallel()

	provider := NewContactsProvider(nil, nil)
	if got := provider.Usage(context.Background(), SessionContext{}, AvailableTools{}); got != "" {
		t.Fatalf("Usage without get_contacts = %q, want empty", got)
	}

	got := provider.Usage(context.Background(), SessionContext{SessionType: sessionmode.Chat, CurrentPlatform: "telegram", ReplyTarget: "chat-1"}, availableToolsForTest(ToolGetContacts(), ToolSend(), ToolSearchMessages()))
	assertUsageItemsAreBulleted(t, got)
	for _, want := range []string{"`get_contacts`", "`send`", "`search_messages`"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Usage with contacts tools should contain %q, got:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "pass the returned `platform` and `target`") && !strings.Contains(got, "Pass the returned `platform` and `target`") {
		t.Fatalf("Usage with messaging dependencies should describe passing contacts output to tools, got:\n%s", got)
	}
	for _, duplicate := range []string{"ordinary assistant text", "Omit `target`"} {
		if strings.Contains(got, duplicate) {
			t.Fatalf("Contacts usage should not restate messaging provider guidance %q, got:\n%s", duplicate, got)
		}
	}

	got = provider.Usage(context.Background(), SessionContext{SessionType: sessionmode.Schedule, CurrentPlatform: "telegram", ReplyTarget: "chat-1"}, availableToolsForTest(ToolGetContacts(), ToolSend()))
	assertUsageItemsAreBulleted(t, got)
	if strings.Contains(got, "Omit `target`") || !strings.Contains(got, "Pass the returned `platform` and `target`") {
		t.Fatalf("Usage for background contacts/messaging should point to returned target values, got:\n%s", got)
	}

	got = provider.Usage(context.Background(), SessionContext{}, availableToolsForTest(ToolGetContacts()))
	assertUsageItemsAreBulleted(t, got)
	if !strings.Contains(got, "`get_contacts`") {
		t.Fatalf("Usage with get_contacts should mention it, got:\n%s", got)
	}
	for _, absent := range []string{"`send`", "`speak`", "`list_sessions`", "`search_messages`"} {
		if strings.Contains(got, absent) {
			t.Fatalf("Usage without dependent tools should not contain %q, got:\n%s", absent, got)
		}
	}

	got = provider.Usage(context.Background(), SessionContext{}, availableToolsForTest(ToolGetContacts(), ToolSearchMessages()))
	assertUsageItemsAreBulleted(t, got)
	if !strings.Contains(got, "`search_messages`") {
		t.Fatalf("Usage with search_messages should mention it, got:\n%s", got)
	}
	for _, absent := range []string{"`list_sessions`", "`get_messages`", "list sessions", "read recent messages"} {
		if strings.Contains(got, absent) {
			t.Fatalf("Usage with only search_messages should not imply %q, got:\n%s", absent, got)
		}
	}
	if !strings.Contains(got, "`session_id` or `contact_id` filters for `search_messages`") {
		t.Fatalf("Usage with search_messages should scope contact filters to search_messages, got:\n%s", got)
	}

	got = provider.Usage(context.Background(), SessionContext{}, availableToolsForTest(ToolGetContacts(), ToolListSessions(), ToolGetMessages()))
	assertUsageItemsAreBulleted(t, got)
	if strings.Contains(got, "contact filters") || strings.Contains(got, "`contact_id`") {
		t.Fatalf("Usage without search_messages should not suggest contact filters for list/get history tools, got:\n%s", got)
	}
}

func TestBrowserProviderUsageGatesRegisteredTools(t *testing.T) {
	t.Parallel()

	provider := NewBrowserProvider(nil, nil, nil, nil, "")
	if got := provider.Usage(context.Background(), SessionContext{}, AvailableTools{}); got != "" {
		t.Fatalf("Usage without browser tools = %q, want empty", got)
	}

	got := provider.Usage(context.Background(), SessionContext{}, availableToolsForTest(ToolBrowserObserve(), ToolBrowserAction()))
	assertUsageItemsAreBulleted(t, got)
	if !strings.Contains(got, "`browser_observe`") || !strings.Contains(got, "`browser_action`") {
		t.Fatalf("Usage with browser tools should mention them, got:\n%s", got)
	}
	for _, absent := range []string{"`computer_observe`", "`computer_action`", "`browser_remote_session`", "`read`"} {
		if strings.Contains(got, absent) {
			t.Fatalf("Usage without %s should not mention it, got:\n%s", absent, got)
		}
	}

	got = provider.Usage(context.Background(), SessionContext{}, availableToolsForTest(ToolComputerObserve(), ToolComputerAction(), ToolBrowserRemoteSession(), ToolRead()))
	assertUsageItemsAreBulleted(t, got)
	for _, want := range []string{"`computer_observe`", "`computer_action`", "`browser_remote_session`"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Usage with %s should mention it, got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "`read`") || strings.Contains(got, "when you need the image") {
		t.Fatalf("Usage without image input support should not tell the model to read screenshots as images, got:\n%s", got)
	}
	for _, absent := range []string{"`browser_observe`", "`browser_action`"} {
		if strings.Contains(got, absent) {
			t.Fatalf("Usage without %s should not mention it, got:\n%s", absent, got)
		}
	}

	got = provider.Usage(context.Background(), SessionContext{SupportsImageInput: true}, availableToolsForTest(ToolComputerObserve(), ToolRead()))
	assertUsageItemsAreBulleted(t, got)
	if !strings.Contains(got, "`computer_observe`") || !strings.Contains(got, "`read`") || !strings.Contains(got, "when you need the image") {
		t.Fatalf("Usage with observe/read and image input support should mention image read path, got:\n%s", got)
	}

	got = provider.Usage(context.Background(), SessionContext{}, availableToolsForTest(ToolBrowserAction()))
	assertUsageItemsAreBulleted(t, got)
	if !strings.Contains(got, "`browser_action`") {
		t.Fatalf("Usage with browser_action should mention it, got:\n%s", got)
	}
	for _, absent := range []string{"Observe before acting", "snapshot", "screenshots"} {
		if strings.Contains(got, absent) {
			t.Fatalf("Usage without observe tools should not contain %q, got:\n%s", absent, got)
		}
	}

	got = provider.Usage(context.Background(), SessionContext{}, availableToolsForTest(ToolBrowserRemoteSession()))
	assertUsageItemsAreBulleted(t, got)
	if !strings.Contains(got, "`browser_remote_session`") {
		t.Fatalf("Usage with browser_remote_session should mention it, got:\n%s", got)
	}
	if strings.Contains(got, "above") {
		t.Fatalf("Usage with only browser_remote_session should not refer to tools above, got:\n%s", got)
	}
	for _, absent := range []string{"`browser_observe`", "`browser_action`", "`computer_observe`", "`computer_action`"} {
		if strings.Contains(got, absent) {
			t.Fatalf("Usage without %s should not mention it, got:\n%s", absent, got)
		}
	}
}

func TestScheduleProviderUsageGatesRegisteredTools(t *testing.T) {
	t.Parallel()

	provider := NewScheduleProvider(nil, nil)
	if got := provider.Usage(context.Background(), SessionContext{}, AvailableTools{}); got != "" {
		t.Fatalf("Usage without schedule tools = %q, want empty", got)
	}

	got := provider.Usage(context.Background(), SessionContext{}, availableToolsForTest(ToolCreateSchedule(), ToolSend()))
	assertUsageItemsAreBulleted(t, got)
	if !strings.Contains(got, "`create_schedule`") || !strings.Contains(got, "`send`") {
		t.Fatalf("Usage with create_schedule/send should mention them, got:\n%s", got)
	}
	if !strings.Contains(got, "explicit `platform` and `target`") {
		t.Fatalf("Usage with create_schedule/send should require explicit delivery target in scheduled commands, got:\n%s", got)
	}
	for _, absent := range []string{"`list_schedule`", "`get_schedule`", "`update_schedule`", "`delete_schedule`"} {
		if strings.Contains(got, absent) {
			t.Fatalf("Usage without %s should not mention it, got:\n%s", absent, got)
		}
	}

	got = provider.Usage(context.Background(), SessionContext{}, availableToolsForTest(ToolCreateSchedule()))
	assertUsageItemsAreBulleted(t, got)
	if !strings.Contains(got, "`create_schedule`") {
		t.Fatalf("Usage with create_schedule should mention it, got:\n%s", got)
	}
	if strings.Contains(got, "`send`") || strings.Contains(got, "`speak`") {
		t.Fatalf("Usage without messaging tools should not mention send/speak, got:\n%s", got)
	}

	got = provider.Usage(context.Background(), SessionContext{}, availableToolsForTest(ToolCreateSchedule(), ToolSpeak()))
	assertUsageItemsAreBulleted(t, got)
	if !strings.Contains(got, "`create_schedule`") {
		t.Fatalf("Usage with create_schedule/speak should mention create_schedule, got:\n%s", got)
	}
	if strings.Contains(got, "`send`") || !strings.Contains(got, "`speak`") {
		t.Fatalf("Usage with speak should mention available speak and omit unavailable send, got:\n%s", got)
	}
	if !strings.Contains(got, "explicit `platform` and `target`") {
		t.Fatalf("Usage with create_schedule/speak should require explicit delivery target in scheduled commands, got:\n%s", got)
	}

	got = provider.Usage(context.Background(), SessionContext{}, availableToolsForTest(ToolListSchedule(), ToolGetSchedule(), ToolUpdateSchedule(), ToolDeleteSchedule()))
	assertUsageItemsAreBulleted(t, got)
	for _, want := range []string{"`list_schedule`", "`get_schedule`", "`update_schedule`", "`delete_schedule`"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Usage with %s should mention it, got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "`create_schedule`") || strings.Contains(got, "`send`") {
		t.Fatalf("Usage without create_schedule/send should not mention them, got:\n%s", got)
	}

	got = provider.Usage(context.Background(), SessionContext{}, availableToolsForTest(ToolListSchedule()))
	assertUsageItemsAreBulleted(t, got)
	if !strings.Contains(got, "`list_schedule`") {
		t.Fatalf("Usage with list_schedule should mention it, got:\n%s", got)
	}
	for _, absent := range []string{"`get_schedule`", "`update_schedule`", "`delete_schedule`", "manage existing"} {
		if strings.Contains(got, absent) {
			t.Fatalf("Usage with only list_schedule should not imply %q, got:\n%s", absent, got)
		}
	}
}

func TestSpawnProviderUsageGatesRegisteredTools(t *testing.T) {
	t.Parallel()

	provider := &SpawnProvider{}
	if got := provider.Usage(context.Background(), SessionContext{}, AvailableTools{}); got != "" {
		t.Fatalf("Usage without spawn_agent = %q, want empty", got)
	}

	got := provider.Usage(context.Background(), SessionContext{}, availableToolsForTest(ToolSpawnAgent()))
	assertUsageItemsAreBulleted(t, got)
	if !strings.Contains(got, "`spawn_agent`") {
		t.Fatalf("Usage with spawn_agent should mention it, got:\n%s", got)
	}
	if !strings.Contains(got, "configured tools") || !strings.Contains(got, "cannot ask the user") {
		t.Fatalf("Usage with spawn_agent should describe the expanded worker tools and exclusions, got:\n%s", got)
	}
	if strings.Contains(got, "unless those tools are explicitly available") {
		t.Fatalf("Usage with spawn_agent should not imply side-effect tools can be passed through, got:\n%s", got)
	}
	for _, absent := range []string{"`send_message`", "`wait_until`"} {
		if strings.Contains(got, absent) {
			t.Fatalf("Usage without %s should not mention it, got:\n%s", absent, got)
		}
	}

	got = provider.Usage(context.Background(), SessionContext{}, availableToolsForTest(ToolSpawnAgent(), ToolSendMessage(), ToolWaitUntil(), ToolGetBackgroundStatus()))
	assertUsageItemsAreBulleted(t, got)
	for _, want := range []string{"`spawn_agent`", "`send_message`", "`wait_until`", "`get_background_status`"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Usage with %s should mention it, got:\n%s", want, got)
		}
	}
	for _, absent := range []string{"`list_agents`", "`list_background`", "`kill_background`", "`search_messages`"} {
		if strings.Contains(got, absent) {
			t.Fatalf("Usage without %s should not mention it, got:\n%s", absent, got)
		}
	}

	got = provider.Usage(context.Background(), SessionContext{}, availableToolsForTest(ToolSpawnAgent(), ToolSendMessage(), ToolWaitUntil(), ToolListAgents(), ToolListBackground(), ToolGetBackgroundStatus(), ToolKillBackground(), ToolSearchMessages()))
	assertUsageItemsAreBulleted(t, got)
	for _, want := range []string{"`spawn_agent`", "`send_message`", "`wait_until`", "`list_agents`", "`list_background`", "`get_background_status`", "`kill_background`", "`search_messages`"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Usage with %s should mention it, got:\n%s", want, got)
		}
	}

	got = provider.Usage(context.Background(), SessionContext{}, availableToolsForTest(ToolSendMessage(), ToolListAgents()))
	assertUsageItemsAreBulleted(t, got)
	for _, want := range []string{"`send_message`", "`list_agents`"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Usage with %s but without spawn_agent should mention it, got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "`spawn_agent`") {
		t.Fatalf("Usage without spawn_agent should not mention it, got:\n%s", got)
	}

	got = provider.Usage(context.Background(), SessionContext{}, availableToolsForTest(ToolWaitUntil(), ToolListAgents(), ToolListBackground(), ToolGetBackgroundStatus()))
	assertUsageItemsAreBulleted(t, got)
	for _, want := range []string{"`wait_until`", "`list_agents`", "`list_background`", "`get_background_status`"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Usage with %s should mention it, got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "run_in_background") {
		t.Fatalf("Usage without spawn_agent/send_message should not suggest run_in_background, got:\n%s", got)
	}
}

func TestContainerProviderUsageNamesWorkdirPath(t *testing.T) {
	t.Parallel()

	provider := NewContainerProvider(nil, nil, nil, "")
	got := provider.Usage(context.Background(), SessionContext{
		WorkdirPath: "/data/site",
	}, availableToolsForTest(ToolRead(), ToolExec()))
	for _, want := range []string{`"/data/site"`, "working directory", "commands run there by default"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Usage with a workdir binding should contain %q, got:\n%s", want, got)
		}
	}

	unbound := provider.Usage(context.Background(), SessionContext{}, availableToolsForTest(ToolRead(), ToolExec()))
	if strings.Contains(unbound, "working directory is") {
		t.Fatalf("Usage without a workdir binding must not name one, got:\n%s", unbound)
	}
}
