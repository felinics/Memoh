package compaction

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestDoCompactionInjectsPriorContext(t *testing.T) {
	rows := machineryCorpus(t)
	stub := &stubModel{summary: "S2"}
	cfg := machineryConfig(stub, 450)
	q := &fakeQueries{
		uncompacted: rows,
		priorLogs: []ArtifactRecord{{
			ID:        uuid.NewString(),
			BotID:     cfg.BotID,
			SessionID: cfg.SessionID,
			Summary:   "earlier-segment-summary",
			Status:    "ok",
		}},
	}
	svc := newMachineryService(q)

	if _, err := svc.RunCompactionSync(context.Background(), cfg); err != nil {
		t.Fatalf("RunCompactionSync: %v", err)
	}
	if !strings.Contains(stub.prompt, "prior_context") || !strings.Contains(stub.prompt, "earlier-segment-summary") {
		t.Fatalf("prior summary not injected as prior context:\n%s", stub.prompt)
	}
}

func TestDoCompactionPriorContextUsesOnlyActiveArtifactFrontier(t *testing.T) {
	parentID := uuid.NewString()
	activeID := uuid.NewString()
	stub := &stubModel{summary: "S2"}
	cfg := machineryConfig(stub, 450)
	botID := cfg.BotID
	sessionID := cfg.SessionID
	coverage := testCoverageJSON(t, "covered-row")
	q := &fakeQueries{
		uncompacted: machineryCorpus(t),
		priorLogs: []ArtifactRecord{
			{
				ID:           parentID,
				BotID:        botID,
				SessionID:    sessionID,
				Status:       "ok",
				Summary:      "stale-parent-summary",
				Coverage:     coverage,
				SupersededBy: activeID,
				SupersededAt: time.Unix(1, 0),
			},
			{
				ID:        activeID,
				BotID:     botID,
				SessionID: sessionID,
				Status:    "ok",
				Summary:   "active-frontier-summary",
				Coverage:  coverage,
				ParentIDs: []string{parentID},
			},
		},
	}

	if _, err := newMachineryService(q).RunCompactionSync(context.Background(), cfg); err != nil {
		t.Fatalf("RunCompactionSync: %v", err)
	}
	if !strings.Contains(stub.prompt, "active-frontier-summary") || strings.Contains(stub.prompt, "stale-parent-summary") {
		t.Fatalf("prior context did not use the active frontier:\n%s", stub.prompt)
	}
}
