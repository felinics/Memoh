package bot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	botpersistence "github.com/memohai/memoh/domains/api/bot/persistence"
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
	ErrGrantNotFound = botpersistence.ErrGrantNotFound
	// ErrInvalidPermission indicates an unknown or empty permission set.
	ErrInvalidPermission = errors.New("invalid permission")
	// ErrInvalidGrantSubject indicates an unknown subject type.
	ErrInvalidGrantSubject = errors.New("invalid grant subject")
	// ErrGrantUserRequired indicates a user grant is missing its user id.
	ErrGrantUserRequired = errors.New("user id is required for a user grant")
	// ErrGrantOwnerConflict indicates an attempt to grant access to the bot owner.
	ErrGrantOwnerConflict = errors.New("the bot owner already has full access")
	// ErrGrantExists indicates a grant for the subject already exists.
	ErrGrantExists = botpersistence.ErrGrantExists
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
	if s.bots == nil {
		return nil, errors.New("bot queries not configured")
	}
	botID, err := parseUUID(botID)
	if err != nil {
		return nil, err
	}
	row, err := s.bots.GetBotByID(ctx, botID)
	if err != nil {
		if errors.Is(err, ErrBotNotFound) {
			return nil, ErrBotNotFound
		}
		return nil, err
	}
	bot, err := toBot(row)
	if err != nil {
		return nil, err
	}
	return s.ResolveUserPermissionsForBot(ctx, bot, userID, isAdmin)
}

// ResolveUserPermissionsForBot resolves permissions using an already-loaded bot
// row. Hot read paths use this to avoid loading the same bot twice.
func (s *Service) ResolveUserPermissionsForBot(ctx context.Context, bot Bot, userID string, isAdmin bool) ([]string, error) {
	if s.grants == nil {
		return nil, errors.New("bot queries not configured")
	}
	uid := strings.TrimSpace(userID)
	if isAdmin || bot.OwnerUserID == uid {
		return allPermissions(), nil
	}
	botID, err := parseUUID(bot.ID)
	if err != nil {
		return nil, err
	}
	resolvedUserID := ""
	if uid != "" {
		if parsed, parseErr := parseUUID(uid); parseErr == nil {
			resolvedUserID = parsed
		}
	}
	grants, err := s.grants.ListGrantsForUser(ctx, botID, resolvedUserID)
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
	if s.bots == nil {
		return Bot{}, errors.New("bot queries not configured")
	}
	bot, err := s.Get(ctx, botID)
	if err != nil {
		if errors.Is(err, ErrBotNotFound) {
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
	if s.bots == nil || s.grants == nil {
		return nil, errors.New("bot queries not configured")
	}
	botID, err := parseUUID(botID)
	if err != nil {
		return nil, err
	}
	botRow, err := s.bots.GetBotByID(ctx, botID)
	if err != nil {
		if errors.Is(err, ErrBotNotFound) {
			return nil, ErrBotNotFound
		}
		return nil, err
	}
	rows, err := s.grants.ListGrants(ctx, botID)
	if err != nil {
		return nil, err
	}
	items := make([]UserGrant, 0, len(rows)+1)
	owner := UserGrant{
		BotID:       botID,
		SubjectType: GrantSubjectUser,
		UserID:      botRow.OwnerUserID,
		Permissions: allPermissions(),
		IsOwner:     true,
	}
	if s.users != nil {
		if ownerAccount, err := s.users.GetUser(ctx, botRow.OwnerUserID); err == nil {
			owner.UserUsername = ownerAccount.Username
			owner.UserDisplayName = ownerAccount.DisplayName
			owner.UserAvatarURL = ownerAccount.AvatarURL
		}
	}
	items = append(items, owner)
	for _, row := range rows {
		items = append(items, s.grantFromRecord(ctx, row))
	}
	return items, nil
}

// CreateUserGrant adds a new workspace user (or everyone) access grant.
func (s *Service) CreateUserGrant(ctx context.Context, botID, createdByUserID string, req CreateUserGrantRequest) (UserGrant, error) {
	if s.bots == nil || s.grants == nil {
		return UserGrant{}, errors.New("bot queries not configured")
	}
	botID, err := parseUUID(botID)
	if err != nil {
		return UserGrant{}, err
	}
	botRow, err := s.bots.GetBotByID(ctx, botID)
	if err != nil {
		if errors.Is(err, ErrBotNotFound) {
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

	input := botpersistence.CreateGrantInput{
		BotID:       botID,
		SubjectType: subjectType,
		Permissions: payload,
	}
	if createdBy := strings.TrimSpace(createdByUserID); createdBy != "" {
		if parsed, parseErr := parseUUID(createdBy); parseErr == nil {
			input.CreatedByUserID = parsed
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
		userID, parseErr := parseUUID(userID)
		if parseErr != nil {
			return UserGrant{}, parseErr
		}
		if botRow.OwnerUserID == userID {
			return UserGrant{}, ErrGrantOwnerConflict
		}
		if err := s.ensureUserExists(ctx, userID); err != nil {
			return UserGrant{}, err
		}
		input.UserID = userID
	default:
		return UserGrant{}, ErrInvalidGrantSubject
	}

	row, err := s.grants.CreateGrant(ctx, input)
	if err != nil {
		return UserGrant{}, err
	}
	return s.grantFromRecord(ctx, row), nil
}

// UpdateUserGrant updates the permission set of an existing grant.
func (s *Service) UpdateUserGrant(ctx context.Context, botID, grantID string, req UpdateUserGrantRequest) (UserGrant, error) {
	if s.grants == nil {
		return UserGrant{}, errors.New("bot queries not configured")
	}
	grantID, err := parseUUID(grantID)
	if err != nil {
		return UserGrant{}, err
	}
	existing, err := s.grants.GetGrant(ctx, grantID)
	if err != nil {
		if errors.Is(err, ErrGrantNotFound) {
			return UserGrant{}, ErrGrantNotFound
		}
		return UserGrant{}, err
	}
	if existing.BotID != strings.TrimSpace(botID) {
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
	row, err := s.grants.UpdateGrantPermissions(ctx, grantID, payload)
	if err != nil {
		return UserGrant{}, err
	}
	return s.grantFromRecord(ctx, row), nil
}

// DeleteUserGrant removes a grant from a bot.
func (s *Service) DeleteUserGrant(ctx context.Context, botID, grantID string) error {
	if s.grants == nil {
		return errors.New("bot queries not configured")
	}
	grantID, err := parseUUID(grantID)
	if err != nil {
		return err
	}
	existing, err := s.grants.GetGrant(ctx, grantID)
	if err != nil {
		if errors.Is(err, ErrGrantNotFound) {
			return ErrGrantNotFound
		}
		return err
	}
	if existing.BotID != strings.TrimSpace(botID) {
		return ErrGrantNotFound
	}
	return s.grants.DeleteGrant(ctx, grantID)
}

func (s *Service) grantFromRecord(ctx context.Context, row botpersistence.GrantRecord) UserGrant {
	grant := UserGrant{
		ID:          row.ID,
		BotID:       row.BotID,
		SubjectType: row.SubjectType,
		UserID:      row.UserID,
		Permissions: decodePermissions(row.Permissions),
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
	if row.UserID != "" && s.users != nil {
		if account, err := s.users.GetUser(ctx, row.UserID); err == nil {
			grant.UserUsername = account.Username
			grant.UserDisplayName = account.DisplayName
			grant.UserAvatarURL = account.AvatarURL
		}
	}
	return grant
}
