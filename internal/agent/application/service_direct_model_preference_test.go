package application

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/runtimekind"
)

type preferenceCatalogDriver struct {
	external.Driver
	runtime      string
	catalog      external.ModelCatalog
	calls        *int
	projectPaths *[]string
}

func (d preferenceCatalogDriver) RuntimeType() string { return d.runtime }

func (preferenceCatalogDriver) Caps() external.Caps {
	return external.Caps{}
}

func (d preferenceCatalogDriver) ModelCatalog(_ context.Context, request external.ModelCatalogRequest) (external.ModelCatalog, error) {
	if d.calls != nil {
		*d.calls++
	}
	if d.projectPaths != nil {
		*d.projectPaths = append(*d.projectPaths, request.ProjectPath)
	}
	return d.catalog, nil
}

func TestDirectModelPreferenceSurvivesServiceRestart(t *testing.T) {
	for _, runtimeType := range []string{string(runtimekind.Codex), string(runtimekind.ClaudeCode), string(runtimekind.GrokBuild)} {
		t.Run(runtimeType, func(t *testing.T) {
			const sid = "00000000-0000-0000-0000-000000000610"
			catalog := external.ModelCatalog{ConfiguredModelID: "default", ConfiguredReasoningEffort: "medium", Models: []external.ModelOption{{ID: "chosen", DefaultReasoningEffort: "medium", ReasoningEfforts: []external.ReasoningEffortOption{{ID: "medium"}, {ID: "high"}}}}}
			fake := &modelSelectionFakeQueries{}
			svc := newModelSelectionService(t, fake)
			svc.runtimes = testRuntimes(t, preferenceCatalogDriver{runtime: runtimeType, catalog: catalog})
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
			fresh.runtimes = testRuntimes(t, preferenceCatalogDriver{runtime: runtimeType, catalog: catalog})
			resumed, err := fresh.applyDirectModelPreference(context.Background(), ChatRequest{BotID: "bot"}, sess)
			if err != nil || resumed.Model != "chosen" || resumed.ReasoningEffort != "high" {
				t.Fatalf("resume=%+v err=%v", resumed, err)
			}
		})
	}
}

func TestDirectDefaultSelectionReplacesSavedModel(t *testing.T) {
	for _, tc := range []struct {
		name, runtime, configured, selected string
	}{
		{"Claude configured default", string(runtimekind.ClaudeCode), "B", "B"},
		{"Codex configured default", string(runtimekind.Codex), "B", "B"},
		{"Codex advertised default", string(runtimekind.Codex), "", "B"},
		{"Claude opaque default", string(runtimekind.ClaudeCode), "", "default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const sid = "00000000-0000-0000-0000-000000000612"
			catalog := external.ModelCatalog{ConfiguredModelID: tc.configured, Models: []external.ModelOption{
				{ID: "A", ReasoningEfforts: []external.ReasoningEffortOption{{ID: "high"}}},
				{ID: tc.selected, Default: true, DefaultReasoningEffort: "medium", ReasoningEfforts: []external.ReasoningEffortOption{{ID: "medium"}, {ID: "high"}}},
			}}
			fake := &modelSelectionFakeQueries{session: sqlc.BotSession{
				ID: db.ParseUUIDOrEmpty(sid), RuntimeType: tc.runtime,
				Metadata: []byte(`{"project_path":"/data/old"}`), RuntimeMetadata: []byte(`{"project_path":"/data/project"}`),
				PreferredExternalModelID: pgtype.Text{String: "A", Valid: true},
				PreferredReasoningEffort: pgtype.Text{String: "high", Valid: true},
			}}
			svc := newModelSelectionService(t, fake)
			var projectPaths []string
			svc.runtimes = testRuntimes(t, preferenceCatalogDriver{runtime: tc.runtime, catalog: catalog, projectPaths: &projectPaths})
			// The picker resolves Default to an explicit ID before either the
			// PATCH or a send. Both entry points must replace the saved A/high.
			model, effort := tc.selected, "medium"
			if err := svc.PatchSessionModelPreference(context.Background(), "bot", sid, &model, &effort, ""); err != nil {
				t.Fatal(err)
			}
			if len(fake.patchedPrefs) != 1 || fake.patchedPrefs[0].PreferredExternalModelID.String != model || fake.patchedPrefs[0].PreferredReasoningEffort.String != effort {
				t.Fatalf("PATCH=%+v", fake.patchedPrefs)
			}
			sess := session.Thread{ID: sid, BotID: "bot", RuntimeType: tc.runtime, PreferredExternalModelID: "A", PreferredReasoningEffort: "high"}
			sess.Metadata = map[string]any{"project_path": "/data/old"}
			sess.RuntimeMetadata = map[string]any{"project_path": "/data/project"}
			req, err := svc.applyDirectModelPreference(context.Background(), ChatRequest{BotID: "bot", Model: model, ReasoningEffort: effort}, sess)
			if err != nil || req.Model != model || req.ReasoningEffort != effort {
				t.Fatalf("send=%+v err=%v", req, err)
			}
			if len(fake.updatedPrefs) != 1 {
				t.Fatalf("send write-back=%+v", fake.updatedPrefs)
			}
			if len(projectPaths) != 2 || projectPaths[0] != "/data/project" || projectPaths[1] != "/data/project" {
				t.Fatalf("PATCH and send must discover settings in the runtime project: %v", projectPaths)
			}
			stored := fake.updatedPrefs[0]
			sess.PreferredExternalModelID = stored.PreferredExternalModelID.String
			sess.PreferredReasoningEffort = stored.PreferredReasoningEffort.String
			fresh := newModelSelectionService(t, &modelSelectionFakeQueries{})
			fresh.runtimes = testRuntimes(t, preferenceCatalogDriver{runtime: tc.runtime, catalog: catalog})
			resumed, err := fresh.applyDirectModelPreference(context.Background(), ChatRequest{BotID: "bot"}, sess)
			if err != nil || resumed.Model != model || resumed.ReasoningEffort != effort {
				t.Fatalf("resume=%+v err=%v", resumed, err)
			}
		})
	}
}

func TestDirectModelSwitchReconcilesAgainstNewModel(t *testing.T) {
	catalog := external.ModelCatalog{Models: []external.ModelOption{
		{ID: "A", DefaultReasoningEffort: "medium", ReasoningEfforts: []external.ReasoningEffortOption{{ID: "medium"}, {ID: "high"}}},
		{ID: "B", DefaultReasoningEffort: "low", ReasoningEfforts: []external.ReasoningEffortOption{{ID: "low"}}},
		{ID: "no-default", ReasoningEfforts: []external.ReasoningEffortOption{{ID: "low"}, {ID: "max"}}},
	}}
	id, effort, err := reconcileDirectPair(catalog, "B", "high")
	if err != nil || id != "B" || effort != "low" {
		t.Fatalf("pair=%s/%s err=%v", id, effort, err)
	}
	// Without a model default the effort stays unset until a level is picked.
	for sent, want := range map[string]string{"": "", "max": "max", "high": ""} {
		if _, effort, err = reconcileDirectPair(catalog, "no-default", sent); err != nil || effort != want {
			t.Fatalf("no-default effort %q = %q, want %q (err=%v)", sent, effort, want, err)
		}
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
			svc.runtimes = testRuntimes(t, preferenceCatalogDriver{runtime: string(runtimekind.ClaudeCode), catalog: catalog})
			sess := session.Thread{ID: "00000000-0000-0000-0000-000000000610", BotID: "bot", RuntimeType: string(runtimekind.ClaudeCode)}
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
