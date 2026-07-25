package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/memohai/memoh/domains/iam/account"
	"github.com/memohai/memoh/internal/config"
)

type accountCounterFake struct {
	count int64
	err   error
}

func (f accountCounterFake) CountAccounts(context.Context) (int64, error) {
	return f.count, f.err
}

type accountCreatorFake struct {
	account.Store
	createdUser    account.CreateUserInput
	createdAccount account.CreateInput
}

func (f *accountCreatorFake) CreateUser(_ context.Context, input account.CreateUserInput) (account.Record, error) {
	f.createdUser = input
	return account.Record{ID: "user-1"}, nil
}

func (f *accountCreatorFake) CreateAccount(_ context.Context, input account.CreateInput) (account.Record, error) {
	f.createdAccount = input
	return account.Record{ID: input.UserID}, nil
}

func TestParseMigrateCommand(t *testing.T) {
	for _, command := range []string{"up", "status", "verify", "upgrade-v2"} {
		t.Run(command, func(t *testing.T) {
			got, err := parseMigrateCommand([]string{command})
			if err != nil {
				t.Fatalf("parseMigrateCommand() error = %v", err)
			}
			if got != command {
				t.Fatalf("parseMigrateCommand() = %q, want %q", got, command)
			}
		})
	}
	for _, args := range [][]string{nil, {"down"}, {"force", "1"}, {"up", "extra"}} {
		if _, err := parseMigrateCommand(args); err == nil {
			t.Fatalf("parseMigrateCommand(%v) error = nil, want error", args)
		}
	}
}

func TestRunAccountCommandValidatesRecoveryInputBeforeOpeningDatabase(t *testing.T) {
	t.Setenv("CONFIG_PATH", "/path/that/does/not/exist")

	for name, tc := range map[string]struct {
		args     []string
		password string
		want     string
	}{
		"command":  {args: []string{"unknown"}, want: "usage:"},
		"identity": {args: []string{"recover-admin", " "}, password: "secret", want: "username or email is required"},
		"password": {args: []string{"recover-admin", "admin"}, password: "\n", want: "new password is required"},
		"limit":    {args: []string{"recover-admin", "admin"}, password: strings.Repeat("x", 4097), want: "password exceeds"},
	} {
		t.Run(name, func(t *testing.T) {
			err := runAccountCommand(tc.args, strings.NewReader(tc.password))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("runAccountCommand() error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestEnsureAdminUserSkipsExistingInstallation(t *testing.T) {
	creator := &accountCreatorFake{}
	err := ensureAdminUser(
		context.Background(),
		slog.New(slog.DiscardHandler),
		accountCounterFake{count: 1},
		creator,
		nil,
		config.Config{},
	)
	if err != nil {
		t.Fatalf("ensureAdminUser() error = %v", err)
	}
	if creator.createdAccount.UserID != "" {
		t.Fatalf("CreateAccount() was called for an existing installation: %#v", creator.createdAccount)
	}
}

func TestEnsureAdminUserCreatesFirstAdminThroughAccountsPort(t *testing.T) {
	creator := &accountCreatorFake{}
	cfg := config.Config{
		Admin: config.AdminConfig{
			Username: " admin ",
			Password: "secret",
			Email:    " admin@example.com ",
		},
		Workspace: config.WorkspaceConfig{DataRoot: "/srv/memoh"},
	}
	err := ensureAdminUser(
		context.Background(),
		slog.New(slog.DiscardHandler),
		accountCounterFake{},
		creator,
		nil,
		cfg,
	)
	if err != nil {
		t.Fatalf("ensureAdminUser() error = %v", err)
	}
	if !creator.createdUser.IsActive || string(creator.createdUser.Metadata) != "{}" {
		t.Fatalf("CreateUser() input = %#v", creator.createdUser)
	}
	input := creator.createdAccount
	if input.UserID != "user-1" || input.Username != "admin" || input.Email != "admin@example.com" || input.Role != "admin" || !input.IsActive {
		t.Fatalf("CreateAccount() input = %#v", input)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(input.PasswordHash), []byte("secret")); err != nil {
		t.Fatalf("CreateAccount() password hash does not match: %v", err)
	}
}

func TestEnsureAdminUserWrapsCountFailure(t *testing.T) {
	sentinel := errors.New("database unavailable")
	err := ensureAdminUser(
		context.Background(),
		slog.New(slog.DiscardHandler),
		accountCounterFake{err: sentinel},
		&accountCreatorFake{},
		nil,
		config.Config{},
	)
	if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "count accounts") {
		t.Fatalf("ensureAdminUser() error = %v", err)
	}
}
