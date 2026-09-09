// Package channelaccess wires together the global account binding (channel
// identity <-> web user) and the per-bot Manage capability, resolving the
// effective Manage as "local override ?? inherited from bound web member".
package channelaccess

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/acl"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

const (
	defaultCodeTTL = 10 * time.Minute
	tokenAlphabet  = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"
	tokenLength    = 8
)

var (
	ErrCodeNotFound = errors.New("link code not found")
	ErrCodeExpired  = errors.New("link code expired")
	ErrCodeConsumed = errors.New("link code already used")
	ErrInvalidInput = errors.New("invalid input")
)

type Service struct {
	queries dbstore.Queries
	acl     manageOverrideStore
	bots    botPermissionResolver
	logger  *slog.Logger
	now     func() time.Time
	token   func() (string, error)
	codeTTL time.Duration
}

type manageOverrideStore interface {
	GetManageOverride(ctx context.Context, botID, channelIdentityID string) (granted bool, exists bool, err error)
	ListManageOverrides(ctx context.Context, botID string) ([]acl.ManageOverride, error)
	SetManageOverride(ctx context.Context, botID, channelIdentityID string, granted bool, createdByUserID string) (acl.ManageOverride, error)
	DeleteManageOverride(ctx context.Context, botID, channelIdentityID string) error
}

type botPermissionResolver interface {
	ResolveUserPermissions(ctx context.Context, botID, userID string, isAdmin bool) ([]string, error)
	ListUserGrants(ctx context.Context, botID string) ([]bots.UserGrant, error)
}

func NewService(log *slog.Logger, queries dbstore.Queries, aclService *acl.Service, botService *bots.Service) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		queries: queries,
		acl:     aclService,
		bots:    botService,
		logger:  log.With(slog.String("service", "channelaccess")),
		now:     func() time.Time { return time.Now().UTC() },
		token:   generateToken,
		codeTTL: defaultCodeTTL,
	}
}

// HasManageGrant reports the effective Manage capability for a channel identity on
// a bot: a local Channel Access override wins; otherwise it is inherited when the
// identity is bound to a web member that carries Manage (owner or manage grant).
// It satisfies the command package's ChannelManageResolver.
func (s *Service) HasManageGrant(ctx context.Context, botID, channelIdentityID string) (bool, error) {
	if s == nil {
		return false, errors.New("channelaccess service not configured")
	}
	channelIdentityID = strings.TrimSpace(channelIdentityID)
	if channelIdentityID == "" {
		return false, nil
	}
	if s.acl != nil {
		granted, exists, err := s.acl.GetManageOverride(ctx, botID, channelIdentityID)
		if err != nil {
			return false, err
		}
		if exists {
			return granted, nil
		}
	}
	return s.inheritedManage(ctx, botID, channelIdentityID)
}

