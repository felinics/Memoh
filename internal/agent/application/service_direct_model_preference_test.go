package application

import (
	"context"
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	session "github.com/felinics/memoh/internal/chat/thread"
)

type preferenceCatalogDriver struct {
	external.Driver
	catalog external.ModelCatalog
}

func (d preferenceCatalogDriver) ModelCatalog(context.Context, string, string) (external.ModelCatalog, error) {
	return d.catalog, nil
}

func TestDirectModelPreferenceSurvivesServiceRestart(t *testing.T) {
	for _, runtimeType := range []string{session.RuntimeCodex, session.RuntimeClaudeCode} {
		t.Run(runtimeType, func(t *testing.T) {
			const sid = "00000000-0000-0000-0000-000000000610"
			catalog := external.ModelCatalog{ConfiguredModelID: "default", ConfiguredReasoningEffort: "medium", Models: []external.ModelOption{{ID: "chosen", DefaultReasoningEffort: "medium", ReasoningEfforts: []external.ReasoningEffortOption{{ID: "medium"}, {ID: "high"}}}}}
			fake := &modelSelectionFakeQueries{}
			svc := newModelSelectionService(t, fake)
			svc.externalDrivers = map[string]external.Driver{runtimeType: preferenceCatalogDriver{catalog: catalog}}
			sess := session.Thread{ID: sid, BotID: "bot", RuntimeType: runtimeType}
			req, err := svc.applyDirectModelPreference(context.Background(), ChatRequest{BotID: "bot", Model: "chosen", ReasoningEffort: "high"}, sess)
			if err != nil {
				t.Fatal(err)
			}
			if req.Model != "chosen" || req.ReasoningEffort != "high" {
				t.Fatalf("request=%+v", req)
			}
			if len(fake.updatedPrefs) != 1 {
				t.Fatal(fake.updatedPrefs)
			}
			stored := fake.updatedPrefs[0]
			if stored.PreferredChatModelID.Valid {
				t.Fatal("external ID entered native FK")
			}
			// A fresh service and fresh view recover exclusively from persisted columns.
			sess.PreferredExternalModelID = stored.PreferredExternalModelID.String
			sess.PreferredReasoningEffort = stored.PreferredReasoningEffort.String
			fresh := newModelSelectionService(t, &modelSelectionFakeQueries{})
			resumed, err := fresh.applyDirectModelPreference(context.Background(), ChatRequest{BotID: "bot"}, sess)
			if err != nil || resumed.Model != "chosen" || resumed.ReasoningEffort != "high" {
				t.Fatalf("resume=%+v err=%v", resumed, err)
			}
		})
	}
}

func TestDirectModelSwitchReconcilesAgainstNewModel(t *testing.T) {
	catalog := external.ModelCatalog{Models: []external.ModelOption{
		{ID: "A", DefaultReasoningEffort: "medium", ReasoningEfforts: []external.ReasoningEffortOption{{ID: "medium"}, {ID: "high"}}},
		{ID: "B", DefaultReasoningEffort: "low", ReasoningEfforts: []external.ReasoningEffortOption{{ID: "low"}}},
	}}
	id, effort, err := reconcileDirectPair(catalog, "B", "high")
	if err != nil || id != "B" || effort != "low" {
		t.Fatalf("pair=%s/%s err=%v", id, effort, err)
	}
	if _, _, err = reconcileDirectPair(catalog, "", ""); err == nil {
		t.Fatal("accepted empty model without a configured or advertised default")
	}
	if _, _, err = reconcileDirectPair(catalog, "missing", ""); err == nil {
		t.Fatal("accepted missing model")
	}
}

func TestDirectModelPreferencePreservesClaudeResolvedModel(t *testing.T) {
	const full = "claude-opus-5"
	catalog := external.ModelCatalog{ConfiguredModelID: full, Models: []external.ModelOption{{ID: "opus", ResolvedModelID: full, ReasoningEfforts: []external.ReasoningEffortOption{{ID: "high"}}}}}
	for _, requested := range []string{full, "", "opus"} {
		t.Run("model="+requested, func(t *testing.T) {
			fake := &modelSelectionFakeQueries{}
			svc := newModelSelectionService(t, fake)
			svc.externalDrivers = map[string]external.Driver{session.RuntimeClaudeCode: preferenceCatalogDriver{catalog: catalog}}
			sess := session.Thread{ID: "00000000-0000-0000-0000-000000000610", BotID: "bot", RuntimeType: session.RuntimeClaudeCode}
			req, err := svc.applyDirectModelPreference(context.Background(), ChatRequest{BotID: "bot", Model: requested, ReasoningEffort: "high"}, sess)
			want := requested
			if want == "" {
				want = full
			}
			if err != nil || req.Model != want || req.ReasoningEffort != "high" {
				t.Fatalf("request=%+v err=%v", req, err)
			}
			if len(fake.updatedPrefs) != 1 || fake.updatedPrefs[0].PreferredExternalModelID.String != want {
				t.Fatalf("stored=%+v", fake.updatedPrefs)
			}
		})
	}
	if _, _, err := reconcileDirectPair(catalog, "unknown", "high"); err == nil {
		t.Fatal("accepted an unadvertised model")
	}
}
