package modelchecker

import (
	"context"
	"testing"

	"github.com/felinics/memoh/internal/healthcheck"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/oauthctx"
)

type testLookup struct {
	models BotModels
}

func (l testLookup) GetBotModelIDs(context.Context, string) (BotModels, error) {
	return l.models, nil
}

type testProber struct {
	t          *testing.T
	wantUserID string
}

func (p testProber) Test(ctx context.Context, id string) (models.TestResponse, error) {
	if got := oauthctx.UserIDFromContext(ctx); got != p.wantUserID {
		p.t.Fatalf("expected oauth user id %q, got %q", p.wantUserID, got)
	}
	if id != "model-chat-1" {
		p.t.Fatalf("expected model id %q, got %q", "model-chat-1", id)
	}
	return models.TestResponse{
		Status:    models.TestStatusOK,
		Reachable: true,
		Message:   "ok",
	}, nil
}

func TestCheckerListChecksInjectsOwnerUserIDIntoProbeContext(t *testing.T) {
	t.Parallel()

	checker := NewChecker(nil, testLookup{
		models: BotModels{
			OwnerUserID: "user-123",
			ChatModelID: "model-chat-1",
		},
	}, testProber{
		t:          t,
		wantUserID: "user-123",
	})

	items := checker.ListChecks(context.Background(), "bot-1")
	if len(items) != 1 {
		t.Fatalf("expected 1 check result, got %d", len(items))
	}
	if items[0].Status != healthcheck.StatusOK {
		t.Fatalf("expected status %q, got %q", healthcheck.StatusOK, items[0].Status)
	}
}
