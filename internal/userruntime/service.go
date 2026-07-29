package userruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	dbstore "github.com/memohai/memoh/internal/db/store"
)

var (
	ErrInvalidInput              = errors.New("invalid runtime input")
	ErrRuntimeConnectionNotReady = errors.New("runtime connection is no longer ready")
)

// maxRuntimeNameBytes matches the hostname cap in handshake metadata and
// keeps (user_id, lower(name)) well under the Postgres btree index row limit.
const maxRuntimeNameBytes = 255

type ConnectionCommitGuard func() error

// Service owns the small persistent credential registry and the in-memory
// reverse-RPC connections. Bot/session routing belongs outside this package.
type Service struct {
	store          dbstore.UserRuntimeStore
	hub            *Hub
	lifecycleLocks *runtimeLifecycleLocks
}

func NewService(store dbstore.UserRuntimeStore, hub *Hub) *Service {
	return &Service{
		store:          store,
		hub:            hub,
		lifecycleLocks: newRuntimeLifecycleLocks(),
	}
}

func (s *Service) CreateRuntime(ctx context.Context, userID string, req CreateRuntimeRequest) (Runtime, error) {
	if s == nil || s.store == nil {
		return Runtime{}, errors.New("user runtime service not configured")
	}
	userID = strings.TrimSpace(userID)
	name := strings.TrimSpace(req.Name)
	if userID == "" || name == "" {
		return Runtime{}, ErrInvalidInput
	}
	if len(name) > maxRuntimeNameBytes || strings.ContainsRune(name, '\x00') || !utf8.ValidString(name) {
		return Runtime{}, fmt.Errorf("%w: name must be valid UTF-8 of at most %d bytes", ErrInvalidInput, maxRuntimeNameBytes)
	}
	key, err := NewKey()
	if err != nil {
		return Runtime{}, err
	}
	row, err := s.store.CreateUserRuntime(ctx, dbstore.CreateUserRuntimeInput{
		UserID: userID, Name: name, APIToken: key,
	})
	if err != nil {
		return Runtime{}, err
	}
	return runtimeFromRecord(row, nil), nil
}

func (s *Service) ListRuntimes(ctx context.Context, userID string) ([]Runtime, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("user runtime service not configured")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, ErrInvalidInput
	}
	rows, err := s.store.ListUserRuntimes(ctx, userID)
	if err != nil {
		return nil, err
	}
	items := make([]Runtime, 0, len(rows))
	for _, row := range rows {
		items = append(items, runtimeFromRecord(row, s.connection(row.ID)))
	}
	return items, nil
}

func (s *Service) RevokeRuntime(ctx context.Context, userID, runtimeID string) error {
	if s == nil || s.store == nil || s.lifecycleLocks == nil {
		return errors.New("user runtime service not configured")
	}
	userID = strings.TrimSpace(userID)
	runtimeID, ok := canonicalRuntimeUUID(runtimeID)
	if userID == "" || !ok {
		return ErrInvalidInput
	}
	release, err := s.lifecycleLocks.lock(ctx, runtimeID)
	if err != nil {
		return err
	}
	defer release()
	if err := s.store.RevokeUserRuntime(ctx, runtimeID, userID); err != nil {
		return err
	}
	if s.hub != nil {
		s.hub.Kick(runtimeID, "runtime revoked")
	}
	return nil
}

func (s *Service) AuthenticateKey(ctx context.Context, key string) (Runtime, error) {
	row, err := s.authenticateKeyRecord(ctx, key)
	if err != nil {
		return Runtime{}, err
	}
	return runtimeFromRecord(row, s.connection(row.ID)), nil
}

func (s *Service) authenticateKeyRecord(ctx context.Context, key string) (dbstore.UserRuntimeRecord, error) {
	if s == nil || s.store == nil {
		return dbstore.UserRuntimeRecord{}, errors.New("user runtime service not configured")
	}
	key = strings.TrimSpace(key)
	if err := ValidateKeyFormat(key); err != nil {
		return dbstore.UserRuntimeRecord{}, err
	}
	row, err := s.store.GetUserRuntimeByAPIToken(ctx, key)
	if err != nil {
		return dbstore.UserRuntimeRecord{}, err
	}
	if row.APIToken != key {
		return dbstore.UserRuntimeRecord{}, ErrInvalidKey
	}
	return row, nil
}

// ActivateConnection rechecks the credential at the publication boundary so
// a concurrent revoke can never leave a newly published connection alive.
func (s *Service) ActivateConnection(ctx context.Context, key, runtimeID string, info HandshakeInfo, connection *Connection, guard ConnectionCommitGuard) error {
	if s == nil || s.store == nil || s.hub == nil || s.lifecycleLocks == nil || connection == nil || connection.Client == nil || strings.TrimSpace(connection.ConnectionID) == "" || guard == nil {
		return errors.New("runtime connection service not configured")
	}
	runtimeID, ok := canonicalRuntimeUUID(runtimeID)
	if !ok {
		return ErrInvalidInput
	}
	release, err := s.lifecycleLocks.lock(ctx, runtimeID)
	if err != nil {
		return err
	}
	defer release()
	row, err := s.authenticateKeyRecord(ctx, key)
	if err != nil {
		return err
	}
	if row.ID != runtimeID {
		return ErrInvalidKey
	}
	connection.RuntimeID = runtimeID
	connection.Info = RuntimeInfo{
		WorkspaceBase: info.WorkspaceBase,
		Hostname:      info.Hostname,
		OS:            info.OS,
		Arch:          info.Arch,
		ClientVersion: info.ClientVersion,
		Capabilities:  append([]string(nil), info.Capabilities...),
	}
	return s.hub.registerGuarded(connection, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return guard()
	})
}

func (s *Service) DeactivateConnection(runtimeID string, connection *Connection, reason string) {
	if s == nil || s.hub == nil {
		return
	}
	s.hub.unregister(runtimeID, connection, reason)
}

func (s *Service) Connection(runtimeID string) (*Connection, bool) {
	runtimeID, ok := canonicalRuntimeUUID(runtimeID)
	if !ok || s == nil || s.hub == nil {
		return nil, false
	}
	return s.hub.Get(runtimeID)
}

func (s *Service) connection(runtimeID string) *Connection {
	connection, _ := s.Connection(runtimeID)
	return connection
}

func canonicalRuntimeUUID(value string) (string, bool) {
	parsed, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return "", false
	}
	return parsed.String(), true
}

func runtimeFromRecord(row dbstore.UserRuntimeRecord, connection *Connection) Runtime {
	runtime := Runtime{
		ID: row.ID, TeamID: row.TeamID, Name: row.Name, Key: row.APIToken, CreatedAt: row.CreatedAt,
	}
	if connection == nil {
		return runtime
	}
	runtime.Online = true
	runtime.WorkspaceBase = connection.Info.WorkspaceBase
	runtime.Hostname = connection.Info.Hostname
	runtime.OS = connection.Info.OS
	runtime.Arch = connection.Info.Arch
	runtime.ClientVersion = connection.Info.ClientVersion
	runtime.Capabilities = append([]string(nil), connection.Info.Capabilities...)
	return runtime
}
