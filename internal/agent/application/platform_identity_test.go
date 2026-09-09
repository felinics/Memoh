package application

import (
	"strings"
	"testing"
)

func TestBuildPlatformIdentitiesXML(t *testing.T) {
	t.Parallel()

	configs := []PlatformIdentity{
		{
			ID:               "tg-1",
			Platform:         "telegram",
			ExternalIdentity: "12345",
			SelfIdentity: map[string]any{
				"user_id":  "12345",
				"username": "memoh_bot",
			},
		},
		{
			ID:               "discord-1",
			Platform:         "discord",
			ExternalIdentity: "98765",
			SelfIdentity: map[string]any{
				"name":     "Memoh & Co",
				"username": "@memoh",
			},
		},
	}

	got := buildPlatformIdentitiesXML(configs)
	want := strings.Join([]string{
		`<identity channel="discord" name="Memoh &amp; Co" username="@memoh" external_identity="98765"/>`,
		`<identity channel="telegram" user_id="12345" username="@memoh_bot" external_identity="12345"/>`,
	}, "\n")
	if got != want {
		t.Fatalf("unexpected XML:\nwant:\n%s\n\ngot:\n%s", want, got)
	}
}

func TestBuildPlatformIdentityPromptItemsPreserveSortedIDs(t *testing.T) {
	t.Parallel()

	items := buildPlatformIdentityPromptItems([]PlatformIdentity{
		{
			ID:               "telegram-2",
			Platform:         "telegram",
			ExternalIdentity: "2",
			SelfIdentity:     map[string]any{"username": "second"},
		},
		{
			ID:               "discord-1",
			Platform:         "discord",
			ExternalIdentity: "1",
			SelfIdentity:     map[string]any{"username": "first"},
		},
	})

	if len(items) != 2 {
		t.Fatalf("items = %#v, want 2", items)
	}
	if items[0].ID != "discord-1" || !strings.Contains(items[0].Text, `channel="discord"`) {
		t.Fatalf("first item = %#v, want sorted discord identity with stable ID", items[0])
	}
	if items[1].ID != "telegram-2" || !strings.Contains(items[1].Text, `channel="telegram"`) {
		t.Fatalf("second item = %#v, want sorted telegram identity with stable ID", items[1])
	}
	wantSection := platformIdentitiesIntro + "\n" + items[0].Text + "\n" + items[1].Text
	if got := buildPlatformIdentitiesSectionFromItems(items); got != wantSection {
		t.Fatalf("section = %q, want byte-equivalent %q", got, wantSection)
	}
}

func TestBuildPlatformIdentityLineNormalizesAttrs(t *testing.T) {
	t.Parallel()

	got := buildPlatformIdentityLine(PlatformIdentity{
		Platform: "telegram",
		SelfIdentity: map[string]any{
			"123id":        7,
			"display name": `Memoh <Bot>`,
			"username":     "memoh",
			"xml_name":     "reserved",
		},
	})

	want := `<identity channel="telegram" attr_123id="7" display_name="Memoh &lt;Bot&gt;" username="@memoh" attr_xml_name="reserved"/>`
	if got != want {
		t.Fatalf("unexpected identity line:\nwant: %s\ngot:  %s", want, got)
	}
}

func TestBuildPlatformIdentitiesSectionSkipsEmptyConfigs(t *testing.T) {
	t.Parallel()

	got := buildPlatformIdentitiesSection([]PlatformIdentity{{
		ID:       "local-1",
		Platform: "local",
	}})
	if got != "" {
		t.Fatalf("expected empty section, got %q", got)
	}
}
