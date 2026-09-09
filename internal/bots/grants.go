package bots

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// Grant permission scopes. manage implies every scoped permission; workspace_write
// implies workspace_read.
const (
	PermissionChat           = "chat"
	PermissionWorkspaceRead  = "workspace_read"
	PermissionWorkspaceWrite = "workspace_write"
	PermissionWorkspaceExec  = "workspace_exec"
	PermissionManage         = "manage"
)

// Grant subject types.
const (
	GrantSubjectUser     = "user"
	GrantSubjectEveryone = "everyone"
)

var (
	// ErrGrantNotFound indicates the grant does not exist for the bot.
	ErrGrantNotFound = errors.New("bot user grant not found")
	// ErrInvalidPermission indicates an unknown or empty permission set.
	ErrInvalidPermission = errors.New("invalid permission")
	// ErrInvalidGrantSubject indicates an unknown subject type.
	ErrInvalidGrantSubject = errors.New("invalid grant subject")
	// ErrGrantUserRequired indicates a user grant is missing its user id.
	ErrGrantUserRequired = errors.New("user id is required for a user grant")
	// ErrGrantOwnerConflict indicates an attempt to grant access to the bot owner.
	ErrGrantOwnerConflict = errors.New("the bot owner already has full access")
	// ErrGrantExists indicates a grant for the subject already exists.
	ErrGrantExists = errors.New("a grant for this subject already exists")
)

