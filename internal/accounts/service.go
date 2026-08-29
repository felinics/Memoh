package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/felinics/memoh/internal/db"
	dbstore "github.com/felinics/memoh/internal/db/store"
	tzutil "github.com/felinics/memoh/internal/timezone"
)

// Service provides account (credential) management for users.
type Service struct {
	store  dbstore.AccountStore
	logger *slog.Logger
}

var (
	ErrInvalidPassword    = errors.New("invalid password")
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrInactiveAccount    = errors.New("account is inactive")
	ErrInvalidTitleModel  = errors.New("invalid title model")
)

// NewService creates a new accounts service.
func NewService(log *slog.Logger, store dbstore.AccountStore) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		store:  store,
		logger: log.With(slog.String("service", "accounts")),
	}
}

// Get returns an account by user id.
func (s *Service) Get(ctx context.Context, userID string) (Account, error) {
	if s.store == nil {
		return Account{}, errors.New("account store not configured")
	}
	row, err := s.store.GetByUserID(ctx, userID)
	if err != nil {
		return Account{}, err
	}
	return toAccount(row), nil
}

// ValidateSession rejects JWTs whose backing account or current membership is
// no longer active.
func (s *Service) ValidateSession(ctx context.Context, userID string) error {
	if s.store == nil {
		return errors.New("account store not configured")
	}
	row, err := s.store.GetByUserID(ctx, userID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return ErrInactiveAccount
		}
		return err
	}
	if !row.IsActive {
		return ErrInactiveAccount
	}
	return nil
}

// Login authenticates by identity (username or email) and password.
func (s *Service) Login(ctx context.Context, identity, password string) (Account, error) {
	if s.store == nil {
		return Account{}, errors.New("account store not configured")
	}
	identity = strings.TrimSpace(identity)
	if identity == "" || strings.TrimSpace(password) == "" {
		return Account{}, ErrInvalidCredentials
	}
	row, err := s.store.GetByIdentity(ctx, identity)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return Account{}, ErrInvalidCredentials
		}
		return Account{}, err
	}
	if !row.IsActive {
		return Account{}, ErrInactiveAccount
	}
	if !row.HasPasswordHash {
		return Account{}, ErrInvalidCredentials
	}
	if err := bcrypt.CompareHashAndPassword([]byte(row.PasswordHash), []byte(password)); err != nil {
		return Account{}, ErrInvalidCredentials
	}
	if err := s.store.UpdateLastLogin(ctx, row.ID); err != nil {
		if s.logger != nil {
			s.logger.Warn("touch last login failed", slog.Any("error", err))
		}
	}
	return toAccount(row), nil
}

// ListAccounts returns all accounts.
func (s *Service) ListAccounts(ctx context.Context) ([]Account, error) {
	if s.store == nil {
		return nil, errors.New("account store not configured")
	}
	rows, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]Account, 0, len(rows))
	for _, row := range rows {
		items = append(items, toAccount(row))
	}
	return items, nil
}

