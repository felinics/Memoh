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
