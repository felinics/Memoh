// Package connectors manages bot-scoped bindings to the trusted Connect-It
// deployment. It is intentionally separate from the raw MCP connection model.
package connectors

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	connectsdk "github.com/felinics/connect-it/sdk/go"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

var (
	ErrInvalidInput        = errors.New("invalid connector input")
	ErrNotConfigured       = errors.New("connect-it is not configured")
	ErrUpstreamUnavailable = errors.New("connect-it upstream request failed")
)

const bindingRollbackTimeout = 5 * time.Second

// Connector is Memoh's bot-level view of one Connect-It connection.
// Provider credentials and MCP session tokens are deliberately absent.
type Connector struct {
	ConnectionID  string `json:"connection_id"`
	Alias         string `json:"alias"`
	Enabled       bool   `json:"enabled"`
	ConnectorType string `json:"connector_type"`
	AuthMethod    string `json:"auth_method"`
	Status        string `json:"status"`
}

type ListResponse struct {
	Items []Connector `json:"items"`
}

// Service owns local connector bindings and the trusted Connect-It
// control-plane client.
type Service struct {
	queries dbstore.Queries
	client  *connectsdk.Client
	logger  *slog.Logger
}

func NewService(log *slog.Logger, cfg config.Config, queries dbstore.Queries) (*Service, error) {
	if log == nil {
		log = slog.Default()
	}
	service := &Service{
		queries: queries,
		logger:  log.With(slog.String("service", "connectors")),
	}
	if !cfg.ConnectIt.Configured() {
		return service, nil
	}
	client, err := connectsdk.New(cfg.ConnectIt.BaseURL, cfg.ConnectIt.APIToken)
	if err != nil {
		return nil, err
	}
	service.client = client
	return service, nil
}

func (s *Service) Configured() bool {
	return s.client != nil
}

func (s *Service) ListCatalog(ctx context.Context) ([]connectsdk.Connector, error) {
	if !s.Configured() {
		return nil, ErrNotConfigured
	}
	items, err := s.client.ListConnectors(ctx)
	return items, upstreamError(err)
}

func (s *Service) List(ctx context.Context, botID string) ([]Connector, error) {
	if !s.Configured() {
		return nil, ErrNotConfigured
	}
	bindings, err := s.listBindings(ctx, botID)
	if err != nil {
		return nil, err
	}
	items := make([]Connector, 0, len(bindings))
	for _, item := range bindings {
		connector, err := s.resolveBinding(ctx, item)
		if err != nil {
			return nil, err
		}
		items = append(items, connector)
	}
	return items, nil
}

func (s *Service) Get(ctx context.Context, botID, connectionID string) (Connector, error) {
	if !s.Configured() {
		return Connector{}, ErrNotConfigured
	}
	item, err := s.getBinding(ctx, botID, connectionID)
	if err != nil {
		return Connector{}, err
	}
	return s.resolveBinding(ctx, item)
}

func (s *Service) BeginOAuth(
	ctx context.Context,
	botID, connectorType, authMethod string,
) (connectsdk.OAuthAuthorization, error) {
	if !s.Configured() {
		return connectsdk.OAuthAuthorization{}, ErrNotConfigured
	}
	connectorType = strings.TrimSpace(connectorType)
	authMethod = strings.TrimSpace(authMethod)
	if connectorType == "" || authMethod == "" {
		return connectsdk.OAuthAuthorization{},
			fmt.Errorf("%w: connector_type and auth_method are required", ErrInvalidInput)
	}
	result, err := s.client.BeginOAuth(ctx, connectsdk.BeginOAuthRequest{
		ConnectorType: connectorType,
		AuthMethod:    authMethod,
	})
	if err != nil {
		return connectsdk.OAuthAuthorization{}, upstreamError(err)
	}
	if _, err := s.createBindingOrRollback(ctx, botID, result.ConnectionID, connectorType); err != nil {
		return connectsdk.OAuthAuthorization{}, err
	}
	return result, nil
}

func (s *Service) CreateCredential(
	ctx context.Context,
	botID, connectorType, authMethod string,
	fields map[string]string,
) (Connector, error) {
	if !s.Configured() {
		return Connector{}, ErrNotConfigured
	}
	connectorType = strings.TrimSpace(connectorType)
	authMethod = strings.TrimSpace(authMethod)
	if connectorType == "" || authMethod == "" {
		return Connector{},
			fmt.Errorf("%w: connector_type and auth_method are required", ErrInvalidInput)
	}
	result, err := s.client.CreateAPIKeyConnection(ctx, connectsdk.CreateAPIKeyConnectionRequest{
		ConnectorType: connectorType,
		AuthMethod:    authMethod,
		Fields:        fields,
	})
	if err != nil {
		return Connector{}, upstreamError(err)
	}
	item, err := s.createBindingOrRollback(ctx, botID, result.ConnectionID, connectorType)
	if err != nil {
		return Connector{}, err
	}
	return connectorFrom(item, connectsdk.Connection{
		ConnectorType: connectorType,
		AuthMethod:    authMethod,
		Status:        "active",
	}), nil
}

