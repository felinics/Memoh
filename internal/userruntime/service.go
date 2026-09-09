package userruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/db"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

var (
	ErrInvalidInput              = errors.New("invalid runtime input")
	ErrRuntimeConnectionNotReady = errors.New("runtime connection is no longer ready")
)

// maxRuntimeNameBytes matches the hostname cap in handshake metadata and
// keeps (user_id, lower(name)) well under the Postgres btree index row limit.
const maxRuntimeNameBytes = 255

type ConnectionCommitGuard func() error

// Service owns the persistent pending/activated credential registry and the
// in-memory reverse-RPC connections. Bot/session routing belongs outside this
// package.
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

// DefaultRuntimeName is the placeholder assigned at credential creation when
// the caller does not pick a name. It is derived from the credential key (not
// user input), so the first handshake can safely replace it with the
// machine's hostname without ever touching a user-chosen name.
func DefaultRuntimeName(key string) string {
	tail := strings.TrimPrefix(key, "mrk_")
	if len(tail) > 6 {
		tail = tail[:6]
	}
	return "Computer " + tail
}

func (s *Service) CreateRuntime(ctx context.Context, userID string, req CreateRuntimeRequest) (Runtime, error) {
	if s == nil || s.store == nil {
		return Runtime{}, errors.New("user runtime service not configured")
	}
	userID = strings.TrimSpace(userID)
	name := strings.TrimSpace(req.Name)
	if userID == "" {
		return Runtime{}, ErrInvalidInput
	}
	if len(name) > maxRuntimeNameBytes || strings.ContainsRune(name, '\x00') || !utf8.ValidString(name) {
		return Runtime{}, fmt.Errorf("%w: name must be valid UTF-8 of at most %d bytes", ErrInvalidInput, maxRuntimeNameBytes)
	}
	if err := s.store.ExpirePendingUserRuntimes(ctx, userID); err != nil {
		return Runtime{}, err
	}
	key, err := NewKey()
	if err != nil {
		return Runtime{}, err
	}
	if name == "" {
		name = DefaultRuntimeName(key)
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
	if err := s.store.ExpirePendingUserRuntimes(ctx, userID); err != nil {
		return nil, err
	}
	rows, err := s.store.ListUserRuntimes(ctx, userID)
	if err != nil {
		return nil, err
	}
	items := make([]Runtime, 0, len(rows))
	for _, row := range rows {
		runtime := runtimeFromRecord(row, s.connection(row.ID))
		// A credential becomes a manageable computer only after a ready
		// connection consumes its short-lived pending state. Persisting that
		// boundary keeps listing correct across restarts and browser failures.
		if row.ActivatedAt.IsZero() {
			continue
		}
		items = append(items, runtime)
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

// ActivateConnection rechecks the credential and transport readiness before
// consuming the pending state, then checks readiness again at the publication
// boundary. The lifecycle lock prevents a concurrent revoke from publishing a
// replacement connection after it returns.
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
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := guard(); err != nil {
		return err
	}
	activatedRow, err := s.store.ActivateUserRuntime(ctx, runtimeID, key)
	if err != nil {
		return err
	}
	if err := s.hub.registerGuarded(connection, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return guard()
	}); err != nil {
		return err
	}
	s.backfillRuntimeName(ctx, activatedRow, info.Hostname)
	return nil
}

// backfillRuntimeName adopts the connecting machine's hostname as the display
// name while the runtime still carries its creation default. A name the user
// chose (or one already backfilled) is left untouched; a hostname that
// collides with another of the user's computers gets a numeric suffix.
func (s *Service) backfillRuntimeName(ctx context.Context, row dbstore.UserRuntimeRecord, hostname string) {
	hostname = strings.TrimSpace(hostname)
	if hostname == "" {
		return
	}
	defaultName := DefaultRuntimeName(row.APIToken)
	candidate := hostname
	for attempt := range 10 {
		if attempt > 0 {
			candidate = fmt.Sprintf("%s (%d)", hostname, attempt+1)
		}
		if len(candidate) > maxRuntimeNameBytes {
			return
		}
		updated, err := s.store.BackfillUserRuntimeName(ctx, row.ID, row.UserID, candidate, defaultName)
		if err == nil {
			// Either adopted, or the name no longer defaults (user renamed /
			// earlier handshake already backfilled) — both are terminal.
			_ = updated
			return
		}
		if !db.IsUniqueViolation(err) {
			return
		}
	}
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
