package agentcredential

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

var (
	ErrAuthorizationExpired  = errors.New("authorization session expired or unavailable")
	ErrAuthorizationNotReady = errors.New("authorization is not complete")
	ErrAuthorizationLimit    = errors.New("too many pending authorizations")
)

const authorizationTTL = 30 * time.Minute

// AuthorizationService stages encrypted credentials independently of a Bot.
// Row locks serialize one-time claims across server instances.
type AuthorizationService struct {
	credentials *Service
}

func NewAuthorizationService(credentials *Service) *AuthorizationService {
	return &AuthorizationService{credentials: credentials}
}

// A struct tag cannot be computed, so the runtime enum repeats the runtimes of
// the authKinds table; TestAuthorizationRuntimeEnumMatchesAuthKinds pins it.
type AuthorizationRequest struct {
	Runtime  string            `json:"runtime" validate:"required" enums:"codex,claude-code,grok-build"`
	AuthKind string            `json:"auth_kind" validate:"required"`
	Secret   map[string]string `json:"secret,omitempty"`
}

// Authorization contains only the public handoff, never credentials or provider device IDs.
type Authorization struct {
	ID        string    `json:"id" validate:"required"`
	Runtime   string    `json:"runtime" validate:"required"`
	AuthKind  string    `json:"auth_kind" validate:"required"`
	Status    string    `json:"status" validate:"required" enums:"ready,claimed"`
	ExpiresAt time.Time `json:"expires_at" validate:"required"`
}

func (s *AuthorizationService) Create(ctx context.Context, owner string, req AuthorizationRequest) (Authorization, error) {
	if !s.credentials.Configured() {
		return Authorization{}, ErrEncryptionUnavailable
	}
	ownerID, err := db.ParseUUID(owner)
	if err != nil {
		return Authorization{}, ErrInvalidRequest
	}
	req.Runtime = normalize(req.Runtime)
	if !Compatible(req.Runtime, req.AuthKind) {
		return Authorization{}, ErrInvalidRequest
	}
	if !validSecret(req.AuthKind, req.Secret) {
		return Authorization{}, ErrInvalidRequest
	}
	var result Authorization
	err = s.credentials.inTx(ctx, func(q dbstore.Queries) error {
		// Bound abandoned sessions per user, including concurrent starts.
		if err := q.LockAgentAuthorizationOwner(ctx, owner); err != nil {
			return err
		}
		if err := q.PruneAgentAuthorizations(ctx); err != nil {
			return err
		}
		count, err := q.CountAgentAuthorizations(ctx, ownerID)
		if err != nil {
			return err
		}
		if count >= 5 {
			return ErrAuthorizationLimit
		}
		expires := time.Now().Add(authorizationTTL)
		ciphertext, nonce, err := s.credentials.encrypt(req.Secret)
		if err != nil {
			return err
		}
		row, err := q.CreateAgentAuthorization(ctx, dbsqlc.CreateAgentAuthorizationParams{
			OwnerUserID: ownerID, Runtime: req.Runtime, AuthKind: req.AuthKind, Status: "ready",
			EncryptedPayload: ciphertext, EncryptionNonce: nonce, ExpiresAt: timestamptz(&expires),
			PollAfter: timestamptz(timePtr(time.Now())),
		})
		if err != nil {
			return err
		}
		result = authorizationView(row)
		return nil
	})
	return result, err
}

func (s *AuthorizationService) Get(ctx context.Context, owner, id string) (Authorization, error) {
	var result Authorization
	err := s.withSession(ctx, owner, id, func(_ dbstore.Queries, row dbsqlc.AgentAuthorization) error {
		result = authorizationView(row)
		return nil
	})
	return result, err
}

// Claim is idempotent for the same Agent and cannot attach a session to a second
// Agent. Binding, credential encryption, and erasing staged secrets commit together.
func (s *AuthorizationService) Claim(ctx context.Context, owner, id, botID, agentID string) (PublicCredential, error) {
	var result PublicCredential
	err := s.withSession(ctx, owner, id, func(q dbstore.Queries, row dbsqlc.AgentAuthorization) error {
		creds := &Service{queries: q, aead: s.credentials.aead}
		if row.Status == "claimed" {
			if row.ClaimedAgentID.String() != agentID {
				return ErrRevoked
			}
			var err error
			result, err = creds.GetForBotAgent(ctx, botID, agentID)
			return err
		}
		if row.Status != "ready" {
			return ErrAuthorizationNotReady
		}
		secret, err := s.credentials.decrypt(row.EncryptedPayload, row.EncryptionNonce, 1)
		if err != nil {
			return err
		}
		result, err = creds.AttachToBotAgent(ctx, owner, botID, agentID, CreateRequest{
			Provider: ProviderForAuthKind(row.AuthKind), AuthKind: row.AuthKind, Secret: secret,
		})
		if err != nil {
			return err
		}
		row.ClaimedAgentID, err = db.ParseUUID(agentID)
		if err != nil {
			return ErrInvalidRequest
		}
		row.Status = "claimed"
		row.EncryptedPayload = []byte{}
		row.EncryptionNonce = []byte{}
		_, err = q.UpdateAgentAuthorization(ctx, authorizationUpdate(row))
		return err
	})
	return result, err
}

func (s *AuthorizationService) Cancel(ctx context.Context, owner, id string) error {
	sessionID, ownerID, err := parseTwoIDs(id, owner)
	if err != nil {
		return ErrInvalidRequest
	}
	return s.credentials.queries.DeleteAgentAuthorization(ctx, dbsqlc.DeleteAgentAuthorizationParams{ID: sessionID, OwnerUserID: ownerID})
}

func (s *AuthorizationService) withSession(ctx context.Context, owner, id string, fn func(dbstore.Queries, dbsqlc.AgentAuthorization) error) error {
	if !s.credentials.Configured() {
		return ErrEncryptionUnavailable
	}
	sessionID, ownerID, err := parseTwoIDs(id, owner)
	if err != nil {
		return ErrAuthorizationExpired
	}
	return s.credentials.inTx(ctx, func(q dbstore.Queries) error {
		row, err := q.GetAgentAuthorization(ctx, dbsqlc.GetAgentAuthorizationParams{ID: sessionID, OwnerUserID: ownerID})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAuthorizationExpired
		}
		if err != nil {
			return err
		}
		return fn(q, row)
	})
}

func authorizationView(row dbsqlc.AgentAuthorization) Authorization {
	return Authorization{ID: row.ID.String(), Runtime: row.Runtime, AuthKind: row.AuthKind, Status: row.Status, ExpiresAt: row.ExpiresAt.Time}
}

func authorizationUpdate(row dbsqlc.AgentAuthorization) dbsqlc.UpdateAgentAuthorizationParams {
	return dbsqlc.UpdateAgentAuthorizationParams{
		ID: row.ID, Status: row.Status,
		EncryptedPayload: row.EncryptedPayload, EncryptionNonce: row.EncryptionNonce,
		ExpiresAt: row.ExpiresAt, PollAfter: row.PollAfter, ClaimedAgentID: row.ClaimedAgentID,
	}
}

func timePtr(t time.Time) *time.Time { return &t }