// UserGrant represents a workspace user (or everyone) access grant for a bot.
type UserGrant struct {
	ID              string    `json:"id"`
	BotID           string    `json:"bot_id"`
	SubjectType     string    `json:"subject_type"`
	UserID          string    `json:"user_id,omitempty"`
	UserUsername    string    `json:"user_username,omitempty"`
	UserDisplayName string    `json:"user_display_name,omitempty"`
	UserAvatarURL   string    `json:"user_avatar_url,omitempty"`
	Permissions     []string  `json:"permissions"`
	IsOwner         bool      `json:"is_owner,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// CreateUserGrantRequest is the input for adding a user access grant.
type CreateUserGrantRequest struct {
	SubjectType string   `json:"subject_type"`
	UserID      string   `json:"user_id,omitempty"`
	Permissions []string `json:"permissions"`
}

// UpdateUserGrantRequest is the input for updating a grant's permissions.
type UpdateUserGrantRequest struct {
	Permissions []string `json:"permissions"`
}

// allPermissions returns the full permission set (owner/admin level).
func allPermissions() []string {
	return []string{
		PermissionChat,
		PermissionWorkspaceRead,
		PermissionWorkspaceWrite,
		PermissionWorkspaceExec,
		PermissionManage,
	}
}

// HasPermission reports whether the granted set satisfies the required scope.
func HasPermission(granted []string, required string) bool {
	return hasPermission(granted, required)
}

func hasPermission(granted []string, required string) bool {
	required = strings.ToLower(strings.TrimSpace(required))
	if required == "" {
		required = PermissionManage
	}
	perms := expandPermissions(granted)
	for _, p := range perms {
		if p == required {
			return true
		}
	}
	return false
}

// normalizePermissions validates and de-duplicates a permission list, preserving
// a stable canonical order.
func normalizePermissions(raw []string) ([]string, error) {
	seen := map[string]bool{}
	for _, p := range raw {
		key := strings.ToLower(strings.TrimSpace(p))
		switch key {
		case PermissionChat, PermissionWorkspaceRead, PermissionWorkspaceWrite, PermissionWorkspaceExec, PermissionManage:
			seen[key] = true
		case "":
			continue
		default:
			return nil, ErrInvalidPermission
		}
	}
	out := expandPermissionSet(seen)
	if len(out) == 0 {
		return nil, ErrInvalidPermission
	}
	return out, nil
}

func isKnownPermission(permission string) bool {
	switch permission {
	case PermissionChat, PermissionWorkspaceRead, PermissionWorkspaceWrite, PermissionWorkspaceExec, PermissionManage:
		return true
	default:
		return false
	}
}

func expandPermissions(perms []string) []string {
	seen := make(map[string]bool, len(perms))
	for _, p := range perms {
		key := strings.ToLower(strings.TrimSpace(p))
		if isKnownPermission(key) {
			seen[key] = true
		}
	}
	return expandPermissionSet(seen)
}

func expandPermissionSet(seen map[string]bool) []string {
	if seen[PermissionManage] {
		for _, p := range allPermissions() {
			seen[p] = true
		}
	}
	if seen[PermissionWorkspaceWrite] {
		seen[PermissionWorkspaceRead] = true
	}

	out := make([]string, 0, len(seen))
	for _, p := range allPermissions() {
		if seen[p] {
			out = append(out, p)
		}
	}
	return out
}

func decodePermissions(payload []byte) []string {
	if len(payload) == 0 {
		return nil
	}
	var perms []string
	if err := json.Unmarshal(payload, &perms); err != nil {
		return nil
	}
	out := make([]string, 0, len(perms))
	for _, p := range perms {
		key := strings.ToLower(strings.TrimSpace(p))
		if isKnownPermission(key) {
			out = append(out, key)
		}
	}
	return expandPermissions(out)
}

func encodePermissions(perms []string) ([]byte, error) {
	if perms == nil {
		perms = []string{}
	}
	return json.Marshal(perms)
}

// ResolveUserPermissions returns the effective permissions for userID on botID.
// Owners and admins always receive the full permission set; other users receive
// the union of their direct grant and any everyone grant.
func (s *Service) ResolveUserPermissions(ctx context.Context, botID, userID string, isAdmin bool) ([]string, error) {
	if s.queries == nil {
		return nil, errors.New("bot queries not configured")
	}
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	row, err := s.queries.GetBotByID(ctx, botUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrBotNotFound
		}
		return nil, err
	}
	bot, err := toBot(asSQLCBot(row))
	if err != nil {
		return nil, err
	}
	return s.ResolveUserPermissionsForBot(ctx, bot, userID, isAdmin)
}

// ResolveUserPermissionsForBot resolves permissions using an already-loaded bot
// row. Hot read paths use this to avoid loading the same bot twice.
func (s *Service) ResolveUserPermissionsForBot(ctx context.Context, bot Bot, userID string, isAdmin bool) ([]string, error) {
	if s.queries == nil {
		return nil, errors.New("bot queries not configured")
	}
	uid := strings.TrimSpace(userID)
	if isAdmin || bot.OwnerUserID == uid {
		return allPermissions(), nil
	}
	botUUID, err := db.ParseUUID(bot.ID)
	if err != nil {
		return nil, err
	}
	var userUUID pgtype.UUID
	if uid != "" {
		if parsed, parseErr := db.ParseUUID(uid); parseErr == nil {
			userUUID = parsed
		}
	}
	grants, err := s.queries.ListBotUserGrantsForUser(ctx, sqlc.ListBotUserGrantsForUserParams{
		BotID:  botUUID,
		UserID: userUUID,
	})
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, g := range grants {
		for _, p := range decodePermissions(g.Permissions) {
			seen[p] = true
		}
	}
	return expandPermissionSet(seen), nil
}

// AuthorizeAccessWithPermission checks whether userID may access the bot with the
// required permission scope (owner, admin, or a matching grant).
func (s *Service) AuthorizeAccessWithPermission(ctx context.Context, userID, botID string, isAdmin bool, required string) (Bot, error) {
	if s.queries == nil {
		return Bot{}, errors.New("bot queries not configured")
	}
	bot, err := s.Get(ctx, botID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Bot{}, ErrBotNotFound
		}
		return Bot{}, err
	}
	uid := strings.TrimSpace(userID)
	if isAdmin || bot.OwnerUserID == uid {
		return bot, nil
	}
	if required == "" {
		required = PermissionManage
	}
	// Use the resolved bot UUID: botID may be a name slug from the URL, but grant
	// resolution requires the canonical UUID.
	perms, err := s.ResolveUserPermissions(ctx, bot.ID, userID, isAdmin)
	if err != nil {
		return Bot{}, err
	}
	if hasPermission(perms, required) {
		return bot, nil
	}
	return Bot{}, ErrBotAccessDenied
}

// ListUserGrants returns all workspace user access grants for a bot, with the
// owner prepended as an implicit full-access entry.
func (s *Service) ListUserGrants(ctx context.Context, botID string) ([]UserGrant, error) {
	if s.queries == nil {
		return nil, errors.New("bot queries not configured")
	}
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	botRow, err := s.queries.GetBotByID(ctx, botUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrBotNotFound
		}
		return nil, err
	}
	rows, err := s.queries.ListBotUserGrants(ctx, botUUID)
	if err != nil {
		return nil, err
	}
	items := make([]UserGrant, 0, len(rows)+1)
	owner := UserGrant{
		BotID:       botID,
		SubjectType: GrantSubjectUser,
		UserID:      botRow.OwnerUserID.String(),
		Permissions: allPermissions(),
		IsOwner:     true,
	}
	if ownerAccount, err := s.queries.GetUserByID(ctx, botRow.OwnerUserID); err == nil {
		owner.UserUsername = ownerAccount.Username.String
		owner.UserDisplayName = ownerAccount.DisplayName.String
		owner.UserAvatarURL = ownerAccount.AvatarUrl.String
	}
	items = append(items, owner)
	for _, row := range rows {
		items = append(items, UserGrant{
			ID:              row.ID.String(),
			BotID:           row.BotID.String(),
			SubjectType:     row.SubjectType,
			UserID:          optionalUUIDString(row.UserID),
			UserUsername:    row.UserUsername.String,
			UserDisplayName: row.UserDisplayName.String,
			UserAvatarURL:   row.UserAvatarUrl.String,
			Permissions:     decodePermissions(row.Permissions),
			CreatedAt:       timeFromPG(row.CreatedAt),
			UpdatedAt:       timeFromPG(row.UpdatedAt),
		})
	}
	return items, nil
}

// CreateUserGrant adds a new workspace user (or everyone) access grant.
func (s *Service) CreateUserGrant(ctx context.Context, botID, createdByUserID string, req CreateUserGrantRequest) (UserGrant, error) {
	if s.queries == nil {
		return UserGrant{}, errors.New("bot queries not configured")
	}
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return UserGrant{}, err
	}
	botRow, err := s.queries.GetBotByID(ctx, botUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return UserGrant{}, ErrBotNotFound
		}
		return UserGrant{}, err
	}
	subjectType := strings.ToLower(strings.TrimSpace(req.SubjectType))
	perms, err := normalizePermissions(req.Permissions)
	if err != nil {
		return UserGrant{}, err
	}
	payload, err := encodePermissions(perms)
	if err != nil {
		return UserGrant{}, err
	}

	params := sqlc.CreateBotUserGrantParams{
		BotID:       botUUID,
		SubjectType: subjectType,
		Permissions: payload,
	}
	if createdBy := strings.TrimSpace(createdByUserID); createdBy != "" {
		if parsed, parseErr := db.ParseUUID(createdBy); parseErr == nil {
			params.CreatedByUserID = parsed
		}
	}

	switch subjectType {
	case GrantSubjectEveryone:
		// user_id stays NULL
	case GrantSubjectUser:
		userID := strings.TrimSpace(req.UserID)
		if userID == "" {
			return UserGrant{}, ErrGrantUserRequired
		}
		userUUID, parseErr := db.ParseUUID(userID)
		if parseErr != nil {
			return UserGrant{}, parseErr
		}
		if botRow.OwnerUserID.String() == userID {
			return UserGrant{}, ErrGrantOwnerConflict
		}
		if err := s.ensureUserExists(ctx, userUUID); err != nil {
			return UserGrant{}, err
		}
		params.UserID = userUUID
	default:
		return UserGrant{}, ErrInvalidGrantSubject
	}

	row, err := s.queries.CreateBotUserGrant(ctx, params)
	if err != nil {
		if db.IsUniqueViolation(err) {
			return UserGrant{}, ErrGrantExists
		}
		return UserGrant{}, err
	}
	return s.grantFromModel(ctx, row), nil
}

// UpdateUserGrant updates the permission set of an existing grant.
func (s *Service) UpdateUserGrant(ctx context.Context, botID, grantID string, req UpdateUserGrantRequest) (UserGrant, error) {
	if s.queries == nil {
		return UserGrant{}, errors.New("bot queries not configured")
	}
	grantUUID, err := db.ParseUUID(grantID)
	if err != nil {
		return UserGrant{}, err
	}
	existing, err := s.queries.GetBotUserGrantByID(ctx, grantUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return UserGrant{}, ErrGrantNotFound
		}
		return UserGrant{}, err
	}
	if existing.BotID.String() != strings.TrimSpace(botID) {
		return UserGrant{}, ErrGrantNotFound
	}
	perms, err := normalizePermissions(req.Permissions)
	if err != nil {
		return UserGrant{}, err
	}
	payload, err := encodePermissions(perms)
	if err != nil {
		return UserGrant{}, err
	}
	row, err := s.queries.UpdateBotUserGrantPermissions(ctx, sqlc.UpdateBotUserGrantPermissionsParams{
		ID:          grantUUID,
		Permissions: payload,
	})
	if err != nil {
		return UserGrant{}, err
	}
	return s.grantFromModel(ctx, row), nil
}

// DeleteUserGrant removes a grant from a bot.
func (s *Service) DeleteUserGrant(ctx context.Context, botID, grantID string) error {
	if s.queries == nil {
		return errors.New("bot queries not configured")
	}
	grantUUID, err := db.ParseUUID(grantID)
	if err != nil {
		return err
	}
	existing, err := s.queries.GetBotUserGrantByID(ctx, grantUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrGrantNotFound
		}
		return err
	}
	if existing.BotID.String() != strings.TrimSpace(botID) {
		return ErrGrantNotFound
	}
	return s.queries.DeleteBotUserGrantByID(ctx, grantUUID)
}

func (s *Service) grantFromModel(ctx context.Context, row sqlc.BotUserGrant) UserGrant {
	grant := UserGrant{
		ID:          row.ID.String(),
		BotID:       row.BotID.String(),
		SubjectType: row.SubjectType,
		UserID:      optionalUUIDString(row.UserID),
		Permissions: decodePermissions(row.Permissions),
		CreatedAt:   timeFromPG(row.CreatedAt),
		UpdatedAt:   timeFromPG(row.UpdatedAt),
	}
	if row.UserID.Valid {
		if account, err := s.queries.GetUserByID(ctx, row.UserID); err == nil {
			grant.UserUsername = account.Username.String
			grant.UserDisplayName = account.DisplayName.String
			grant.UserAvatarURL = account.AvatarUrl.String
		}
	}
	return grant
}

func optionalUUIDString(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	return id.String()
}

func timeFromPG(ts pgtype.Timestamptz) time.Time {
	if !ts.Valid {
		return time.Time{}
	}
	return ts.Time
}
