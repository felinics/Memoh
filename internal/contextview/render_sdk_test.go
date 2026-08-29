package contextview

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

func TestSDKRendererPreservesSystemSectionBytes(t *testing.T) {
	t.Parallel()
	frags := []contextfrag.ContextFrag{
		{
			ID: "sys.1", Kind: contextfrag.KindSystemPrompt, Role: sdk.MessageRoleSystem,
			Slot: contextfrag.SlotSystem, Priority: 20, Trust: contextfrag.TrustSystem,
			Parts: []contextfrag.Part{{Type: contextfrag.PartText, Text: "  first\n"}},
		},
		{
			ID: "sys.2", Kind: contextfrag.KindBotIdentity, Role: sdk.MessageRoleSystem,
			Slot: contextfrag.SlotSystem, Priority: 40, Trust: contextfrag.TrustSystem,
			Parts: []contextfrag.Part{{Type: contextfrag.PartText, Text: ""}},
		},
		{
			ID: "sys.3", Kind: contextfrag.KindHookContext, Role: sdk.MessageRoleSystem,
			Slot: contextfrag.SlotSystem, Priority: 80, Trust: contextfrag.TrustSystem,
			Parts: []contextfrag.Part{{Type: contextfrag.PartText, Text: " hook tail "}},
		},
	}
	payload, _ := renderSDKPayload(t, frags, placementFor(frags))
	if payload.System != "  first\n\n\n\n\n hook tail " {
		t.Fatalf("system = %q", payload.System)
	}
}

func TestSDKRendererSystemRenderGroupsUseDeclaredJoiners(t *testing.T) {
	t.Parallel()

	frags := []contextfrag.ContextFrag{
		textFrag("system.prompt", contextfrag.SlotSystem, contextfrag.KindSystemPrompt, sdk.MessageRoleSystem, "prompt"),
		groupedSystemTextFrag(
			"system.platform_identity.header",
			"system.platform_identity",
			"\n",
			"## Platform Identities\n\nKnown identities.\n",
		),
		groupedSystemTextFrag(
			"system.platform_identity.telegram",
			"system.platform_identity",
			"\n",
			`<identity channel="telegram" username="@memoh"/>`,
		),
		groupedSystemTextFrag(
			"system.platform_identity.微信",
			"system.platform_identity",
			"\n",
			`<identity channel="weixin" username="小明"/>`,
		),
		groupedSystemTextFrag(
			"system.skills.header",
			"system.skills",
			"\n",
			"## Skills\n\n2 skill(s) available:",
		),
		groupedSystemTextFrag("system.skill.alpha", "system.skills", "\n", "- **alpha**: first"),
		groupedSystemTextFrag("system.skill.技能", "system.skills", "\n", "- **技能**: 第二"),
		textFrag(
			"system.workspace_file.AGENTS.md",
			contextfrag.SlotSystem,
			contextfrag.KindWorkspaceInstruction,
			sdk.MessageRoleSystem,
			"## AGENTS.md\n\ninstructions",
		),
	}

	payload, _ := renderSDKPayload(t, frags, placementFor(frags))
	want := "prompt\n\n" +
		"## Platform Identities\n\nKnown identities.\n\n" +
		"<identity channel=\"telegram\" username=\"@memoh\"/>\n" +
		"<identity channel=\"weixin\" username=\"小明\"/>\n\n" +
		"## Skills\n\n2 skill(s) available:\n" +
		"- **alpha**: first\n" +
		"- **技能**: 第二\n\n" +
		"## AGENTS.md\n\ninstructions"
	if payload.System != want {
		t.Fatalf("system = %q, want grouped system bytes %q", payload.System, want)
	}
}

func TestSDKRendererPreservesMessagesAndMaterializesRawCurrentInput(t *testing.T) {
	t.Parallel()
	query := "  current request \n"
	images := []sdk.ImagePart{{Image: "data:image/png;base64,abc", MediaType: "image/png"}}
	frags := []contextfrag.ContextFrag{
		messageFrag("history", sdk.AssistantMessage("answer")),
		contextfrag.MessageFrag(contextfrag.MessageFragInput{ID: "current", Message: sdk.UserMessage(query), Kind: contextfrag.KindCurrentUserMessage, Slot: contextfrag.SlotCurrentUser}),
		contextfrag.ImageFrag("images", images, contextfrag.Scope{}, contextfrag.SourceRunConfig),
	}
	payload, _ := renderSDKPayload(t, frags, placementFor(frags))
	want := []sdk.Message{sdk.AssistantMessage("answer"), sdk.UserMessage(query, images[0])}
	assertMessagesEqual(t, payload.Messages, want)
	if payload.Query != "" || len(payload.InlineImages) != 0 {
		t.Fatalf("unmaterialized current input: %#v", payload)
	}
}