// inheritedManage reports whether any web user bound to the channel identity carries
// the Manage capability on the bot.
func (s *Service) inheritedManage(ctx context.Context, botID, channelIdentityID string) (bool, error) {
	if s.queries == nil || s.bots == nil {
		return false, nil
	}
	pgID, err := db.ParseUUID(channelIdentityID)
	if err != nil {
		return false, err
	}
	userIDs, err := s.queries.ListUserIDsByChannelIdentity(ctx, pgID)
	if err != nil {
		return false, err
	}
	for _, uid := range userIDs {
		userID := uuidString(uid)
		if strings.TrimSpace(userID) == "" {
			continue
		}
		ok, err := s.userHasManage(ctx, botID, userID)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) userHasManage(ctx context.Context, botID, userID string) (bool, error) {
	perms, err := s.bots.ResolveUserPermissions(ctx, botID, userID, false)
	if err != nil {
		// A bound user who lost access (e.g. bot deleted) should not block resolution.
		if errors.Is(err, bots.ErrBotNotFound) {
			return false, nil
		}
		return false, err
	}
	return bots.HasPermission(perms, bots.PermissionManage), nil
}

// IssueLinkCode generates a one-time code the user sends as /link <code> in IM.
func (s *Service) IssueLinkCode(ctx context.Context, userID, channelType string) (LinkCode, error) {
	if s == nil || s.queries == nil {
		return LinkCode{}, errors.New("channelaccess service not configured")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return LinkCode{}, ErrInvalidInput
	}
	pgUserID, err := db.ParseUUID(userID)
	if err != nil {
		return LinkCode{}, err
	}
	token, err := s.token()
	if err != nil {
		return LinkCode{}, err
	}
	expiresAt := s.now().Add(s.codeTTL)
	row, err := s.queries.CreateChannelLinkCode(ctx, sqlc.CreateChannelLinkCodeParams{
		Token:  token,
		UserID: pgUserID,
		// channel_type is NOT NULL; "" is the "no specific platform" sentinel, so
		// always send a valid (possibly empty) string rather than NULL.
		ChannelType: pgtype.Text{String: strings.TrimSpace(channelType), Valid: true},
		ExpiresAt:   pgtype.Timestamptz{Time: expiresAt, Valid: true},
	})
	if err != nil {
		return LinkCode{}, err
	}
	return LinkCode{
		Token:       row.Token,
		UserID:      uuidString(row.UserID),
		ChannelType: row.ChannelType,
		ExpiresAt:   db.TimeFromPg(row.ExpiresAt),
		CreatedAt:   db.TimeFromPg(row.CreatedAt),
	}, nil
}

// ConsumeLinkCode binds the given channel identity to the user that owns the code.
// It is invoked from the IM /link command with the sender's channel identity.
func (s *Service) ConsumeLinkCode(ctx context.Context, token, channelIdentityID string) (Binding, error) {
	if s == nil || s.queries == nil {
		return Binding{}, errors.New("channelaccess service not configured")
	}
	token = normalizeToken(token)
	channelIdentityID = strings.TrimSpace(channelIdentityID)
	if token == "" || channelIdentityID == "" {
		return Binding{}, ErrInvalidInput
	}
	pgIdentityID, err := db.ParseUUID(channelIdentityID)
	if err != nil {
		return Binding{}, err
	}
	bindingRow, err := s.queries.RedeemChannelLinkCode(ctx, sqlc.RedeemChannelLinkCodeParams{
		Token:             token,
		ChannelIdentityID: pgIdentityID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Binding{}, s.classifyRedeemNoRow(ctx, token)
		}
		return Binding{}, err
	}
	return Binding{
		ID:                uuidString(bindingRow.ID),
		UserID:            uuidString(bindingRow.UserID),
		ChannelIdentityID: uuidString(bindingRow.ChannelIdentityID),
		CreatedAt:         db.TimeFromPg(bindingRow.CreatedAt),
	}, nil
}

func (s *Service) classifyRedeemNoRow(ctx context.Context, token string) error {
	code, err := s.queries.GetChannelLinkCodeByToken(ctx, token)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCodeNotFound
		}
		return err
	}
	if code.ConsumedAt.Valid {
		return ErrCodeConsumed
	}
	if !code.ExpiresAt.Valid || !code.ExpiresAt.Time.After(s.now()) {
		return ErrCodeExpired
	}
	// The code exists, is unconsumed, and is not expired, yet the redeem CTE
	// returned no rows. This is a narrow race: between the CTE's UPDATE check
	// and this diagnostic SELECT, another concurrent request consumed the code,
	// or a clock-skew edge made the CTE's now() disagree with ours. Returning
	// ErrCodeConsumed is approximately correct for the user ("code already used")
	// and safe — the binding was not created.
	return ErrCodeConsumed
}

