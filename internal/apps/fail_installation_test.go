package apps

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
)

func TestFailInstallationReturnsCauseWithoutLogging(t *testing.T) {
	var buf bytes.Buffer
	h := newHarness()
	svc := h.service
	svc.logger = slog.New(slog.NewJSONHandler(&buf, nil))
	inst, err := h.store.Upsert(context.Background(), UpsertInstallation{BotID: testBotID, RegistryID: "r", AppID: "a", Status: StatusInstalling})
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("publish failed")

	got := svc.failInstallation(context.Background(), inst, cause)
	if !errors.Is(got, cause) {
		t.Fatalf("err = %v, want cause", got)
	}
	if buf.Len() != 0 {
		t.Fatalf("failInstallation logged %q; the caller records the returned cause", buf.String())
	}
}
