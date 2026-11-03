package grok

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/external"
)

func TestModelStateFollowsWhatGrokReports(t *testing.T) {
	grok := &fakeGrok{models: map[string]any{"currentModelId": "grok-b", "availableModels": []any{map[string]any{"modelId": "grok-b"}}}}
	srv := newTestProcess(t, grok)
	ctx := context.Background()

	// Nothing pushed yet: the catalog is read from Grok, not from the
	// snapshot initialize carried.
	if state := srv.modelState(ctx); state["currentModelId"] != "grok-b" {
		t.Fatalf("modelState() = %v, want what models/list answers", state)
	}
	if err := grok.conn.SendNotification(ctx, modelsUpdateMethod, map[string]any{"currentModelId": "grok-c", "availableModels": []any{map[string]any{"modelId": "grok-c"}}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for srv.modelState(ctx)["currentModelId"] != "grok-c" {
		if time.Now().After(deadline) {
			t.Fatalf("modelState() = %v, want the catalog Grok pushed", srv.modelState(ctx))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestGatewayStatusReportsOnlyTheMemohTools(t *testing.T) {
	grok := &fakeGrok{}
	srv := newTestProcess(t, grok)
	ctx := context.Background()
	if _, ok := srv.gatewayStatus(ctx, "s1", 50*time.Millisecond); ok {
		t.Fatal("a status Grok has not reported must not be invented")
	}
	for _, report := range []map[string]any{
		{"sessionId": "s1", "name": "other_server", "status": mcpStatusUnavailable, "reason": "handshake_failed"},
		{"sessionId": "s1", "name": "Memoh_Tools", "status": mcpStatusUnavailable, "reason": "handshake_failed"},
	} {
		if err := grok.conn.SendNotification(ctx, mcpStatusMethod, report); err != nil {
			t.Fatal(err)
		}
	}
	status, ok := srv.gatewayStatus(ctx, "s1", 5*time.Second)
	if !ok || status.Status != mcpStatusUnavailable || status.Reason != "handshake_failed" {
		t.Fatalf("gatewayStatus() = %+v, %v; want Grok's report on the Memoh tools", status, ok)
	}
}

func TestCompactForkAndInfoSpeakGroksMethods(t *testing.T) {
	grok := &fakeGrok{}
	srv := newTestProcess(t, grok)
	ctx := context.Background()

	if err := srv.compact(ctx, "s1"); err != nil || len(grok.compacted) != 1 || grok.compacted[0] != "s1" {
		t.Fatalf("compact: err=%v compacted=%v", err, grok.compacted)
	}
	forked, err := srv.fork(ctx, "s1", "/data/project")
	if err != nil || forked != "fork-1" {
		t.Fatalf("fork() = %q, %v", forked, err)
	}
	if params := grok.forked[0]; params["sourceSessionId"] != "s1" || params["sourceCwd"] != "/data/project" || params["newCwd"] != "/data/project" {
		t.Fatalf("fork params = %v", params)
	}
	info, err := srv.sessionInfo(ctx, "s1")
	if err != nil || info.Model != "grok-a" || info.Context.Used != 1234 || info.Context.Total != 256000 {
		t.Fatalf("sessionInfo() = %+v, %v", info, err)
	}
}

// Grok copies a session whole, so only the latest turn can anchor a fork.
func TestForkThreadRefusesAnEarlierTurn(t *testing.T) {
	d := &Driver{}
	metadata := map[string]any{metadataSessionIDKey: "s1", metadataLastPromptIDKey: "p2"}
	_, err := d.ForkThread(context.Background(), "bot", "agent", metadata, "p1")
	var failure *external.Failure
	if !errors.As(err, &failure) || failure.Kind != external.FailureForkLatestOnly {
		t.Fatalf("ForkThread(earlier turn) error = %v, want the latest-only fork failure", err)
	}
	if _, err := d.ForkThread(context.Background(), "bot", "agent", map[string]any{}, ""); err == nil {
		t.Fatal("a conversation without a Grok session cannot be forked")
	}
}