// SearchAccounts returns account candidates for UI search.
func (s *Service) SearchAccounts(ctx context.Context, query string, limit int) ([]Account, error) {
	if s.store == nil {
		return nil, errors.New("account store not configured")
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.store.Search(ctx, strings.TrimSpace(query), int32(limit)) //nolint:gosec // limit is capped above
	if err != nil {
		return nil, err
	}
	items := make([]Account, 0, len(rows))
	for _, row := range rows {
		items = append(items, toAccount(row))
	}
	return items, nil
}

// IsAdmin checks if the user has admin role.
func (s *Service) IsAdmin(ctx context.Context, userID string) (bool, error) {
	if s.store == nil {
		return false, errors.New("account store not configured")
	}
	row, err := s.store.GetByUserID(ctx, userID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return row.IsActive && isAdminRole(row.Role), nil
}

// Create creates a new account for an existing user.
func (s *Service) Create(ctx context.Context, userID string, req CreateAccountRequest) (Account, error) {
	if s.store == nil {
		return Account{}, errors.New("account store not configured")
	}
	username := strings.TrimSpace(req.Username)
	if username == "" {
		return Account{}, errors.New("username is required")
	}
	password := strings.TrimSpace(req.Password)
	if password == "" {
		return Account{}, errors.New("password is required")
	}
	role, err := normalizeRole(req.Role)
	if err != nil {
		return Account{}, err
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return Account{}, err
	}

	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		displayName = username
	}
	avatarURL := strings.TrimSpace(req.AvatarURL)
	email := strings.TrimSpace(req.Email)
	isActive := true
	if req.IsActive != nil {
		isActive = *req.IsActive
	}

	row, err := s.store.CreateAccount(ctx, dbstore.CreateAccountInput{
		UserID:       userID,
		Username:     username,
		Email:        email,
		PasswordHash: string(hashed),
		Role:         role,
		DisplayName:  displayName,
		AvatarURL:    avatarURL,
		IsActive:     isActive,
	})
	if err != nil {
		return Account{}, err
	}
	account := toAccount(row)
	return account, nil
}

// CreateHuman keeps compatibility with older call sites.
//
// Deprecated: use Create directly.
func (s *Service) CreateHuman(ctx context.Context, userID string, req CreateAccountRequest) (Account, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		if s.store == nil {
			return Account{}, errors.New("account store not configured")
		}
		userRow, err := s.store.CreateUser(ctx, dbstore.CreateUserInput{
			IsActive: true,
			Metadata: []byte("{}"),
		})
		if err != nil {
			return Account{}, err
		}
		if strings.TrimSpace(userRow.ID) == "" {
			return Account{}, errors.New("create user: invalid id")
		}
		userID = userRow.ID
	}
	return s.Create(ctx, userID, req)
}

// UpdateAdmin updates account fields as admin.
func (s *Service) UpdateAdmin(ctx context.Context, userID string, req UpdateAccountRequest) (Account, error) {
	if s.store == nil {
		return Account{}, errors.New("account store not configured")
	}
	existing, err := s.store.GetByUserID(ctx, userID)
	if err != nil {
		return Account{}, err
	}
	role := existing.Role
	if req.Role != nil {
		role, err = normalizeRole(*req.Role)
		if err != nil {
			return Account{}, err
		}
	}
	row, err := s.store.UpdateAdmin(ctx, dbstore.UpdateAccountAdminInput{
		UserID:   userID,
		Role:     role,
		IsActive: req.IsActive,
	})
	if err != nil {
		return Account{}, err
	}
	return toAccount(row), nil
}

// UpdateProfile updates the user's profile.
func (s *Service) UpdateProfile(ctx context.Context, userID string, req UpdateProfileRequest) (Account, error) {
	if s.store == nil {
		return Account{}, errors.New("account store not configured")
	}
	existing, err := s.store.GetByUserID(ctx, userID)
	if err != nil {
		return Account{}, err
	}
	displayName := strings.TrimSpace(existing.DisplayName)
	if req.DisplayName != nil {
		displayName = strings.TrimSpace(*req.DisplayName)
	}
	if displayName == "" {
		displayName = strings.TrimSpace(existing.Username)
	}
	avatarURL := strings.TrimSpace(existing.AvatarURL)
	if req.AvatarURL != nil {
		avatarURL = strings.TrimSpace(*req.AvatarURL)
	}
	tzName := strings.TrimSpace(existing.Timezone)
	if req.Timezone != nil {
		resolved, _, err := tzutil.Resolve(*req.Timezone)
		if err != nil {
			return Account{}, err
		}
		tzName = resolved.String()
	}
	if tzName == "" {
		tzName = "UTC"
	}
	metadata := s.mergeMetadata(existing.Metadata, req.Metadata)
	titleModelID := strings.TrimSpace(existing.TitleModelID)
	if req.TitleModelID != nil {
		titleModelID = strings.TrimSpace(*req.TitleModelID)
		if titleModelID != "" {
			valid, err := s.store.IsValidTitleModel(ctx, titleModelID)
			if err != nil {
				return Account{}, err
			}
			if !valid {
				return Account{}, ErrInvalidTitleModel
			}
		}
	}
	row, err := s.store.UpdateProfile(ctx, dbstore.UpdateAccountProfileInput{
		UserID:       userID,
		DisplayName:  displayName,
		AvatarURL:    avatarURL,
		Timezone:     tzName,
		Metadata:     metadata,
		TitleModelID: titleModelID,
	})
	if err != nil {
		return Account{}, err
	}
	return toAccount(row), nil
}

