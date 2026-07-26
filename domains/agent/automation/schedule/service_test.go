package schedule

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/robfig/cron/v3"
)

type runOnceSchedule struct {
	nextCalled atomic.Bool
}

func (s *runOnceSchedule) Next(now time.Time) time.Time {
	if s.nextCalled.Swap(true) {
		return time.Time{}
	}
	return now
}

func TestServiceLifecycle(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := NewService(logger, nil, nil, nil, "", time.UTC)

	started := make(chan struct{})
	stopped := make(chan struct{})
	var runs atomic.Int32
	svc.cron.Schedule(&runOnceSchedule{}, cron.FuncJob(func() {
		runCtx, cancel, ok := svc.jobContext(time.Minute)
		if !ok {
			return
		}
		defer cancel()
		runs.Add(1)
		close(started)
		<-runCtx.Done()
		close(stopped)
	}))

	select {
	case <-started:
		t.Fatal("NewService started the cron scheduler")
	default:
	}

	if err := svc.Start(t.Context()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := svc.Start(t.Context()); err != nil {
		t.Fatalf("second Start() error = %v", err)
	}
	waitForSignal(t, started, "scheduled job to start")

	if err := svc.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	waitForSignal(t, stopped, "active schedule job to stop")
	if err := svc.Shutdown(t.Context()); err != nil {
		t.Fatalf("second Shutdown(): %v", err)
	}
	if err := svc.Start(t.Context()); err == nil {
		t.Fatal("Start() after Shutdown() succeeded")
	}
	if got := runs.Load(); got != 1 {
		t.Fatalf("job runs = %d, want 1", got)
	}
}

func waitForSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	select {
	case <-signal:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for %s", description)
	}
}

func TestGenerateTriggerToken(t *testing.T) {
	secret := "test-secret-key-for-schedule"
	svc := &Service{
		jwtSecret: secret,
		logger:    slog.Default(),
	}
	userID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

	tok, err := svc.generateTriggerToken(userID)
	if err != nil {
		t.Fatalf("generateTriggerToken returned error: %v", err)
	}
	if !strings.HasPrefix(tok, "Bearer ") {
		t.Fatalf("expected Bearer prefix, got: %s", tok)
	}

	raw := strings.TrimPrefix(tok, "Bearer ")
	parsed, err := jwt.Parse(raw, func(_ *jwt.Token) (any, error) {
		return []byte(secret), nil
	})
	if err != nil {
		t.Fatalf("failed to parse JWT: %v", err)
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatal("expected MapClaims")
	}
	if sub, _ := claims["sub"].(string); sub != userID {
		t.Errorf("expected sub=%s, got=%s", userID, sub)
	}
	if uid, _ := claims["user_id"].(string); uid != userID {
		t.Errorf("expected user_id=%s, got=%s", userID, uid)
	}
	exp, _ := claims["exp"].(float64)
	if exp == 0 {
		t.Fatal("expected non-zero exp")
	}
	expTime := time.Unix(int64(exp), 0)
	if expTime.Before(time.Now().Add(9 * time.Minute)) {
		t.Error("token expires too soon")
	}
}

func TestGenerateTriggerToken_EmptySecret(t *testing.T) {
	svc := &Service{
		jwtSecret: "",
		logger:    slog.Default(),
	}
	_, err := svc.generateTriggerToken("user-123")
	if err == nil {
		t.Fatal("expected error for empty secret")
	}
}

func TestGenerateTriggerToken_EmptyUserID(t *testing.T) {
	svc := &Service{
		jwtSecret: "some-secret",
		logger:    slog.Default(),
	}
	_, err := svc.generateTriggerToken("")
	if err == nil {
		t.Fatal("expected error for empty user ID")
	}
}
