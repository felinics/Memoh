package account

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

type Recovery struct {
	store AdminRecoveryStore
}

func NewRecovery(store AdminRecoveryStore) *Recovery {
	return &Recovery{store: store}
}

func (r *Recovery) RecoverAdmin(ctx context.Context, identity, password string) error {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return errors.New("username or email is required")
	}
	if password == "" {
		return errors.New("new password is required")
	}
	if r == nil || r.store == nil {
		return errors.New("account recovery store not configured")
	}
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	return r.store.RecoverAdmin(ctx, identity, string(hashed))
}
