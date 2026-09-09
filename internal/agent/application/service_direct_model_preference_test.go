package application

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

type preferenceCatalogDriver struct {
	external.Driver
	catalog external.ModelCatalog
	calls   *int
}

func (d preferenceCatalogDriver) ModelCatalog(context.Context, string, string) (external.ModelCatalog, error) {
	if d.calls != nil {
		*d.calls++
	}
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

func TestDirectDefaultSelectionReplacesSavedModel(t *testing.T) {
	for _, tc := range []struct {
		name, runtime, configured, selected string
	}{
		{"Claude configured default", session.RuntimeClaudeCode, "B", "B"},
		{"Codex configured default", session.RuntimeCodex, "B", "B"},
		{"Codex advertised default", session.RuntimeCodex, "", "B"},
		{"Claude opaque default", session.RuntimeClaudeCode, "", "default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const sid = "00000000-0000-0000-0000-000000000612"
			catalog := external.ModelCatalog{ConfiguredModelID: tc.configured, Models: []external.ModelOption{
				{ID: "A", ReasoningEfforts: []external.ReasoningEffortOption{{ID: "high"}}},
				{ID: tc.selected, Default: true, DefaultReasoningEffort: "medium", ReasoningEfforts: []external.ReasoningEffortOption{{ID: "medium"}, {ID: "high"}}},
			}}
			fake := &modelSelectionFakeQueries{session: sqlc.BotSession{
				ID: db.ParseUUIDOrEmpty(sid), RuntimeType: tc.runtime,
				PreferredExternalModelID: pgtype.Text{String: "A", Valid: true},
				PreferredReasoningEffort: pgtype.Text{String: "high", Valid: true},
			}}
			svc := newModelSelectionService(t, fake)
			svc.externalDrivers = map[string]external.Driver{tc.runtime: preferenceCatalogDriver{catalog: catalog}}
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
			req, err := svc.applyDirectModelPreference(context.Background(), ChatRequest{BotID: "bot", Model: model, ReasoningEffort: effort}, sess)
			if err != nil || req.Model != model || req.ReasoningEffort != effort {
				t.Fatalf("send=%+v err=%v", req, err)
			}
			if len(fake.updatedPrefs) != 1 {
				t.Fatalf("send write-back=%+v", fake.updatedPrefs)
			}
			stored := fake.updatedPrefs[0]
			sess.PreferredExternalModelID = stored.PreferredExternalModelID.String
			sess.PreferredReasoningEffort = stored.PreferredReasoningEffort.String
			fresh := newModelSelectionService(t, &modelSelectionFakeQueries{})
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

// A remembered pair repeated on every send must not re-run catalog discovery
// (Claude Code spawns a CLI for it), but each carried send still advances the
// revision so a stale picker PATCH cannot overwrite it.
func TestDirectModelPreferenceRepeatedPairSkipsCatalog(t *testing.T) {
	catalog := external.ModelCatalog{Models: []external.ModelOption{{ID: "A", DefaultReasoningEffort: "medium", ReasoningEfforts: []external.ReasoningEffortOption{{ID: "medium"}, {ID: "high"}}}}}
	calls := 0
	fake := &modelSelectionFakeQueries{}
	svc := newModelSelectionService(t, fake)
	svc.externalDrivers = map[string]external.Driver{session.RuntimeCodex: preferenceCatalogDriver{catalog: catalog, calls: &calls}}
	sess := session.Thread{ID: "00000000-0000-0000-0000-000000000610", BotID: "bot", RuntimeType: session.RuntimeCodex, PreferredExternalModelID: "A", PreferredReasoningEffort: "high"}

	req, err := svc.applyDirectModelPreference(context.Background(), ChatRequest{BotID: "bot", Model: "A", ReasoningEffort: "high"}, sess)
	if err != nil || req.Model != "A" || req.ReasoningEffort != "high" {
		t.Fatalf("request=%+v err=%v", req, err)
	}
	if calls != 0 {
		t.Fatalf("unchanged pair re-ran catalog discovery %d time(s)", calls)
	}
	if len(fake.updatedPrefs) != 1 {
		t.Fatalf("revision must still advance on a carried send: %+v", fake.updatedPrefs)
	}

	// A changed component goes back through the catalog.
	req, err = svc.applyDirectModelPreference(context.Background(), ChatRequest{BotID: "bot", Model: "A", ReasoningEffort: "medium"}, sess)
	if err != nil || req.ReasoningEffort != "medium" {
		t.Fatalf("request=%+v err=%v", req, err)
	}
	if calls != 1 {
		t.Fatalf("changed pair skipped catalog discovery (calls=%d)", calls)
	}
}

// A failed write-back must not break the turn (#879): same best-effort
// contract as the native writeBackSessionModelPreference. The request pair
// still applies to the in-flight turn; the next send retries the persist.
func TestDirectModelPreferenceWriteFailureDoesNotBreakTurn(t *testing.T) {
	catalog := external.ModelCatalog{ConfiguredModelID: "A", ConfiguredReasoningEffort: "medium", Models: []external.ModelOption{{ID: "A", DefaultReasoningEffort: "medium", ReasoningEfforts: []external.ReasoningEffortOption{{ID: "medium"}, {ID: "high"}}}}}
	fake := &modelSelectionFakeQueries{updatePrefErr: errors.New("db down")}
	svc := newModelSelectionService(t, fake)
	svc.logger = slog.New(slog.DiscardHandler)
	svc.externalDrivers = map[string]external.Driver{session.RuntimeClaudeCode: preferenceCatalogDriver{catalog: catalog}}
	sess := session.Thread{ID: "00000000-0000-0000-0000-000000000611", BotID: "bot", RuntimeType: session.RuntimeClaudeCode, PreferredExternalModelID: "previous", PreferredReasoningEffort: "medium"}
	req, err := svc.applyDirectModelPreference(context.Background(), ChatRequest{BotID: "bot", Model: "A", ReasoningEffort: "high"}, sess)
	if err != nil {
		t.Fatalf("write-back failure broke the turn: %v", err)
	}
	if req.Model != "A" || req.ReasoningEffort != "high" {
		t.Fatalf("request=%+v", req)
	}
}
