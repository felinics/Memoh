package bots

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/internal/db"
	"github.com/memohai/memoh/internal/db/postgres/sqlc"
)

// Peer grant permission scopes. A peer grant answers "may this bot reach this
// bot", which is a different question from "may this person use this bot", so
// it has its own vocabulary rather than sharing the user scopes.
//
// The set is deliberately small and strictly weaker than the user vocabulary.
// It carries no workspace scope and no manage: letting one bot execute inside
// another bot's workspace is lateral movement with no human in the loop, and
// letting one bot reconfigure another is an escalation path with no owner
// approval. Neither has a use case that a human-held grant does not cover.
const (
	// PeerPermissionDiscover lets the caller see this bot as a reachable peer.
	PeerPermissionDiscover = "discover"
	// PeerPermissionContact lets the caller deliver a message to this bot.
	// Whether that message starts a run is the callee's own policy, not a right
	// the caller holds.
	PeerPermissionContact = "contact"
	// PeerPermissionDelegate lets the caller hand this bot work and wait for a
	// result, which spends the callee's budget on the caller's schedule. It is
	// the strongest peer scope and should stay explicitly granted per edge.
	PeerPermissionDelegate = "delegate"
)

// Peer grant subject types. 'any_bot' is the peer analogue of the user
// vocabulary's 'everyone': every other bot in the team, never the callee itself.
const (
	PeerGrantSubjectBot    = "bot"
	PeerGrantSubjectAnyBot = "any_bot"
)

// peerVocabulary is the permission vocabulary for bot subjects. Its scopes are
// disjoint from userVocabulary's, and peer_grants_permissions_check in the
// database rejects anything outside it, so a user scope cannot reach this table
// even if a future call site picks the wrong encoder.
var peerVocabulary = newVocabulary(
	[]string{
		PeerPermissionDiscover,
		PeerPermissionContact,
		PeerPermissionDelegate,
	},
	map[string][]string{
		PeerPermissionDelegate: {PeerPermissionContact},
		PeerPermissionContact:  {PeerPermissionDiscover},
	},
)

var (
	// ErrPeerGrantNotFound indicates the peer grant does not exist for the bot.
	ErrPeerGrantNotFound = errors.New("bot peer grant not found")
	// ErrPeerGrantBotRequired indicates a bot-subject grant is missing its bot id.
	ErrPeerGrantBotRequired = errors.New("subject bot id is required for a bot grant")
	// ErrPeerGrantSelfConflict indicates an attempt to grant a bot access to itself.
	ErrPeerGrantSelfConflict = errors.New("a bot cannot be granted access to itself")
	// ErrPeerGrantSubjectNotFound indicates the subject bot does not exist.
	ErrPeerGrantSubjectNotFound = errors.New("subject bot not found")
)

