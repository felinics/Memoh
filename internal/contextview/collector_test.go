package contextview

import (
	"context"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

func TestSystemPromptCollectorSplitsCanonicalToolUsage(t *testing.T) {
	t.Parallel()

	toolUsage := "## Tool usage\nUse tools carefully."
	system := "Base system.\n\n" + toolUsage + "\n\n## Workspace instruction files\nAGENTS.md"
	frags, err := (&SystemPromptCollector{}).Collect(context.Background(), CollectRequest{
		Scope:  contextfrag.Scope{BotID: "bot-1"},
		Config: SystemPromptConfig{System: system, ToolUsage: toolUsage},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := []contextfrag.Kind{frags[0].Kind, frags[1].Kind, frags[2].Kind}; !reflect.DeepEqual(got, []contextfrag.Kind{
		contextfrag.KindSystemPrompt, contextfrag.KindToolUsage, contextfrag.KindWorkspaceInstruction,
	}) {
		t.Fatalf("kinds = %v", got)
	}
	if got := collectedSystemText(frags); got != system {
		t.Fatalf("system bytes = %q, want %q", got, system)
	}
}

func TestSystemPromptCollectorPreservesNoncanonicalBytes(t *testing.T) {
	t.Parallel()

	for _, system := range []string{"  system with outer space\n", "   "} {
		frags, err := (&SystemPromptCollector{}).Collect(context.Background(), CollectRequest{Config: SystemPromptConfig{System: system}})
		if err != nil {
			t.Fatal(err)
		}
		if len(frags) != 1 || collectedSystemText(frags) != system {
			t.Fatalf("frags = %#v, want exact system %q", frags, system)
		}
	}
}

func TestSystemPromptCollectorEmptySystem(t *testing.T) {
	t.Parallel()

	frags, err := (&SystemPromptCollector{}).Collect(context.Background(), CollectRequest{})
	if err != nil || len(frags) != 0 {
		t.Fatalf("frags = %#v, err = %v", frags, err)
	}
}

func TestCurrentUserCollectorPreservesQueryBytes(t *testing.T) {
	t.Parallel()

	query := "  keep leading and trailing space \n"
	frags, err := (&CurrentUserCollector{}).Collect(context.Background(), CollectRequest{
		Config: CurrentUserConfig{Query: query},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(frags) != 1 || frags[0].Slot != contextfrag.SlotCurrentUser {
		t.Fatalf("frags = %#v", frags)
	}
	msg := contextfrag.FragMessage(frags[0])
	if msg == nil || len(msg.Content) != 1 {
		t.Fatalf("message = %#v", msg)
	}
	text, ok := msg.Content[0].(sdk.TextPart)
	if !ok || text.Text != query {
		t.Fatalf("text = %#v, want %q", msg.Content[0], query)
	}
}

func TestCurrentUserCollectorOnlyOmitsEmptyString(t *testing.T) {
	t.Parallel()

	empty, err := (&CurrentUserCollector{}).Collect(context.Background(), CollectRequest{Config: CurrentUserConfig{Query: ""}})
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty frags = %#v, err = %v", empty, err)
	}
	spaces, err := (&CurrentUserCollector{}).Collect(context.Background(), CollectRequest{Config: CurrentUserConfig{Query: "  "}})
	if err != nil || len(spaces) != 1 {
		t.Fatalf("space frags = %#v, err = %v", spaces, err)
	}
}

func TestInlineImageCollectorFiltersEmptyImages(t *testing.T) {
	t.Parallel()

	images := []sdk.ImagePart{
		{Image: "data:image/png;base64,abc", MediaType: "image/png"},
		{Image: "", MediaType: "image/png"},
		{Image: "data:image/jpeg;base64,def", MediaType: "image/jpeg"},
	}
	frags, err := (&InlineImageCollector{}).Collect(context.Background(), CollectRequest{Config: InlineImageConfig{Images: images}})
	if err != nil {
		t.Fatal(err)
	}
	if len(frags) != 1 || len(frags[0].Parts) != 2 {
		t.Fatalf("frags = %#v", frags)
	}
	if frags[0].Kind != contextfrag.KindNativeImage || frags[0].Slot != contextfrag.SlotCurrentUser {
		t.Fatalf("image frag metadata = %#v", frags[0])
	}
}

func collectedSystemText(frags []contextfrag.ContextFrag) string {
	var parts []string
	for _, frag := range frags {
		for _, part := range frag.Parts {
			if part.Type == contextfrag.PartText {
				parts = append(parts, part.Text)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}
