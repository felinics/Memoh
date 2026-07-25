package account

import (
	"context"
	"errors"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

type recoveryStoreFake struct {
	identity string
	hash     string
	err      error
}

func (s *recoveryStoreFake) RecoverAdmin(_ context.Context, identity, hash string) error {
	s.identity = identity
	s.hash = hash
	return s.err
}

func TestRecoveryHashesPasswordAndNormalizesIdentity(t *testing.T) {
	store := &recoveryStoreFake{}
	err := NewRecovery(store).RecoverAdmin(t.Context(), " admin@example.com ", "secret")
	if err != nil {
		t.Fatalf("RecoverAdmin() error = %v", err)
	}
	if store.identity != "admin@example.com" {
		t.Fatalf("identity = %q", store.identity)
	}
	if store.hash == "secret" || bcrypt.CompareHashAndPassword([]byte(store.hash), []byte("secret")) != nil {
		t.Fatal("password was not bcrypt hashed")
	}
}

func TestRecoveryValidationAndStoreError(t *testing.T) {
	sentinel := errors.New("database unavailable")
	tests := []struct {
		name     string
		recovery *Recovery
		identity string
		password string
		wantErr  error
	}{
		{name: "identity", recovery: NewRecovery(&recoveryStoreFake{}), password: "secret"},
		{name: "password", recovery: NewRecovery(&recoveryStoreFake{}), identity: "admin"},
		{name: "store", recovery: NewRecovery(&recoveryStoreFake{err: sentinel}), identity: "admin", password: "secret", wantErr: sentinel},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.recovery.RecoverAdmin(t.Context(), tt.identity, tt.password)
			if err == nil {
				t.Fatal("RecoverAdmin() error = nil")
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("RecoverAdmin() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}
