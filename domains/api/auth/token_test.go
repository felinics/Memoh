package auth

import "testing"

func TestNewTokenConfig(t *testing.T) {
	t.Parallel()

	cfg, err := NewTokenConfig("secret", "24h")
	if err != nil {
		t.Fatalf("NewTokenConfig returned error: %v", err)
	}
	if cfg.Secret != "secret" {
		t.Fatalf("Secret = %q, want secret", cfg.Secret)
	}
	if cfg.ExpiresIn.Hours() != 24 {
		t.Fatalf("ExpiresIn = %v, want 24h", cfg.ExpiresIn)
	}
}

func TestNewTokenConfigRequiresSecret(t *testing.T) {
	t.Parallel()

	if _, err := NewTokenConfig(" ", "24h"); err == nil {
		t.Fatal("expected missing jwt secret error")
	}
}

func TestNewTokenConfigRejectsInvalidExpiresIn(t *testing.T) {
	t.Parallel()

	if _, err := NewTokenConfig("secret", "not-a-duration"); err == nil {
		t.Fatal("expected invalid jwt expires in error")
	}
}
