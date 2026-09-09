package command

import (
	"errors"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/i18n"
	"github.com/felinics/memoh/internal/reasoning"
	"github.com/felinics/memoh/internal/settings"
)

func TestFriendlyCommandErrorLocalizesReasoningPolicyErrors(t *testing.T) {
	t.Parallel()

	handler := &Handler{}
	invalid := &settings.InvalidReasoningEffortError{
		Effort: reasoning.EffortXHigh,
		Options: reasoning.Options{
			Supported:     true,
			CanDisable:    true,
			Efforts:       []string{reasoning.EffortLow, reasoning.EffortHigh},
			DefaultEffort: reasoning.EffortLow,
		},
	}
	if got, want := handler.friendlyCommandError(i18n.New("en"), "settings", invalid),
		`Unknown level "xhigh" — available levels: off, low, high.`; got != want {
		t.Fatalf("invalid effort message = %q, want %q", got, want)
	}

	unavailable := errors.Join(settings.ErrReasoningOptionsUnavailable, errors.New("SECRET provider diagnostic"))
	got := handler.friendlyCommandError(i18n.New("en"), "settings", unavailable)
	if want := "Reasoning isn't available right now."; got != want {
		t.Fatalf("unavailable message = %q, want %q", got, want)
	}
	if strings.Contains(got, "SECRET") {
		t.Fatalf("unavailable message leaked private diagnostic: %q", got)
	}
}
