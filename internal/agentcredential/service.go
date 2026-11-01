// Package agentcredential stores Agent credentials encrypted at rest.
//
// A credential belongs to a team and is referenced by at most one Bot Agent
// instance via bot_agents.agent_credential_id (enforced by product flow, not
// schema). Attach replaces an Agent's credential atomically and revokes the
// replaced row once nothing references it; runtime resolution walks
// session → bot_agent → credential and decrypts only while preparing the
// Agent process configuration.
package agentcredential

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

var (
	ErrNotFound              = errors.New("agent credential not found")
	ErrIncompatible          = errors.New("agent credential is incompatible with the agent")
	ErrRevoked               = errors.New("agent credential is revoked")
	ErrEncryptionUnavailable = errors.New("agent credential encryption is unavailable")
	ErrInvalidRequest        = errors.New("invalid agent credential request")
)

type Service struct {
	queries dbstore.Queries
	aead    cipher.AEAD
}

func NewService(queries dbstore.Queries, cfg config.Config) *Service {
	s := &Service{queries: queries}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(cfg.Auth.AgentCredentialsEncryptionKey))
	if err != nil || len(raw) != 32 {
		return s
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return s
	}
	s.aead, _ = cipher.NewGCM(block)
	return s
}

func (s *Service) Configured() bool { return s != nil && s.aead != nil && s.queries != nil }

// GetForBotAgent returns the redacted credential currently attached to a Bot
// Agent instance, or ErrNotFound when the Agent is not connected.
func (s *Service) GetForBotAgent(ctx context.Context, botID, botAgentID string) (PublicCredential, error) {
	row, err := s.botAgentRow(ctx, botID, botAgentID)
	if err != nil {
		return PublicCredential{}, err
	}
	return publicFromJoinRow(row), nil
}

// ResolveForBotAgent decrypts the credential attached to a Bot Agent instance.
func (s *Service) ResolveForBotAgent(ctx context.Context, botID, botAgentID string) (ResolvedCredential, error) {
	if !s.Configured() {
		return ResolvedCredential{}, ErrEncryptionUnavailable
	}
	row, err := s.botAgentRow(ctx, botID, botAgentID)
	if err != nil {
		return ResolvedCredential{}, err
	}
	if row.RevokedAt.Valid {
		return ResolvedCredential{}, ErrRevoked
	}
	if !Compatible(row.AgentRuntime, row.AuthKind) {
		return ResolvedCredential{}, ErrIncompatible
	}
	secret, err := s.decrypt(row.EncryptedPayload, row.EncryptionNonce, row.KeyVersion)
	if err != nil {
		return ResolvedCredential{}, err
	}
	return ResolvedCredential{PublicCredential: publicFromJoinRow(row), AgentRuntime: row.AgentRuntime, Secret: secret}, nil
}

