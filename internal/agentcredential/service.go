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

	acpprofile "github.com/memohai/memoh/internal/agent/runtime/acp/profile"
	"github.com/memohai/memoh/internal/config"
	"github.com/memohai/memoh/internal/db"
	dbsqlc "github.com/memohai/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/memohai/memoh/internal/db/store"
)

var (
	ErrNotFound              = errors.New("agent credential not found")
	ErrForbidden             = errors.New("agent credential forbidden")
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

func (s *Service) Create(ctx context.Context, ownerUserID string, req CreateRequest) (PublicCredential, error) {
	if !s.Configured() {
		return PublicCredential{}, ErrEncryptionUnavailable
	}
	ownerID, err := db.ParseUUID(ownerUserID)
	if err != nil {
		return PublicCredential{}, fmt.Errorf("%w: owner_user_id", ErrInvalidRequest)
	}
	req.Provider = normalize(req.Provider)
	req.AuthKind = normalize(req.AuthKind)
	req.Label = strings.TrimSpace(req.Label)
	if req.Label == "" || !validProviderKind(req.Provider, req.AuthKind) || !validSecret(req.AuthKind, req.Secret) {
		return PublicCredential{}, ErrInvalidRequest
	}
	ciphertext, nonce, err := s.encrypt(req.Secret)
	if err != nil {
		return PublicCredential{}, err
	}
	metadata, err := json.Marshal(nonNilMap(req.AccountMetadata))
	if err != nil {
		return PublicCredential{}, fmt.Errorf("%w: account_metadata", ErrInvalidRequest)
	}
	row, err := s.queries.CreateAgentCredential(ctx, dbsqlc.CreateAgentCredentialParams{
		OwnerUserID: ownerID, Provider: req.Provider, AuthKind: req.AuthKind, Label: req.Label,
		EncryptedPayload: ciphertext, EncryptionNonce: nonce, KeyVersion: 1,
		AccountMetadata: metadata, ExpiresAt: timestamptz(req.ExpiresAt),
	})
	if err != nil {
		return PublicCredential{}, err
	}
	return publicFromRow(row, false), nil
}

func (s *Service) ListOwned(ctx context.Context, ownerUserID string) ([]PublicCredential, error) {
	id, err := db.ParseUUID(ownerUserID)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	rows, err := s.queries.ListAgentCredentialsByOwner(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]PublicCredential, 0, len(rows))
	for _, row := range rows {
		out = append(out, publicFromRow(row, false))
	}
	return out, nil
}

func (s *Service) UpdateLabel(ctx context.Context, ownerUserID, credentialID, label string) (PublicCredential, error) {
	ownerID, credID, err := parseTwoIDs(ownerUserID, credentialID)
	if err != nil || strings.TrimSpace(label) == "" {
		return PublicCredential{}, ErrInvalidRequest
	}
	row, err := s.queries.UpdateAgentCredentialLabel(ctx, dbsqlc.UpdateAgentCredentialLabelParams{Label: strings.TrimSpace(label), ID: credID, OwnerUserID: ownerID})
	if errors.Is(err, pgx.ErrNoRows) {
		return PublicCredential{}, ErrNotFound
	}
	if err != nil {
		return PublicCredential{}, err
	}
	return publicFromRow(row, false), nil
}

func (s *Service) Revoke(ctx context.Context, ownerUserID, credentialID string) (PublicCredential, error) {
	ownerID, credID, err := parseTwoIDs(ownerUserID, credentialID)
	if err != nil {
		return PublicCredential{}, ErrInvalidRequest
	}
	row, err := s.queries.RevokeAgentCredential(ctx, dbsqlc.RevokeAgentCredentialParams{ID: credID, OwnerUserID: ownerID})
	if errors.Is(err, pgx.ErrNoRows) {
		return PublicCredential{}, ErrNotFound
	}
	if err != nil {
		return PublicCredential{}, err
	}
	return publicFromRow(row, false), nil
}

