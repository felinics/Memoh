package contextview

import (
	"context"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

type legacyInput struct {
	system    string
	messages  []sdk.Message
	query     string
	images    []sdk.ImagePart
	toolUsage string
}

func TestContextViewMatchesLegacyProviderBytes(t *testing.T) {
	t.Parallel()
	toolUsage := "## Tool usage\nUse tools carefully."
	image := sdk.ImagePart{Image: "data:image/png;base64,abc", MediaType: "image/png"}
	cases := map[string]legacyInput{
		"basic":                      {system: "You are helpful.", messages: []sdk.Message{sdk.UserMessage("hello"), sdk.AssistantMessage("hi")}, query: "What is 2+2?"},
		"system whitespace":          {system: "  preserve outer system bytes\n", messages: []sdk.Message{sdk.UserMessage("hello")}},
		"tool usage and workspace":   {system: "Base.\n\n" + toolUsage + "\n\n## Workspace instruction files\nAGENTS.md", toolUsage: toolUsage, query: "continue"},
		"query whitespace and image": {system: "vision", query: "  inspect this \n", images: []sdk.ImagePart{image}},
		"image only pipeline":        {system: "vision", messages: []sdk.Message{sdk.UserMessage("embedded query"), sdk.AssistantMessage("working")}, images: []sdk.ImagePart{image}},
		"memory bytes":               {system: "system", messages: []sdk.Message{sdk.UserMessage("<memory>raw & unescaped</memory>"), sdk.UserMessage("current")}},
		"hook bytes":                 {system: "system\n\n[Hook Context: BeforePromptBuild]\n  exact hook output \n"},
	}
	for name, input := range cases {
		name, input := name, input
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assertContextViewEquivalent(t, input)
		})
	}
}

func assertContextViewEquivalent(t *testing.T, input legacyInput) {
	t.Helper()
	want := legacyProviderMessages(input.messages, input.query, input.images)
	builder := NewBuilder(
		NewMapCollectorRegistry(&SystemPromptCollector{}, &HistoryMessagesCollector{}, &CurrentUserCollector{}, &InlineImageCollector{}),
		&FragmentSelector{}, StablePrefixPlacer{}, NewMapRendererRegistry(&SDKMessagesRenderer{}),
	)
	view, err := builder.Build(context.Background(), BuildInput{
		Intent: contextfrag.IntentRunConfigPreProvider,
		Sources: []SourceSpec{
			{Name: systemPromptCollectorName, Config: SystemPromptConfig{System: input.system, ToolUsage: input.toolUsage}},
			{Name: historyMessagesCollectorName, Config: HistoryMessagesConfig{Messages: input.messages}},
			{Name: currentUserCollectorName, Config: CurrentUserConfig{Query: input.query}},
			{Name: inlineImagesCollectorName, Config: InlineImageConfig{Images: input.images}},
		},
		Targets: []contextfrag.RenderTarget{contextfrag.RenderSDKMessages},
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := view.Rendered[contextfrag.RenderSDKMessages].Data.(*SDKRenderedPayload)
	if payload.System != input.system {
		t.Fatalf("system = %q, want %q", payload.System, input.system)
	}
	assertMessagesEqual(t, payload.Messages, want)
}

func legacyProviderMessages(messages []sdk.Message, query string, images []sdk.ImagePart) []sdk.Message {
	var out []sdk.Message
	if messages != nil {
		out = make([]sdk.Message, len(messages))
		for i, message := range messages {
			out[i] = cloneSDKMessage(message)
		}
	}
	parts := make([]sdk.MessagePart, 0, len(images))
	for _, image := range images {
		if image.Image != "" {
			parts = append(parts, image)
		}
	}
	if query != "" {
		return append(out, sdk.UserMessage(query, parts...))
	}
	if len(parts) == 0 {
		return out
	}
	for i := len(out) - 1; i >= 0; i-- {
		if out[i].Role == sdk.MessageRoleUser {
			out[i].Content = append(out[i].Content, parts...)
			return out
		}
	}
	return append(out, sdk.UserMessage("", parts...))
}
