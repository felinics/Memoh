package builtin

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/felinics/memoh/internal/memory/adapters"
)

func TestMemoryRuntimesRejectForeignAndMissingScopeBeforeMutation(t *testing.T) {
	for _, kind := range []string{"graph", "file"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			var rt Runtime = NewGraphRuntime(nil, newFakeWikiStore(), newFakeStore())
			if kind == "file" {
				rt = newFileRuntime(newFakeStore())
			}
			a, err := rt.Add(ctx, adapters.AddRequest{BotID: "bot-a", Message: "A original"})
			if err != nil {
				t.Fatal(err)
			}
			b, err := rt.Add(ctx, adapters.AddRequest{BotID: "bot-b", Message: "B original"})
			if err != nil {
				t.Fatal(err)
			}
			aID, bID := a.Results[0].ID, b.Results[0].ID
			for _, scope := range []string{"", "bot-a"} {
				if _, err := rt.Update(ctx, adapters.UpdateRequest{BotID: scope, MemoryID: bID, Memory: "intrusion"}); !errors.Is(err, adapters.ErrMemoryScope) {
					t.Fatalf("Update scope %q: %v", scope, err)
				}
				if _, err := rt.Delete(ctx, scope, bID); !errors.Is(err, adapters.ErrMemoryScope) {
					t.Fatalf("Delete scope %q: %v", scope, err)
				}
			}
			for _, ids := range [][]string{{aID, bID}, {aID, "invalid"}, {aID, "bot-a:"}, {aID, ""}} {
				if _, err := rt.DeleteBatch(ctx, "bot-a", ids); !errors.Is(err, adapters.ErrMemoryScope) {
					t.Fatalf("DeleteBatch %v: %v", ids, err)
				}
			}
			// Exercise the LLM-decided route with adversarial action IDs too.
			result := formationResult{}
			applyActions(ctx, slog.Default(), rt, "bot-a", []adapters.DecisionAction{
				{Event: actionUPDATE, ID: bID, Text: "intrusion"},
				{Event: actionDELETE, ID: bID},
			}, nil, nil, nil, nil, &result)
			if result.Updated != 0 || result.Deleted != 0 {
				t.Fatalf("foreign formation actions succeeded: %+v", result)
			}
			// Successful own-bot updates prove rejected batches touched neither row.
			for _, entry := range []struct{ bot, id, body string }{{"bot-a", aID, "A original"}, {"bot-b", bID, "B original"}} {
				found, err := rt.GetAll(ctx, adapters.GetAllRequest{BotID: entry.bot})
				if err != nil {
					t.Fatal(err)
				}
				preserved := false
				for _, item := range found.Results {
					if item.ID == entry.id && item.Memory == entry.body {
						preserved = true
					}
				}
				if !preserved {
					t.Fatalf("memory changed after rejection: %+v", found)
				}
				if _, err := rt.Update(ctx, adapters.UpdateRequest{BotID: entry.bot, MemoryID: entry.id, Memory: "own update"}); err != nil {
					t.Fatal(err)
				}
				if _, err := rt.Delete(ctx, entry.bot, entry.id); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