func (s *Service) BindingTargets(ctx context.Context, credentialID string) ([]BindingTarget, error) {
	id, err := db.ParseUUID(credentialID)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	rows, err := s.queries.ListAgentCredentialBindings(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]BindingTarget, 0, len(rows))
	for _, row := range rows {
		out = append(out, BindingTarget{BotID: row.BotID.String(), AgentID: row.AgentID})
	}
	return out, nil
}

func (s *Service) Bind(ctx context.Context, ownerUserID, botID, agentID, credentialID string, makeDefault bool) (PublicCredential, error) {
	ownerID, credID, err := parseTwoIDs(ownerUserID, credentialID)
	if err != nil {
		return PublicCredential{}, ErrInvalidRequest
	}
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return PublicCredential{}, ErrInvalidRequest
	}
	agentID = acpprofile.NormalizeAgentID(agentID)
	row, err := s.queries.GetAgentCredential(ctx, credID)
	if errors.Is(err, pgx.ErrNoRows) {
		return PublicCredential{}, ErrNotFound
	}
	if err != nil {
		return PublicCredential{}, err
	}
	if row.OwnerUserID != ownerID {
		return PublicCredential{}, ErrForbidden
	}
	if row.RevokedAt.Valid {
		return PublicCredential{}, ErrRevoked
	}
	if !Compatible(agentID, row.AuthKind) {
		return PublicCredential{}, ErrIncompatible
	}
	if _, err := s.queries.BindBotAgentCredential(ctx, dbsqlc.BindBotAgentCredentialParams{BotID: botUUID, AgentID: agentID, CredentialID: credID}); err != nil {
		return PublicCredential{}, err
	}
	if makeDefault {
		if err := s.SetDefault(ctx, botID, agentID, credentialID); err != nil {
			return PublicCredential{}, err
		}
	}
	return publicFromRow(row, makeDefault), nil
}

func (s *Service) SetDefault(ctx context.Context, botID, agentID, credentialID string) error {
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return ErrInvalidRequest
	}
	credID, err := db.ParseUUID(credentialID)
	if err != nil {
		return ErrInvalidRequest
	}
	agentID = acpprofile.NormalizeAgentID(agentID)
	setDefault := func(q dbstore.Queries) error {
		if _, err := q.GetBotAgentCredential(ctx, dbsqlc.GetBotAgentCredentialParams{BotID: botUUID, AgentID: agentID, CredentialID: credID}); errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if err := q.ClearBotAgentCredentialDefault(ctx, dbsqlc.ClearBotAgentCredentialDefaultParams{BotID: botUUID, AgentID: agentID}); err != nil {
			return err
		}
		_, err := q.SetBotAgentCredentialDefault(ctx, dbsqlc.SetBotAgentCredentialDefaultParams{BotID: botUUID, AgentID: agentID, CredentialID: credID})
		return err
	}
	if tx, ok := s.queries.(interface {
		InTx(context.Context, func(dbstore.Queries) error) error
	}); ok {
		return tx.InTx(ctx, setDefault)
	}
	return setDefault(s.queries)
}

func (s *Service) Unbind(ctx context.Context, botID, agentID, credentialID string) error {
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return ErrInvalidRequest
	}
	credID, err := db.ParseUUID(credentialID)
	if err != nil {
		return ErrInvalidRequest
	}
	rows, err := s.queries.UnbindBotAgentCredential(ctx, dbsqlc.UnbindBotAgentCredentialParams{BotID: botUUID, AgentID: acpprofile.NormalizeAgentID(agentID), CredentialID: credID})
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) ListBindings(ctx context.Context, botID, agentID string) ([]PublicCredential, error) {
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	rows, err := s.queries.ListBotAgentCredentials(ctx, dbsqlc.ListBotAgentCredentialsParams{BotID: botUUID, AgentID: acpprofile.NormalizeAgentID(agentID)})
	if err != nil {
		return nil, err
	}
	out := make([]PublicCredential, 0, len(rows))
	for _, row := range rows {
		out = append(out, publicFromBinding(row))
	}
	return out, nil
}

