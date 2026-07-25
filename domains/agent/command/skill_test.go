package command

import (
	"context"
	"strings"
	"testing"
)

type fakeSkillLoader struct {
	skills []Skill
}

func (f *fakeSkillLoader) LoadSkills(context.Context, string) ([]Skill, error) {
	return f.skills, nil
}

// fakeRuntimeSkillLoader additionally implements RuntimeSkillLister, modeling
// the production adapter that exposes the runtime-usable safe catalog.
type fakeRuntimeSkillLoader struct {
	fakeSkillLoader
	runtime []Skill
}

func (f *fakeRuntimeSkillLoader) ListRuntimeSkills(context.Context, string) ([]Skill, error) {
	return f.runtime, nil
}

func newSkillTestHandler(loader SkillLoader) *Handler {
	return NewHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, loader, nil)
}

// TestSkillListUpgradesToTapToActivate pins the tap-to-activate contract: with
// a RuntimeSkillLister-capable loader, /skill list rows carry the pre-encoded
// skill-activation callback (and no ItemAction, so no-button channels derive
// no trailer), and the body carries the typeable activation hint.
func TestSkillListUpgradesToTapToActivate(t *testing.T) {
	t.Parallel()

	loader := &fakeRuntimeSkillLoader{
		fakeSkillLoader: fakeSkillLoader{skills: []Skill{{Name: "legacy-only", Description: "must not appear"}}},
		runtime: []Skill{
			{Name: "flutter-adding-home-screen-widgets", Description: "Adds home screen widgets."},
		},
	}
	h := newSkillTestHandler(loader)
	res, err := h.ExecuteResult(context.Background(), ExecuteInput{
		BotID:             "bot-1",
		ChannelIdentityID: "ci-1",
		Text:              "/skill list",
		ChannelType:       "telegram",
	})
	if err != nil {
		t.Fatalf("ExecuteResult(/skill list) error = %v", err)
	}
	if res.Interactive == nil || res.Interactive.List == nil {
		t.Fatalf("result has no list view: %+v", res)
	}
	items := res.Interactive.List.Items
	if len(items) != 1 {
		t.Fatalf("list items = %d, want 1 (runtime catalog, not legacy LoadSkills)", len(items))
	}
	if items[0].Label != "flutter-adding-home-screen-widgets" {
		t.Fatalf("item label = %q", items[0].Label)
	}
	if want := EncodeSkillActivateCallback("flutter-adding-home-screen-widgets"); items[0].Callback != want {
		t.Fatalf("item callback = %q, want %q", items[0].Callback, want)
	}
	if items[0].Action != nil {
		t.Fatalf("item action = %+v, want nil (activation must not round-trip as a /skill sub-command)", items[0].Action)
	}
	if !strings.Contains(res.Text, "/<skill-name>") {
		t.Fatalf("body text misses the typeable activation hint: %q", res.Text)
	}
}

// TestSkillListLegacyLoaderStaysDisplayOnly pins the fallback: a loader without
// RuntimeSkillLister keeps the previous display-only listing.
func TestSkillListLegacyLoaderStaysDisplayOnly(t *testing.T) {
	t.Parallel()

	h := newSkillTestHandler(&fakeSkillLoader{skills: []Skill{{Name: "alpha", Description: "First skill"}}})
	res, err := h.ExecuteResult(context.Background(), ExecuteInput{
		BotID:             "bot-1",
		ChannelIdentityID: "ci-1",
		Text:              "/skill list",
		ChannelType:       "telegram",
	})
	if err != nil {
		t.Fatalf("ExecuteResult(/skill list) error = %v", err)
	}
	if res.Interactive == nil || res.Interactive.List == nil {
		t.Fatalf("result has no list view: %+v", res)
	}
	items := res.Interactive.List.Items
	if len(items) != 1 || items[0].Callback != "" || items[0].Action != nil {
		t.Fatalf("legacy items should stay display-only, got %+v", items)
	}
	if strings.Contains(res.Text, "/<skill-name>") {
		t.Fatalf("legacy body should not advertise activation: %q", res.Text)
	}
}

// TestSkillListEmptyRuntimeCatalog pins the empty state on the upgraded path.
func TestSkillListEmptyRuntimeCatalog(t *testing.T) {
	t.Parallel()

	h := newSkillTestHandler(&fakeRuntimeSkillLoader{
		fakeSkillLoader: fakeSkillLoader{skills: []Skill{{Name: "legacy-only"}}},
	})
	res, err := h.ExecuteResult(context.Background(), ExecuteInput{
		BotID:             "bot-1",
		ChannelIdentityID: "ci-1",
		Text:              "/skill list",
		ChannelType:       "telegram",
	})
	if err != nil {
		t.Fatalf("ExecuteResult(/skill list) error = %v", err)
	}
	if res.Interactive != nil {
		t.Fatalf("empty catalog should render plain text, got %+v", res.Interactive)
	}
}