// ListUserBindings returns the channel identities bound to a user's account.
func (s *Service) ListUserBindings(ctx context.Context, userID string) ([]Binding, error) {
	if s == nil || s.queries == nil {
		return nil, errors.New("channelaccess service not configured")
	}
	pgUserID, err := db.ParseUUID(strings.TrimSpace(userID))
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListChannelIdentityBindingsForUser(ctx, pgUserID)
	if err != nil {
		return nil, err
	}
	items := make([]Binding, 0, len(rows))
	for _, row := range rows {
		items = append(items, Binding{
			ID:                         uuidString(row.ID),
			UserID:                     uuidString(row.UserID),
			ChannelIdentityID:          uuidString(row.ChannelIdentityID),
			ChannelType:                db.TextToString(row.ChannelType),
			ChannelSubjectID:           db.TextToString(row.ChannelSubjectID),
			ChannelIdentityDisplayName: db.TextToString(row.ChannelIdentityDisplayName),
			ChannelIdentityAvatarURL:   db.TextToString(row.ChannelIdentityAvatarUrl),
			CreatedAt:                  db.TimeFromPg(row.CreatedAt),
		})
	}
	return items, nil
}

// Unbind removes a channel identity binding from a user's account.
func (s *Service) Unbind(ctx context.Context, userID, channelIdentityID string) error {
	if s == nil || s.queries == nil {
		return errors.New("channelaccess service not configured")
	}
	pgUserID, err := db.ParseUUID(strings.TrimSpace(userID))
	if err != nil {
		return err
	}
	pgIdentityID, err := db.ParseUUID(strings.TrimSpace(channelIdentityID))
	if err != nil {
		return err
	}
	return s.queries.DeleteUserChannelIdentityBinding(ctx, sqlc.DeleteUserChannelIdentityBindingParams{
		UserID:            pgUserID,
		ChannelIdentityID: pgIdentityID,
	})
}

// SetManager sets a local Manage override (ON/OFF) for a channel identity on a bot.
func (s *Service) SetManager(ctx context.Context, botID, channelIdentityID string, granted bool, actorUserID string) error {
	if s == nil || s.acl == nil {
		return errors.New("channelaccess service not configured")
	}
	_, err := s.acl.SetManageOverride(ctx, botID, channelIdentityID, granted, actorUserID)
	return err
}

// ClearManagerOverride removes the local Manage override so the identity falls back
// to inheritance.
func (s *Service) ClearManagerOverride(ctx context.Context, botID, channelIdentityID string) error {
	if s == nil || s.acl == nil {
		return errors.New("channelaccess service not configured")
	}
	return s.acl.DeleteManageOverride(ctx, botID, channelIdentityID)
}

