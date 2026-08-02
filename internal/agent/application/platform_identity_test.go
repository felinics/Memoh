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
		`<identity channel="telegram" user_id="12345" username="@memoh_bot"/>`,
	}, "\n")
	if got != want {
		t.Fatalf("unexpected XML:\nwant:\n%s\n\ngot:\n%s", want, got)
	}
}

func TestBuildPlatformIdentityLineAllowsOnlyModelSafeAttrs(t *testing.T) {
	t.Parallel()

	got := buildPlatformIdentityLine(PlatformIdentity{
		Platform: "telegram",
		SelfIdentity: map[string]any{
			"display name": `Memoh <Bot>`,
			"username":     "memoh",
			"avatar_url":   "https://api.telegram.org/file/bot123:secret/avatar.jpg",
			"bot_token":    "123:secret",
		},
	})

	want := `<identity channel="telegram" display_name="Memoh &lt;Bot&gt;" username="@memoh"/>`
	if got != want {
		t.Fatalf("unexpected identity line:\nwant: %s\ngot:  %s", want, got)
	}
	if strings.Contains(got, "secret") || strings.Contains(got, "avatar_url") {
		t.Fatalf("secret-bearing identity data reached prompt: %s", got)
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