// PeerGrant is a directed access edge from a caller bot to the bot that owns
// the grant. It is stored on and administered by the callee, because a grant is
// the callee consenting to spend its own attention.
type PeerGrant struct {
	ID                    string    `json:"id"`
	BotID                 string    `json:"bot_id"`
	SubjectType           string    `json:"subject_type"`
	SubjectBotID          string    `json:"subject_bot_id,omitempty"`
	SubjectBotName        string    `json:"subject_bot_name,omitempty"`
	SubjectBotDisplayName string    `json:"subject_bot_display_name,omitempty"`
	SubjectBotAvatarURL   string    `json:"subject_bot_avatar_url,omitempty"`
	Permissions           []string  `json:"permissions"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

// CreatePeerGrantRequest is the input for adding a bot access grant.
type CreatePeerGrantRequest struct {
	SubjectType  string   `json:"subject_type"`
	SubjectBotID string   `json:"subject_bot_id,omitempty"`
	Permissions  []string `json:"permissions"`
}

// UpdatePeerGrantRequest is the input for updating a peer grant's permissions.
type UpdatePeerGrantRequest struct {
	Permissions []string `json:"permissions"`
}

// ReachablePeer is one entry of the caller-side reverse lookup: a bot the
// caller may reach, and with which scopes.
type ReachablePeer struct {
	BotID          string   `json:"bot_id"`
	BotName        string   `json:"bot_name"`
	BotDisplayName string   `json:"bot_display_name,omitempty"`
	BotAvatarURL   string   `json:"bot_avatar_url,omitempty"`
	Permissions    []string `json:"permissions"`
	// ViaAnyBot marks a blanket grant rather than an edge naming this caller.
	ViaAnyBot bool `json:"via_any_bot,omitempty"`
}

// AllPeerPermissions returns every peer scope, weakest first.
func AllPeerPermissions() []string {
	return peerVocabulary.all()
}

// HasPeerPermission reports whether a granted peer set satisfies required.
// Unlike HasPermission it has no implicit default: an empty required scope is
// not a request for the strongest scope, it is a programming error, and
// answering true would silently authorize an unchecked call.
func HasPeerPermission(granted []string, required string) bool {
	return peerVocabulary.has(granted, required)
}

// ResolvePeerPermissions returns the effective peer scopes that callerBotID
// holds on calleeBotID.
//
// There is no owner or admin shortcut here, and that is deliberate: a bot is
// not a seat. Sharing an owner does not make two bots colleagues, so the only
// source of peer access is an explicit grant on the callee. Absent one, the
// answer is the empty set.
func (s *Service) ResolvePeerPermissions(ctx context.Context, calleeBotID, callerBotID string) ([]string, error) {
	if s.queries == nil {
		return nil, errors.New("bot queries not configured")
	}
	callee := strings.TrimSpace(calleeBotID)
	caller := strings.TrimSpace(callerBotID)
	if callee == "" || caller == "" {
		return nil, nil
	}
	// A self-edge cannot be stored, but resolution is a hot authorization path
	// and must not depend on that to stay correct.
	if callee == caller {
		return nil, nil
	}
	calleeUUID, err := db.ParseUUID(callee)
	if err != nil {
		return nil, err
	}
	callerUUID, err := db.ParseUUID(caller)
	if err != nil {
		return nil, err
	}
	grants, err := s.queries.ListBotPeerGrantsForCaller(ctx, sqlc.ListBotPeerGrantsForCallerParams{
		BotID:       calleeUUID,
		CallerBotID: callerUUID,
	})
	if err != nil {
		return nil, err
	}
	union := make([]string, 0, len(grants))
	for _, g := range grants {
		union = append(union, peerVocabulary.decode(g.Permissions)...)
	}
	return peerVocabulary.expand(union), nil
}

// AuthorizePeerAccess checks whether callerBotID holds the required scope on
// calleeBotID and returns the callee. It resolves the callee by id or name, so
// callers may pass whichever they hold.
func (s *Service) AuthorizePeerAccess(ctx context.Context, callerBotID, calleeBotID, required string) (Bot, error) {
	if s.queries == nil {
		return Bot{}, errors.New("bot queries not configured")
	}
	if strings.TrimSpace(required) == "" {
		return Bot{}, ErrInvalidPermission
	}
	callee, err := s.Get(ctx, calleeBotID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Bot{}, ErrBotNotFound
		}
		return Bot{}, err
	}
	perms, err := s.ResolvePeerPermissions(ctx, callee.ID, callerBotID)
	if err != nil {
		return Bot{}, err
	}
	if !HasPeerPermission(perms, required) {
		return Bot{}, ErrBotAccessDenied
	}
	return callee, nil
}

// ListReachablePeers returns every bot callerBotID may reach, for teammate
// discovery. Callers that only want contactable peers should filter on the
// returned scopes rather than expecting this to pre-filter.
func (s *Service) ListReachablePeers(ctx context.Context, callerBotID string) ([]ReachablePeer, error) {
	if s.queries == nil {
		return nil, errors.New("bot queries not configured")
	}
	callerUUID, err := db.ParseUUID(strings.TrimSpace(callerBotID))
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListBotPeerGrantsForSubject(ctx, callerUUID)
	if err != nil {
		return nil, err
	}
	// A callee can match both a blanket row and an explicit row; the effective
	// scope set is their union, and an explicit edge clears the blanket flag.
	order := make([]string, 0, len(rows))
	byBot := make(map[string]*ReachablePeer, len(rows))
	for _, row := range rows {
		botID := row.BotID.String()
		peer, ok := byBot[botID]
		if !ok {
			peer = &ReachablePeer{
				BotID:          botID,
				BotName:        row.BotName,
				BotDisplayName: row.BotDisplayName.String,
				BotAvatarURL:   row.BotAvatarUrl.String,
				ViaAnyBot:      true,
			}
			byBot[botID] = peer
			order = append(order, botID)
		}
		peer.Permissions = append(peer.Permissions, peerVocabulary.decode(row.Permissions)...)
		if row.SubjectType == PeerGrantSubjectBot {
			peer.ViaAnyBot = false
		}
	}
	items := make([]ReachablePeer, 0, len(order))
	for _, botID := range order {
		peer := byBot[botID]
		peer.Permissions = peerVocabulary.expand(peer.Permissions)
		items = append(items, *peer)
	}
	return items, nil
}

// ListPeerGrants returns every bot access grant configured on a bot.
func (s *Service) ListPeerGrants(ctx context.Context, botID string) ([]PeerGrant, error) {
	if s.queries == nil {
		return nil, errors.New("bot queries not configured")
	}
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	if _, err := s.queries.GetBotByID(ctx, botUUID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrBotNotFound
		}
		return nil, err
	}
	rows, err := s.queries.ListBotPeerGrants(ctx, botUUID)
	if err != nil {
		return nil, err
	}
	items := make([]PeerGrant, 0, len(rows))
	for _, row := range rows {
		items = append(items, PeerGrant{
			ID:                    row.ID.String(),
			BotID:                 row.BotID.String(),
			SubjectType:           row.SubjectType,
			SubjectBotID:          optionalUUIDString(row.SubjectBotID),
			SubjectBotName:        row.SubjectBotName.String,
			SubjectBotDisplayName: row.SubjectBotDisplayName.String,
			SubjectBotAvatarURL:   row.SubjectBotAvatarUrl.String,
			Permissions:           peerVocabulary.decode(row.Permissions),
			CreatedAt:             timeFromPG(row.CreatedAt),
			UpdatedAt:             timeFromPG(row.UpdatedAt),
		})
	}
	return items, nil
}

// CreatePeerGrant adds a bot access grant to botID.
func (s *Service) CreatePeerGrant(ctx context.Context, botID, createdByUserID string, req CreatePeerGrantRequest) (PeerGrant, error) {
	if s.queries == nil {
		return PeerGrant{}, errors.New("bot queries not configured")
	}
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return PeerGrant{}, err
	}
	if _, err := s.queries.GetBotByID(ctx, botUUID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PeerGrant{}, ErrBotNotFound
		}
		return PeerGrant{}, err
	}
	perms, err := peerVocabulary.normalize(req.Permissions)
	if err != nil {
		return PeerGrant{}, err
	}
	payload, err := peerVocabulary.encode(perms)
	if err != nil {
		return PeerGrant{}, err
	}

	params := sqlc.CreateBotPeerGrantParams{
		BotID:       botUUID,
		SubjectType: strings.ToLower(strings.TrimSpace(req.SubjectType)),
		Permissions: payload,
	}
	if createdBy := strings.TrimSpace(createdByUserID); createdBy != "" {
		if parsed, parseErr := db.ParseUUID(createdBy); parseErr == nil {
			params.CreatedByUserID = parsed
		}
	}

	switch params.SubjectType {
	case PeerGrantSubjectAnyBot:
		// subject_bot_id stays NULL
	case PeerGrantSubjectBot:
		subjectID := strings.TrimSpace(req.SubjectBotID)
		if subjectID == "" {
			return PeerGrant{}, ErrPeerGrantBotRequired
		}
		subjectUUID, parseErr := db.ParseUUID(subjectID)
		if parseErr != nil {
			return PeerGrant{}, parseErr
		}
		if subjectUUID == botUUID {
			return PeerGrant{}, ErrPeerGrantSelfConflict
		}
		if _, getErr := s.queries.GetBotByID(ctx, subjectUUID); getErr != nil {
			if errors.Is(getErr, pgx.ErrNoRows) {
				return PeerGrant{}, ErrPeerGrantSubjectNotFound
			}
			return PeerGrant{}, getErr
		}
		params.SubjectBotID = subjectUUID
	default:
		return PeerGrant{}, ErrInvalidGrantSubject
	}

	row, err := s.queries.CreateBotPeerGrant(ctx, params)
	if err != nil {
		if db.IsUniqueViolation(err) {
			return PeerGrant{}, ErrGrantExists
		}
		return PeerGrant{}, err
	}
	return s.peerGrantFromRow(ctx, row.ID, row.BotID, row.SubjectType, row.SubjectBotID, row.Permissions, row.CreatedAt, row.UpdatedAt), nil
}

// UpdatePeerGrant replaces the permission set of an existing peer grant.
func (s *Service) UpdatePeerGrant(ctx context.Context, botID, grantID string, req UpdatePeerGrantRequest) (PeerGrant, error) {
	if s.queries == nil {
		return PeerGrant{}, errors.New("bot queries not configured")
	}
	existing, err := s.lookupPeerGrant(ctx, botID, grantID)
	if err != nil {
		return PeerGrant{}, err
	}
	perms, err := peerVocabulary.normalize(req.Permissions)
	if err != nil {
		return PeerGrant{}, err
	}
	payload, err := peerVocabulary.encode(perms)
	if err != nil {
		return PeerGrant{}, err
	}
	row, err := s.queries.UpdateBotPeerGrantPermissions(ctx, sqlc.UpdateBotPeerGrantPermissionsParams{
		ID:          existing.ID,
		Permissions: payload,
	})
	if err != nil {
		return PeerGrant{}, err
	}
	return s.peerGrantFromRow(ctx, row.ID, row.BotID, row.SubjectType, row.SubjectBotID, row.Permissions, row.CreatedAt, row.UpdatedAt), nil
}

// DeletePeerGrant removes a peer grant from a bot.
func (s *Service) DeletePeerGrant(ctx context.Context, botID, grantID string) error {
	if s.queries == nil {
		return errors.New("bot queries not configured")
	}
	existing, err := s.lookupPeerGrant(ctx, botID, grantID)
	if err != nil {
		return err
	}
	return s.queries.DeleteBotPeerGrantByID(ctx, existing.ID)
}

// SubjectBotIDForPeerGrant returns the caller bot named by a grant, or the empty
// string for a blanket grant. Handlers use it to run the callee-side and
// caller-side authorization checks against the same row the mutation will touch.
func (s *Service) SubjectBotIDForPeerGrant(ctx context.Context, botID, grantID string) (string, error) {
	existing, err := s.lookupPeerGrant(ctx, botID, grantID)
	if err != nil {
		return "", err
	}
	return optionalUUIDString(existing.SubjectBotID), nil
}

func (s *Service) lookupPeerGrant(ctx context.Context, botID, grantID string) (sqlc.GetBotPeerGrantByIDRow, error) {
	grantUUID, err := db.ParseUUID(strings.TrimSpace(grantID))
	if err != nil {
		return sqlc.GetBotPeerGrantByIDRow{}, err
	}
	row, err := s.queries.GetBotPeerGrantByID(ctx, grantUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.GetBotPeerGrantByIDRow{}, ErrPeerGrantNotFound
		}
		return sqlc.GetBotPeerGrantByIDRow{}, err
	}
	// The grant id alone is enough to find the row, so the bot in the URL must
	// be confirmed to own it; otherwise a manager of any bot could edit another
	// bot's grants by guessing an id.
	if row.BotID.String() != strings.TrimSpace(botID) {
		return sqlc.GetBotPeerGrantByIDRow{}, ErrPeerGrantNotFound
	}
	return row, nil
}

func (s *Service) peerGrantFromRow(
	ctx context.Context,
	id, botID pgtype.UUID,
	subjectType string,
	subjectBotID pgtype.UUID,
	permissions []byte,
	createdAt, updatedAt pgtype.Timestamptz,
) PeerGrant {
	grant := PeerGrant{
		ID:           id.String(),
		BotID:        botID.String(),
		SubjectType:  subjectType,
		SubjectBotID: optionalUUIDString(subjectBotID),
		Permissions:  peerVocabulary.decode(permissions),
		CreatedAt:    timeFromPG(createdAt),
		UpdatedAt:    timeFromPG(updatedAt),
	}
	if subjectBotID.Valid {
		if subject, err := s.queries.GetBotByID(ctx, subjectBotID); err == nil {
			grant.SubjectBotName = subject.Name
			grant.SubjectBotDisplayName = subject.DisplayName.String
			grant.SubjectBotAvatarURL = subject.AvatarUrl.String
		}
	}
	return grant
}