func (s *Service) SetEnabled(ctx context.Context, botID, connectionID string, enabled bool) error {
	if !s.Configured() {
		return ErrNotConfigured
	}
	botUUID, connectionID, err := bindingKey(botID, connectionID)
	if err != nil {
		return err
	}
	affected, err := s.queries.UpdateConnectorEnabled(ctx, dbsqlc.UpdateConnectorEnabledParams{
		BotID:        botUUID,
		ConnectionID: connectionID,
		Enabled:      enabled,
	})
	if err != nil {
		return err
	}
	if affected == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *Service) Delete(ctx context.Context, botID, connectionID string) error {
	if !s.Configured() {
		return ErrNotConfigured
	}
	item, err := s.getBinding(ctx, botID, connectionID)
	if err != nil {
		return err
	}
	if err := s.client.DeleteConnection(ctx, item.ConnectionID); err != nil {
		if !isConnectItStatus(err, http.StatusNotFound) {
			return upstreamError(err)
		}
	}
	return s.queries.DeleteConnector(ctx, dbsqlc.DeleteConnectorParams{
		BotID:        item.BotID,
		ConnectionID: item.ConnectionID,
	})
}

// CleanupBotConnectors removes every remote credential before the bot row
// cascades its local bindings. Without a configured Connect-It client the
// bindings cannot be revoked remotely, so deletion proceeds only when there
// is nothing to clean up: silently dropping bindings would orphan live
// third-party credentials inside Connect-It with no reference left to find
// them by.
func (s *Service) CleanupBotConnectors(ctx context.Context, botID string) error {
	bindings, err := s.listBindings(ctx, botID)
	if err != nil {
		return err
	}
	if len(bindings) == 0 {
		return nil
	}
	if !s.Configured() {
		return fmt.Errorf(
			"%w: %d connector binding(s) still reference Connect-It connections",
			ErrNotConfigured, len(bindings),
		)
	}
	for _, item := range bindings {
		if err := s.client.DeleteConnection(ctx, item.ConnectionID); err != nil &&
			!isConnectItStatus(err, http.StatusNotFound) {
			return upstreamError(err)
		}
	}
	return nil
}

func (s *Service) Reauthorize(
	ctx context.Context,
	botID, connectionID string,
) (connectsdk.OAuthAuthorization, error) {
	if !s.Configured() {
		return connectsdk.OAuthAuthorization{}, ErrNotConfigured
	}
	item, err := s.getBinding(ctx, botID, connectionID)
	if err != nil {
		return connectsdk.OAuthAuthorization{}, err
	}
	result, err := s.client.ReauthorizeConnection(ctx, item.ConnectionID)
	if err != nil {
		return connectsdk.OAuthAuthorization{}, upstreamError(err)
	}
	return result, nil
}

// SessionConnections returns the enabled and currently active bindings used to
// sign one aggregate Connect-It MCP session for a bot.
func (s *Service) SessionConnections(ctx context.Context, botID string) (map[string]string, error) {
	if !s.Configured() {
		return nil, nil
	}
	bindings, err := s.listBindings(ctx, botID)
	if err != nil {
		return nil, err
	}
	connections := make(map[string]string, len(bindings))
	for _, item := range bindings {
		if !item.Enabled {
			continue
		}
		connection, err := s.client.GetConnection(ctx, item.ConnectionID)
		if err != nil {
			if isConnectItStatus(err, http.StatusNotFound) {
				continue
			}
			return nil, upstreamError(err)
		}
		if strings.TrimSpace(connection.Status) != "active" {
			continue
		}
		// The stored alias is the binding's durable tool namespace: tool
		// names stay stable however the connection set changes around it.
		connections[item.Alias] = item.ConnectionID
	}
	return connections, nil
}

const aliasAllocationAttempts = 3

// createBinding allocates the binding's durable alias and inserts the row.
// The alias is the bot-scoped tool namespace and is fixed for the binding's
// lifetime: allocation happens exactly once, here, against the bot's current
// aliases, and concurrent races resolve through the unique constraint.
func (s *Service) createBinding(
	ctx context.Context,
	botID, connectionID, connectorType string,
) (dbsqlc.Connector, error) {
	botUUID, connectionID, err := bindingKey(botID, connectionID)
	if err != nil {
		return dbsqlc.Connector{}, err
	}
	existing, err := s.queries.ListConnectorsByBotID(ctx, botUUID)
	if err != nil {
		return dbsqlc.Connector{}, err
	}
	used := make(map[string]bool, len(existing))
	for _, item := range existing {
		used[item.Alias] = true
	}
	for range aliasAllocationAttempts {
		alias := allocateAlias(connectorType, used)
		item, err := s.queries.CreateConnector(ctx, dbsqlc.CreateConnectorParams{
			BotID:        botUUID,
			ConnectionID: connectionID,
			Alias:        alias,
		})
		if isUniqueViolationOn(err, "connectors_team_bot_alias_key") {
			used[alias] = true
			continue
		}
		return item, err
	}
	return dbsqlc.Connector{}, fmt.Errorf("could not allocate a connector alias for bot %s", botID)
}