// ListManagers returns the effective Manage state per channel identity on a bot,
// merging inherited members (bound web members carrying Manage) with local overrides.
func (s *Service) ListManagers(ctx context.Context, botID string) ([]Manager, error) {
	if s == nil || s.acl == nil {
		return nil, errors.New("channelaccess service not configured")
	}
	byIdentity := map[string]*Manager{}

	// Platform members: every workspace member of this bot (any permission), via
	// their bound channel identities. Bound marks them as platform members in the
	// UI even without Manage; members whose grant carries Manage (owner or manage
	// grant) additionally contribute inherited Manage.
	if s.bots != nil && s.queries != nil {
		grants, err := s.bots.ListUserGrants(ctx, botID)
		if err != nil {
			return nil, err
		}
		everyoneCarriesManage := false
		for _, g := range grants {
			userID := strings.TrimSpace(g.UserID)
			carriesManage := g.IsOwner || bots.HasPermission(g.Permissions, bots.PermissionManage)
			if g.SubjectType == bots.GrantSubjectEveryone {
				everyoneCarriesManage = everyoneCarriesManage || carriesManage
				continue
			}
			if userID == "" || g.SubjectType != bots.GrantSubjectUser {
				continue
			}
			pgUserID, err := db.ParseUUID(userID)
			if err != nil {
				continue
			}
			bindings, err := s.queries.ListChannelIdentityBindingsForUser(ctx, pgUserID)
			if err != nil {
				return nil, err
			}
			for _, b := range bindings {
				mergeManagerBinding(byIdentity, b.ChannelIdentityID, carriesManage, b.ChannelType, b.ChannelSubjectID, b.ChannelIdentityDisplayName, b.ChannelIdentityAvatarUrl)
			}
		}
		if everyoneCarriesManage {
			// Scoped query: only return bindings for users who have a grant on
			// this specific bot (via bot_user_grants JOIN). This prevents leaking
			// channel identities from unrelated bots while still showing all
			// workspace members' bound identities as inherited-manage.
			bindings, err := s.queries.ListChannelIdentityBindingsForBot(ctx, db.ParseUUIDOrEmpty(botID))
			if err != nil {
				return nil, err
			}
			for _, b := range bindings {
				mergeManagerBinding(byIdentity, b.ChannelIdentityID, true, b.ChannelType, b.ChannelSubjectID, b.ChannelIdentityDisplayName, b.ChannelIdentityAvatarUrl)
			}
		}
	}

	// Local overrides win over inheritance.
	overrides, err := s.acl.ListManageOverrides(ctx, botID)
	if err != nil {
		return nil, err
	}
	for _, o := range overrides {
		ciID := strings.TrimSpace(o.ChannelIdentityID)
		if ciID == "" {
			continue
		}
		m := byIdentity[ciID]
		if m == nil {
			m = &Manager{ChannelIdentityID: ciID}
			byIdentity[ciID] = m
		}
		m.HasOverride = true
		m.Manage = o.Granted
		if o.ChannelType != "" {
			m.ChannelType = o.ChannelType
		}
		if o.ChannelSubjectID != "" {
			m.ChannelSubjectID = o.ChannelSubjectID
		}
		if o.ChannelIdentityDisplayName != "" {
			m.ChannelIdentityDisplayName = o.ChannelIdentityDisplayName
		}
		if o.ChannelIdentityAvatarURL != "" {
			m.ChannelIdentityAvatarURL = o.ChannelIdentityAvatarURL
		}
	}

	items := make([]Manager, 0, len(byIdentity))
	for _, m := range byIdentity {
		items = append(items, *m)
	}
	sort.Slice(items, func(i, j int) bool {
		ni := items[i].ChannelIdentityDisplayName
		nj := items[j].ChannelIdentityDisplayName
		if ni != nj {
			return ni < nj
		}
		return items[i].ChannelIdentityID < items[j].ChannelIdentityID
	})
	return items, nil
}

func mergeManagerBinding(byIdentity map[string]*Manager, channelIdentityID pgtype.UUID, carriesManage bool, channelType, channelSubjectID, displayName, avatarURL pgtype.Text) {
	ciID := uuidString(channelIdentityID)
	if ciID == "" {
		return
	}
	m := byIdentity[ciID]
	if m == nil {
		m = &Manager{ChannelIdentityID: ciID}
		byIdentity[ciID] = m
	}
	m.Bound = true
	// An identity bound to several members is inherited-manage if ANY of those
	// members, including an Everyone grant, carries Manage.
	if carriesManage {
		m.Inherited = true
		m.Manage = true
	}
	if m.ChannelType == "" {
		m.ChannelType = db.TextToString(channelType)
	}
	if m.ChannelSubjectID == "" {
		m.ChannelSubjectID = db.TextToString(channelSubjectID)
	}
	if m.ChannelIdentityDisplayName == "" {
		m.ChannelIdentityDisplayName = db.TextToString(displayName)
	}
	if m.ChannelIdentityAvatarURL == "" {
		m.ChannelIdentityAvatarURL = db.TextToString(avatarURL)
	}
}

func uuidString(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	return uuid.UUID(id.Bytes).String()
}

func normalizeToken(token string) string {
	return strings.ToUpper(strings.TrimSpace(token))
}

func generateToken() (string, error) {
	buf := make([]byte, tokenLength)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, tokenLength)
	for i, b := range buf {
		out[i] = tokenAlphabet[int(b)%len(tokenAlphabet)]
	}
	return string(out), nil
}