func (s *Service) Resolve(ctx context.Context, botID, agentID, credentialID string) (ResolvedCredential, error) {
	if !s.Configured() {
		return ResolvedCredential{}, ErrEncryptionUnavailable
	}
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return ResolvedCredential{}, ErrInvalidRequest
	}
	credID, err := db.ParseUUID(credentialID)
	if err != nil {
		return ResolvedCredential{}, ErrInvalidRequest
	}
	row, err := s.queries.GetBotAgentCredential(ctx, dbsqlc.GetBotAgentCredentialParams{BotID: botUUID, AgentID: acpprofile.NormalizeAgentID(agentID), CredentialID: credID})
	if errors.Is(err, pgx.ErrNoRows) {
		return ResolvedCredential{}, ErrNotFound
	}
	if err != nil {
		return ResolvedCredential{}, err
	}
	if row.RevokedAt.Valid {
		return ResolvedCredential{}, ErrRevoked
	}
	if !Compatible(agentID, row.AuthKind) {
		return ResolvedCredential{}, ErrIncompatible
	}
	secret, err := s.decrypt(row.EncryptedPayload, row.EncryptionNonce, row.KeyVersion)
	if err != nil {
		return ResolvedCredential{}, err
	}
	return ResolvedCredential{PublicCredential: publicFromGetBinding(row), Secret: secret}, nil
}