func isUniqueViolationOn(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}

// createBindingOrRollback keeps the remote connection and local binding as one
// logical create operation. The two stores cannot share a transaction, so a
// failed local insert is compensated by deleting the newly created connection.
func (s *Service) createBindingOrRollback(
	ctx context.Context,
	botID, connectionID, connectorType string,
) (dbsqlc.Connector, error) {
	item, err := s.createBinding(ctx, botID, connectionID, connectorType)
	if err == nil {
		return item, nil
	}

	connectionID = strings.TrimSpace(connectionID)
	if connectionID != "" {
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bindingRollbackTimeout)
		defer cancel()
		rollbackErr := s.client.DeleteConnection(rollbackCtx, connectionID)
		if rollbackErr != nil && !isConnectItStatus(rollbackErr, http.StatusNotFound) {
			s.logger.Warn(
				"failed to roll back Connect-It connection after binding failed",
				slog.String("connection_id", connectionID),
				slog.Any("error", rollbackErr),
			)
		}
	}
	return dbsqlc.Connector{}, err
}

func (s *Service) getBinding(ctx context.Context, botID, connectionID string) (dbsqlc.Connector, error) {
	botUUID, connectionID, err := bindingKey(botID, connectionID)
	if err != nil {
		return dbsqlc.Connector{}, err
	}
	return s.queries.GetConnectorByConnectionID(ctx, dbsqlc.GetConnectorByConnectionIDParams{
		BotID:        botUUID,
		ConnectionID: connectionID,
	})
}

func (s *Service) listBindings(ctx context.Context, botID string) ([]dbsqlc.Connector, error) {
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	return s.queries.ListConnectorsByBotID(ctx, botUUID)
}

func bindingKey(botID, connectionID string) (pgtype.UUID, string, error) {
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return pgtype.UUID{}, "", fmt.Errorf("%w: invalid bot_id: %w", ErrInvalidInput, err)
	}
	connectionID = strings.TrimSpace(connectionID)
	if connectionID == "" {
		return pgtype.UUID{}, "", fmt.Errorf("%w: connection_id is required", ErrInvalidInput)
	}
	return botUUID, connectionID, nil
}

func isConnectItStatus(err error, status int) bool {
	var apiErr *connectsdk.APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == status
}

func connectorFrom(item dbsqlc.Connector, connection connectsdk.Connection) Connector {
	return Connector{
		ConnectionID:  strings.TrimSpace(item.ConnectionID),
		Alias:         strings.TrimSpace(item.Alias),
		Enabled:       item.Enabled,
		ConnectorType: strings.TrimSpace(connection.ConnectorType),
		AuthMethod:    strings.TrimSpace(connection.AuthMethod),
		Status:        strings.TrimSpace(connection.Status),
	}
}

func (s *Service) resolveBinding(ctx context.Context, item dbsqlc.Connector) (Connector, error) {
	connection, err := s.client.GetConnection(ctx, item.ConnectionID)
	if err != nil {
		if isConnectItStatus(err, http.StatusNotFound) {
			return Connector{
				ConnectionID: strings.TrimSpace(item.ConnectionID),
				Alias:        strings.TrimSpace(item.Alias),
				Enabled:      item.Enabled,
				Status:       "unavailable",
			}, nil
		}
		return Connector{}, upstreamError(err)
	}
	return connectorFrom(item, connection), nil
}

func allocateAlias(connectorType string, used map[string]bool) string {
	base := normalizeAlias(connectorType)
	for number := 1; ; number++ {
		suffix := ""
		if number > 1 {
			suffix = "-" + strconv.Itoa(number)
		}
		maxBase := 32 - len(suffix)
		candidateBase := strings.TrimRight(base[:min(len(base), maxBase)], "-")
		if candidateBase == "" {
			candidateBase = "connector"
		}
		candidate := candidateBase + suffix
		if !used[candidate] {
			return candidate
		}
	}
}

func normalizeAlias(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	previousDash := false
	for _, ch := range value {
		valid := ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9'
		if valid {
			builder.WriteRune(ch)
			previousDash = false
			continue
		}
		if builder.Len() > 0 && !previousDash {
			builder.WriteByte('-')
			previousDash = true
		}
	}
	alias := strings.Trim(builder.String(), "-")
	if alias == "" {
		return "connector"
	}
	if len(alias) > 32 {
		alias = strings.TrimRight(alias[:32], "-")
	}
	return alias
}

func upstreamError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrUpstreamUnavailable, err)
}
