package schedule

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

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