func (s *Service) ResolveDefault(ctx context.Context, botID, agentID string) (ResolvedCredential, error) {
	if !s.Configured() {
		return ResolvedCredential{}, ErrEncryptionUnavailable
	}
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return ResolvedCredential{}, ErrInvalidRequest
	}
	row, err := s.queries.GetDefaultBotAgentCredential(ctx, dbsqlc.GetDefaultBotAgentCredentialParams{BotID: botUUID, AgentID: acpprofile.NormalizeAgentID(agentID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return ResolvedCredential{}, ErrNotFound
	}
	if err != nil {
		return ResolvedCredential{}, err
	}
	if row.RevokedAt.Valid {
		return ResolvedCredential{}, ErrRevoked
	}
	secret, err := s.decrypt(row.EncryptedPayload, row.EncryptionNonce, row.KeyVersion)
	if err != nil {
		return ResolvedCredential{}, err
	}
	return ResolvedCredential{PublicCredential: publicFromDefaultBinding(row), Secret: secret}, nil
}

func (s *Service) UpdateSecretCAS(ctx context.Context, credentialID string, expectedVersion int64, secret map[string]string, accountMetadata map[string]any, expiresAt *time.Time) (PublicCredential, error) {
	if !s.Configured() {
		return PublicCredential{}, ErrEncryptionUnavailable
	}
	id, err := db.ParseUUID(credentialID)
	if err != nil || expectedVersion <= 0 {
		return PublicCredential{}, ErrInvalidRequest
	}
	ciphertext, nonce, err := s.encrypt(secret)
	if err != nil {
		return PublicCredential{}, err
	}
	meta, err := json.Marshal(nonNilMap(accountMetadata))
	if err != nil {
		return PublicCredential{}, ErrInvalidRequest
	}
	row, err := s.queries.UpdateAgentCredentialPayloadCAS(ctx, dbsqlc.UpdateAgentCredentialPayloadCASParams{
		EncryptedPayload: ciphertext, EncryptionNonce: nonce, KeyVersion: 1,
		AccountMetadata: meta, ExpiresAt: timestamptz(expiresAt), ID: id, ExpectedVersion: expectedVersion,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return PublicCredential{}, ErrNotFound
	}
	if err != nil {
		return PublicCredential{}, err
	}
	return publicFromRow(row, false), nil
}

func Compatible(agentID, authKind string) bool {
	switch acpprofile.NormalizeAgentID(agentID) {
	case acpprofile.AgentCodexID:
		return authKind == AuthKindOpenAIAPIKey || authKind == AuthKindOpenAICodexOAuth
	case acpprofile.AgentClaudeCodeID:
		return authKind == AuthKindAnthropicAPIKey || authKind == AuthKindClaudeCodeOAuth
	case acpprofile.AgentHermesID:
		return authKind == AuthKindOpenAIAPIKey || authKind == AuthKindGoogleAPIKey || authKind == AuthKindOpenRouterAPIKey
	default:
		return false
	}
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

func validProviderKind(provider, kind string) bool {
	want := map[string]string{AuthKindOpenAIAPIKey: ProviderOpenAI, AuthKindOpenAICodexOAuth: ProviderOpenAI, AuthKindAnthropicAPIKey: ProviderAnthropic, AuthKindClaudeCodeOAuth: ProviderAnthropic, AuthKindGoogleAPIKey: ProviderGoogle, AuthKindOpenRouterAPIKey: ProviderOpenRouter}
	return want[kind] == provider
}

func validSecret(kind string, secret map[string]string) bool {
	required := []string{"api_key"}
	if kind == AuthKindOpenAICodexOAuth {
		required = []string{"access_token", "id_token", "refresh_token", "account_id"}
	}
	if kind == AuthKindClaudeCodeOAuth {
		required = []string{"oauth_token"}
	}
	for _, key := range required {
		if strings.TrimSpace(secret[key]) == "" {
			return false
		}
	}
	return true
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

func publicFromRow(row dbsqlc.AgentCredential, isDefault bool) PublicCredential {
	var expires *time.Time
	if row.ExpiresAt.Valid {
		t := row.ExpiresAt.Time
		expires = &t
	}
	return PublicCredential{ID: row.ID.String(), OwnerUserID: row.OwnerUserID.String(), Provider: row.Provider, AuthKind: row.AuthKind, Label: row.Label, AccountMetadata: metadata(row.AccountMetadata), ExpiresAt: expires, CredentialVersion: row.CredentialVersion, Revoked: row.RevokedAt.Valid, IsDefault: isDefault, CreatedAt: db.TimeFromPg(row.CreatedAt), UpdatedAt: db.TimeFromPg(row.UpdatedAt)}
}

func publicFromBinding(row dbsqlc.ListBotAgentCredentialsRow) PublicCredential {
	return publicFromFields(row.ID, row.OwnerUserID, row.Provider, row.AuthKind, row.Label, row.AccountMetadata, row.ExpiresAt, row.CredentialVersion, row.RevokedAt, row.IsDefault, row.CreatedAt, row.UpdatedAt)
}

func publicFromGetBinding(row dbsqlc.GetBotAgentCredentialRow) PublicCredential {
	return publicFromFields(row.ID, row.OwnerUserID, row.Provider, row.AuthKind, row.Label, row.AccountMetadata, row.ExpiresAt, row.CredentialVersion, row.RevokedAt, row.IsDefault, row.CreatedAt, row.UpdatedAt)
}

func publicFromDefaultBinding(row dbsqlc.GetDefaultBotAgentCredentialRow) PublicCredential {
	return publicFromFields(row.ID, row.OwnerUserID, row.Provider, row.AuthKind, row.Label, row.AccountMetadata, row.ExpiresAt, row.CredentialVersion, row.RevokedAt, row.IsDefault, row.CreatedAt, row.UpdatedAt)
}

func publicFromFields(id, owner pgtype.UUID, provider, kind, label string, meta []byte, exp pgtype.Timestamptz, version int64, revoked pgtype.Timestamptz, def bool, created, updated pgtype.Timestamptz) PublicCredential {
	var expires *time.Time
	if exp.Valid {
		t := exp.Time
		expires = &t
	}
	return PublicCredential{ID: id.String(), OwnerUserID: owner.String(), Provider: provider, AuthKind: kind, Label: label, AccountMetadata: metadata(meta), ExpiresAt: expires, CredentialVersion: version, Revoked: revoked.Valid, IsDefault: def, CreatedAt: db.TimeFromPg(created), UpdatedAt: db.TimeFromPg(updated)}
}
