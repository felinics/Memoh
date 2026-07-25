package account

import (
	"context"
	"errors"
	"testing"
	"time"
)

type testAccountStore struct {
	created        CreateInput
	record         Record
	getErr         error
	adminUpdated   AdminUpdate
	profileUpdated ProfileUpdate
}

type testTitleModelValidator struct {
	valid bool
	err   error
}

func (v testTitleModelValidator) IsValidTitleModel(context.Context, string) (bool, error) {
	return v.valid, v.err
}

func TestCreatePersistsAccountWithoutProvisioningProviderInstances(t *testing.T) {
	t.Parallel()

	store := &testAccountStore{}
	service := NewService(nil, store)
	account, err := service.Create(context.Background(), "user-1", CreateAccountRequest{
		Username: "alice",
		Password: "secret",
		Role:     "member",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if account.ID != "user-1" || store.created.UserID != "user-1" || store.created.Username != "alice" {
		t.Fatalf("created account = %#v, input = %#v", account, store.created)
	}
}

func (s *testAccountStore) GetByUserID(context.Context, string) (Record, error) {
	return s.record, s.getErr
}

func (*testAccountStore) GetByIdentity(context.Context, string) (Record, error) {
	return Record{}, errors.New("not implemented")
}

func (*testAccountStore) List(context.Context) ([]Record, error) { return nil, nil }

func (*testAccountStore) Search(context.Context, string, int) ([]Record, error) {
	return nil, nil
}

func (*testAccountStore) CreateUser(context.Context, CreateUserInput) (Record, error) {
	return Record{}, errors.New("not implemented")
}

func (s *testAccountStore) CreateAccount(_ context.Context, input CreateInput) (Record, error) {
	s.created = input
	now := time.Now()
	return Record{
		ID:              input.UserID,
		Username:        input.Username,
		Email:           input.Email,
		Role:            input.Role,
		DisplayName:     input.DisplayName,
		AvatarURL:       input.AvatarURL,
		PasswordHash:    input.PasswordHash,
		HasPasswordHash: true,
		IsActive:        input.IsActive,
		CreatedAt:       now,
		UpdatedAt:       now,
	}, nil
}
func (*testAccountStore) UpdateLastLogin(context.Context, string) error { return nil }
func (s *testAccountStore) UpdateAdmin(_ context.Context, input AdminUpdate) (Record, error) {
	s.adminUpdated = input
	return s.record, nil
}

func (s *testAccountStore) UpdateProfile(_ context.Context, input ProfileUpdate) (Record, error) {
	s.profileUpdated = input
	s.record.TitleModelID = input.TitleModelID
	return s.record, nil
}

func (*testAccountStore) UpdatePassword(context.Context, PasswordUpdate) error {
	return errors.New("not implemented")
}

func (*testAccountStore) RemoveMember(context.Context, string) error {
	return errors.New("not implemented")
}

func TestValidateSessionAndIsAdminRequireActiveAccount(t *testing.T) {
	store := &testAccountStore{record: Record{ID: "user-1", Role: "admin", IsActive: false}}
	svc := NewService(nil, store)

	if err := svc.ValidateSession(context.Background(), "user-1"); !errors.Is(err, ErrInactiveAccount) {
		t.Fatalf("ValidateSession() error = %v, want ErrInactiveAccount", err)
	}
	isAdmin, err := svc.IsAdmin(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("IsAdmin() error = %v", err)
	}
	if isAdmin {
		t.Fatal("inactive account must not retain admin authority")
	}

	store.record.IsActive = true
	if err := svc.ValidateSession(context.Background(), "user-1"); err != nil {
		t.Fatalf("ValidateSession() active error = %v", err)
	}
	isAdmin, err = svc.IsAdmin(context.Background(), "user-1")
	if err != nil || !isAdmin {
		t.Fatalf("IsAdmin() active = %v, %v", isAdmin, err)
	}
}

func TestHasActiveTeamMembership(t *testing.T) {
	tests := []struct {
		name   string
		record Record
		getErr error
		want   bool
	}{
		{name: "active", record: Record{ID: "user-1", IsActive: true}, want: true},
		{name: "inactive", record: Record{ID: "user-1", IsActive: false}},
		{name: "missing", getErr: ErrAccountNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(nil, &testAccountStore{record: tt.record, getErr: tt.getErr})
			got, err := svc.HasActiveTeamMembership(t.Context(), "user-1")
			if err != nil {
				t.Fatalf("HasActiveTeamMembership() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("HasActiveTeamMembership() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUpdateAdminLeavesMembershipStateUnspecified(t *testing.T) {
	store := &testAccountStore{record: Record{ID: "user-1", Role: "member", IsActive: false}}
	svc := NewService(nil, store)
	role := "admin"

	if _, err := svc.UpdateAdmin(context.Background(), "user-1", UpdateAccountRequest{Role: &role}); err != nil {
		t.Fatalf("UpdateAdmin() error = %v", err)
	}
	if store.adminUpdated.IsActive != nil {
		t.Fatalf("role-only update supplied membership state %v", *store.adminUpdated.IsActive)
	}
}

func TestUpdateProfileValidatesAndPersistsTitleModel(t *testing.T) {
	modelID := "11111111-1111-1111-1111-111111111111"
	store := &testAccountStore{
		record: Record{ID: "user-1", Username: "alice", Timezone: "UTC"},
	}
	svc := NewService(nil, store, testTitleModelValidator{valid: true})

	account, err := svc.UpdateProfile(context.Background(), "user-1", UpdateProfileRequest{TitleModelID: &modelID})
	if err != nil {
		t.Fatalf("UpdateProfile() error = %v", err)
	}
	if store.profileUpdated.TitleModelID != modelID || account.TitleModelID != modelID {
		t.Fatalf("title model was not persisted: input=%q account=%q", store.profileUpdated.TitleModelID, account.TitleModelID)
	}
}

func TestUpdateProfileRejectsInvalidTitleModel(t *testing.T) {
	modelID := "11111111-1111-1111-1111-111111111111"
	store := &testAccountStore{record: Record{ID: "user-1", Username: "alice", Timezone: "UTC"}}
	svc := NewService(nil, store, testTitleModelValidator{})

	_, err := svc.UpdateProfile(context.Background(), "user-1", UpdateProfileRequest{TitleModelID: &modelID})
	if !errors.Is(err, ErrInvalidTitleModel) {
		t.Fatalf("UpdateProfile() error = %v, want ErrInvalidTitleModel", err)
	}
}