func TestSDKRendererImageOnlyInputInjectsLatestUser(t *testing.T) {
	t.Parallel()
	image := sdk.ImagePart{Image: "data:image/png;base64,abc", MediaType: "image/png"}
	frags := []contextfrag.ContextFrag{
		messageFrag("user", sdk.UserMessage("pipeline query")),
		messageFrag("assistant", sdk.AssistantMessage("working")),
		contextfrag.ImageFrag("images", []sdk.ImagePart{image}, contextfrag.Scope{}, contextfrag.SourceRunConfig),
	}
	payload, _ := renderSDKPayload(t, frags, placementFor(frags))
	assertMessagesEqual(t, payload.Messages, []sdk.Message{
		sdk.UserMessage("pipeline query", image),
		sdk.AssistantMessage("working"),
	})
}

func TestSDKRendererValidatesPlacementAndHashesDeterministically(t *testing.T) {
	t.Parallel()
	frag := textFrag("system", contextfrag.SlotSystem, contextfrag.KindSystemPrompt, sdk.MessageRoleSystem, "system")
	first, firstHash := renderSDKPayload(t, []contextfrag.ContextFrag{frag}, placementFor([]contextfrag.ContextFrag{frag}))
	second, secondHash := renderSDKPayload(t, []contextfrag.ContextFrag{frag}, placementFor([]contextfrag.ContextFrag{frag}))
	if firstHash == "" || firstHash != secondHash || !reflect.DeepEqual(first, second) {
		t.Fatalf("first = %#v (%q), second = %#v (%q)", first, firstHash, second, secondHash)
	}
	_, err := (&SDKMessagesRenderer{}).Render(context.Background(), RenderInput{Selected: []contextfrag.ContextFrag{frag}})
	if err == nil {
		t.Fatal("expected missing placement error")
	}
}

func renderSDKPayload(t *testing.T, frags []contextfrag.ContextFrag, placement PlacementPlan) (*SDKRenderedPayload, string) {
	t.Helper()
	rendered, err := (&SDKMessagesRenderer{}).Render(context.Background(), RenderInput{
		Intent: contextfrag.IntentRunConfigPreProvider, Selected: frags, Placement: placement, Target: contextfrag.RenderSDKMessages,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, ok := rendered.Data.(*SDKRenderedPayload)
	if !ok {
		t.Fatalf("payload type = %T", rendered.Data)
	}
	return payload, rendered.ContentHash
}

func placementFor(frags []contextfrag.ContextFrag) PlacementPlan {
	items := make([]PlacementItem, len(frags))
	for i, frag := range frags {
		items[i] = PlacementItem{FragID: frag.ID, Slot: frag.Slot, Position: i, CacheHint: frag.CacheClass, Ref: frag.Ref}
	}
	return PlacementPlan{Items: items, FirstVolatileIndex: len(items)}
}

func assertMessagesEqual(t *testing.T, got, want []sdk.Message) {
	t.Helper()
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("messages = %s, want %s", gotJSON, wantJSON)
	}
}

func groupedSystemTextFrag(id, groupID, joiner, text string) contextfrag.ContextFrag {
	return contextfrag.TextFrag(contextfrag.TextFragInput{
		ID: id, Kind: contextfrag.KindSystemPrompt, Role: sdk.MessageRoleSystem,
		Slot: contextfrag.SlotSystem, Text: text, Priority: 20,
		CacheClass: contextfrag.CacheStable, Trust: contextfrag.TrustSystem,
		Source: "test", Collector: "test",
		Render: contextfrag.RenderPolicy{
			Format:      contextfrag.RenderMarkdown,
			GroupID:     groupID,
			GroupJoiner: joiner,
		},
	})
}