// AttachToBotAgent encrypts a new secret, points the Bot Agent instance at it,
// and revokes the replaced credential once no other instance references it.
// The instance's provider is read inside the transaction and gates auth-kind
// compatibility, so a wrong-profile attach can never link.
func (s *Service) AttachToBotAgent(ctx context.Context, ownerUserID, botID, botAgentID string, req CreateRequest) (PublicCredential, error) {
	if !s.Configured() {
		return PublicCredential{}, ErrEncryptionUnavailable
	}
	ownerID, err := db.ParseUUID(ownerUserID)
	if err != nil {
		return PublicCredential{}, fmt.Errorf("%w: owner_user_id", ErrInvalidRequest)
	}
	botUUID, agentUUID, err := parseTwoIDs(botID, botAgentID)
	if err != nil {
		return PublicCredential{}, ErrInvalidRequest
	}
	req.Provider = normalize(req.Provider)
	req.AuthKind = normalize(req.AuthKind)
	req.Label = strings.TrimSpace(req.Label)
	if req.Label == "" {
		req.Label = defaultLabel(req.AuthKind, req.AccountMetadata)
	}
	if !validProviderKind(req.Provider, req.AuthKind) || !validSecret(req.AuthKind, req.Secret) {
		return PublicCredential{}, ErrInvalidRequest
	}
	ciphertext, nonce, err := s.encrypt(req.Secret)
	if err != nil {
		return PublicCredential{}, err
	}
	meta, err := json.Marshal(nonNilMap(req.AccountMetadata))
	if err != nil {
		return PublicCredential{}, fmt.Errorf("%w: account_metadata", ErrInvalidRequest)
	}

	var created dbsqlc.AgentCredential
	attach := func(q dbstore.Queries) error {
		agentRuntime, providerErr := q.GetBotAgentRuntime(ctx, dbsqlc.GetBotAgentRuntimeParams{BotID: botUUID, BotAgentID: agentUUID})
		if errors.Is(providerErr, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if providerErr != nil {
			return providerErr
		}
		if !Compatible(agentRuntime, req.AuthKind) {
			return ErrIncompatible
		}
		row, createErr := q.CreateAgentCredential(ctx, dbsqlc.CreateAgentCredentialParams{
			OwnerUserID: ownerID, Provider: req.Provider, AuthKind: req.AuthKind, Label: req.Label,
			EncryptedPayload: ciphertext, EncryptionNonce: nonce, KeyVersion: 1,
			AccountMetadata: meta, ExpiresAt: timestamptz(req.ExpiresAt),
		})
		if createErr != nil {
			return createErr
		}
		previous, setErr := q.SetBotAgentCredential(ctx, dbsqlc.SetBotAgentCredentialParams{
			BotID: botUUID, BotAgentID: agentUUID, CredentialID: row.ID,
		})
		if errors.Is(setErr, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if setErr != nil {
			return setErr
		}
		created = row
		return revokeIfOrphan(ctx, q, previous)
	}
	if err := s.inTx(ctx, attach); err != nil {
		return PublicCredential{}, err
	}
	return publicFromRow(created), nil
}

// DetachFromBotAgent disconnects the instance and revokes the credential once
// nothing references it. ErrNotFound covers both a missing Agent and an Agent
// that was never connected.
func (s *Service) DetachFromBotAgent(ctx context.Context, botID, botAgentID string) error {
	botUUID, agentUUID, err := parseTwoIDs(botID, botAgentID)
	if err != nil {
		return ErrInvalidRequest
	}
	detach := func(q dbstore.Queries) error {
		previous, clearErr := q.ClearBotAgentCredential(ctx, dbsqlc.ClearBotAgentCredentialParams{
			BotID: botUUID, BotAgentID: agentUUID,
		})
		if errors.Is(clearErr, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if clearErr != nil {
			return clearErr
		}
		return revokeIfOrphan(ctx, q, previous)
	}
	return s.inTx(ctx, detach)
}

// Compatible reports whether an auth kind can drive the Agent runtime.
func Compatible(agentRuntime, authKind string) bool {
	spec, ok := authKinds[authKind]
	return ok && string(spec.runtime) == normalize(agentRuntime)
}

func (s *Service) botAgentRow(ctx context.Context, botID, botAgentID string) (dbsqlc.GetBotAgentCredentialRow, error) {
	botUUID, agentUUID, err := parseTwoIDs(botID, botAgentID)
	if err != nil {
		return dbsqlc.GetBotAgentCredentialRow{}, ErrInvalidRequest
	}
	row, err := s.queries.GetBotAgentCredential(ctx, dbsqlc.GetBotAgentCredentialParams{BotID: botUUID, BotAgentID: agentUUID})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbsqlc.GetBotAgentCredentialRow{}, ErrNotFound
	}
	return row, err
}

func (s *Service) inTx(ctx context.Context, fn func(dbstore.Queries) error) error {
	if tx, ok := s.queries.(interface {
		InTx(context.Context, func(dbstore.Queries) error) error
	}); ok {
		return tx.InTx(ctx, fn)
	}
	return fn(s.queries)
}

func revokeIfOrphan(ctx context.Context, q dbstore.Queries, credentialID pgtype.UUID) error {
	if !credentialID.Valid {
		return nil
	}
	refs, err := q.CountBotAgentCredentialRefs(ctx, credentialID)
	if err != nil {
		return err
	}
	if refs > 0 {
		return nil
	}
	if _, err := q.RevokeAgentCredentialByID(ctx, credentialID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	return nil
}

func (s *Service) encrypt(secret map[string]string) ([]byte, []byte, error) {
	payload, err := json.Marshal(secret)
	if err != nil {
		return nil, nil, ErrInvalidRequest
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, err
	}
	return s.aead.Seal(nil, nonce, payload, nil), nonce, nil
}

func (s *Service) decrypt(ciphertext, nonce []byte, keyVersion int32) (map[string]string, error) {
	if keyVersion != 1 || len(nonce) != s.aead.NonceSize() {
		return nil, ErrEncryptionUnavailable
	}
	payload, err := s.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, ErrEncryptionUnavailable
	}
	var secret map[string]string
	if err := json.Unmarshal(payload, &secret); err != nil {
		return nil, ErrEncryptionUnavailable
	}
	return secret, nil
}

// ProviderForAuthKind maps an auth kind to its provider so API callers only
// submit the kind.
func ProviderForAuthKind(kind string) string {
	return authKinds[normalize(kind)].provider
}

func validProviderKind(provider, kind string) bool {
	spec, ok := authKinds[kind]
	return ok && spec.provider == provider
}

func validSecret(kind string, secret map[string]string) bool {
	spec, ok := authKinds[kind]
	if !ok {
		return false
	}
	for _, key := range spec.secret {
		if strings.TrimSpace(secret[key]) == "" {
			return false
		}
	}
	return true
}

func defaultLabel(kind string, meta map[string]any) string {
	label := authKinds[kind].label
	if account, ok := meta["account_id"].(string); ok && account != "" {
		return label + " " + account
	}
	return label
}

func normalize(v string) string { return strings.ToLower(strings.TrimSpace(v)) }

func nonNilMap(v map[string]any) map[string]any {
	if v == nil {
		return map[string]any{}
	}
	return v
}

func timestamptz(v *time.Time) pgtype.Timestamptz {
	if v == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: v.UTC(), Valid: true}
}

func parseTwoIDs(a, b string) (pgtype.UUID, pgtype.UUID, error) {
	x, err := db.ParseUUID(a)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, err
	}
	y, err := db.ParseUUID(b)
	return x, y, err
}