// UpdatePassword changes the password after verifying the current one.
func (s *Service) UpdatePassword(ctx context.Context, userID, currentPassword, newPassword string) error {
	if s.store == nil {
		return errors.New("account store not configured")
	}
	if strings.TrimSpace(newPassword) == "" {
		return errors.New("new password is required")
	}
	existing, err := s.store.GetByUserID(ctx, userID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(currentPassword) == "" {
		return ErrInvalidPassword
	}
	if !existing.HasPasswordHash {
		return ErrInvalidPassword
	}
	if err := bcrypt.CompareHashAndPassword([]byte(existing.PasswordHash), []byte(currentPassword)); err != nil {
		return ErrInvalidPassword
	}
	hashed, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return s.store.UpdatePassword(ctx, dbstore.UpdateAccountPasswordInput{
		UserID:       userID,
		PasswordHash: string(hashed),
	})
}

// RemoveMember deactivates the current workspace membership without changing
// the global principal or credentials.
func (s *Service) RemoveMember(ctx context.Context, userID string) error {
	if s.store == nil {
		return errors.New("account store not configured")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return errors.New("user id is required")
	}
	return s.store.RemoveMember(ctx, userID)
}

func normalizeRole(raw string) (string, error) {
	role := strings.ToLower(strings.TrimSpace(raw))
	if role == "" {
		return "member", nil
	}
	if role != "member" && role != "admin" {
		return "", fmt.Errorf("invalid role: %s", raw)
	}
	return role, nil
}

func isAdminRole(role any) bool {
	if role == nil {
		return false
	}
	switch v := role.(type) {
	case string:
		return strings.EqualFold(v, "admin")
	case fmt.Stringer:
		return strings.EqualFold(v.String(), "admin")
	default:
		return strings.EqualFold(fmt.Sprint(v), "admin")
	}
}

func toAccount(row dbstore.AccountRecord) Account {
	username := strings.TrimSpace(row.Username)
	email := strings.TrimSpace(row.Email)
	displayName := strings.TrimSpace(row.DisplayName)
	if displayName == "" {
		displayName = username
	}
	avatarURL := strings.TrimSpace(row.AvatarURL)
	timezone := strings.TrimSpace(row.Timezone)
	var metadata map[string]any
	if row.Metadata != "" {
		_ = json.Unmarshal([]byte(row.Metadata), &metadata)
	}
	return Account{
		ID:                  row.ID,
		Username:            username,
		Email:               email,
		Role:                row.Role,
		DisplayName:         displayName,
		AvatarURL:           avatarURL,
		Timezone:            timezone,
		IsActive:            row.IsActive,
		PrincipalIsActive:   row.PrincipalActive,
		MembershipIsActive:  row.MembershipActive,
		Metadata:            metadata,
		CreatedAt:           row.CreatedAt,
		UpdatedAt:           row.UpdatedAt,
		JoinedAt:            row.JoinedAt,
		MembershipUpdatedAt: row.MembershipUpdatedAt,
		LastLoginAt:         row.LastLoginAt,
		TitleModelID:        strings.TrimSpace(row.TitleModelID),
	}
}

// mergeMetadata applies the allowlisted fields from an update request onto the
// user's existing metadata, preserving any other existing keys. Only keys
// enumerated in UpdateProfileMetadata can be written — arbitrary client keys are
// impossible because incoming is a typed struct, not free-form JSON.
func (s *Service) mergeMetadata(existing string, incoming *UpdateProfileMetadata) string {
	if incoming == nil {
		if existing == "" {
			return "{}"
		}
		return existing
	}
	base := map[string]any{}
	if existing != "" {
		if err := json.Unmarshal([]byte(existing), &base); err != nil {
			// Existing metadata is not valid JSON, so we can't safely merge into
			// it. Log loudly and heal with a clean object holding only the
			// allowlisted fields, rather than locking the user out of profile
			// updates. Safe because nothing but allowlisted keys is written here.
			s.logger.Error("existing user metadata is not valid JSON; healing with allowlisted fields", slog.Any("error", err))
			base = map[string]any{}
		}
	}
	if incoming.OnboardingCompleted != nil {
		base["onboarding_completed"] = *incoming.OnboardingCompleted
	}
	result, err := json.Marshal(base)
	if err != nil {
		return "{}"
	}
	return string(result)
}