func metadata(raw []byte) map[string]any {
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil || out == nil {
		return map[string]any{}
	}
	return out
}

func publicFromRow(row dbsqlc.AgentCredential) PublicCredential {
	return publicFromFields(row.ID, row.OwnerUserID, row.Provider, row.AuthKind, row.Label, row.AccountMetadata, row.ExpiresAt, row.CredentialVersion, row.RevokedAt, row.CreatedAt, row.UpdatedAt)
}

func publicFromJoinRow(row dbsqlc.GetBotAgentCredentialRow) PublicCredential {
	return publicFromFields(row.ID, row.OwnerUserID, row.Provider, row.AuthKind, row.Label, row.AccountMetadata, row.ExpiresAt, row.CredentialVersion, row.RevokedAt, row.CreatedAt, row.UpdatedAt)
}

func publicFromFields(id, owner pgtype.UUID, provider, kind, label string, meta []byte, exp pgtype.Timestamptz, version int64, revoked pgtype.Timestamptz, created, updated pgtype.Timestamptz) PublicCredential {
	var expires *time.Time
	if exp.Valid {
		t := exp.Time
		expires = &t
	}
	return PublicCredential{ID: id.String(), OwnerUserID: owner.String(), Provider: provider, AuthKind: kind, Label: label, AccountMetadata: metadata(meta), ExpiresAt: expires, CredentialVersion: version, Revoked: revoked.Valid, CreatedAt: db.TimeFromPg(created), UpdatedAt: db.TimeFromPg(updated)}
}
