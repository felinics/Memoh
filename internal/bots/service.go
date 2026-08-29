package bots

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/acl"
	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/runtimefence"
	tzutil "github.com/felinics/memoh/internal/timezone"
	"github.com/felinics/memoh/internal/workspace"
)

// Service provides bot CRUD and membership management.
type Service struct {
	queries               dbstore.Queries
	logger                *slog.Logger
	containerLifecycle    ContainerLifecycle
	connectorLifecycle    ConnectorLifecycle
	checkers              []RuntimeChecker
	containerReachability func(ctx context.Context, botID string) error
}

const (
	botLifecycleOperationTimeout   = 5 * time.Minute
	botRuntimeConfigPublishTimeout = 30 * time.Second
)

var (
	ErrBotNotFound       = errors.New("bot not found")
	ErrBotAccessDenied   = errors.New("bot access denied")
	ErrOwnerUserNotFound = errors.New("owner user not found")
	ErrBotNameTaken      = errors.New("bot name already taken")
	ErrBotNameInvalid    = errors.New("bot name is invalid")
	ErrBotNameReserved   = errors.New("bot name is reserved")
)

// NewService creates a new bot service.
func NewService(log *slog.Logger, queries dbstore.Queries) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		queries: queries,
		logger:  log.With(slog.String("service", "bots")),
	}
}

// SetContainerLifecycle registers a container lifecycle handler for bot operations.
func (s *Service) SetContainerLifecycle(lc ContainerLifecycle) {
	s.containerLifecycle = lc
}

// SetConnectorLifecycle registers connector cleanup for bot deletion.
func (s *Service) SetConnectorLifecycle(lc ConnectorLifecycle) {
	s.connectorLifecycle = lc
}

// SetContainerReachability registers a function that checks whether a bot's
// container is reachable via gRPC. Returns nil on success, error otherwise.
func (s *Service) SetContainerReachability(fn func(ctx context.Context, botID string) error) {
	s.containerReachability = fn
}

// AddRuntimeChecker registers an additional runtime checker.
func (s *Service) AddRuntimeChecker(c RuntimeChecker) {
	if c != nil {
		s.checkers = append(s.checkers, c)
	}
}

// AuthorizeAccess checks whether userID may access the given bot (owner or admin only).
func (s *Service) AuthorizeAccess(ctx context.Context, userID, botID string, isAdmin bool) (Bot, error) {
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
	if isAdmin || bot.OwnerUserID == userID {
		return bot, nil
	}
	return Bot{}, ErrBotAccessDenied
}

// Create creates a new bot owned by owner user.
func (s *Service) Create(ctx context.Context, ownerUserID string, req CreateBotRequest) (Bot, error) {
	if s.queries == nil {
		return Bot{}, errors.New("bot queries not configured")
	}
	ownerID := strings.TrimSpace(ownerUserID)
	if ownerID == "" {
		return Bot{}, errors.New("owner user id is required")
	}
	ownerUUID, err := db.ParseUUID(ownerID)
	if err != nil {
		return Bot{}, err
	}
	if err := s.ensureUserExists(ctx, ownerUUID); err != nil {
		return Bot{}, err
	}
	aclPresetKey := acl.NormalizePresetKey(req.AclPreset)
	if _, err := acl.ResolvePreset(aclPresetKey); err != nil {
		return Bot{}, err
	}
	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		displayName = "bot-" + uuid.NewString()
	}
	botName, err := s.resolveName(ctx, req.Name, displayName, "")
	if err != nil {
		return Bot{}, err
	}
	avatarURL := strings.TrimSpace(req.AvatarURL)
	isActive := true
	if req.IsActive != nil {
		isActive = *req.IsActive
	}
	timezoneValue, err := normalizeOptionalTimezone(req.Timezone)
	if err != nil {
		return Bot{}, err
	}
	metadata := req.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	payload, err := json.Marshal(metadata)
	if err != nil {
		return Bot{}, err
	}
	row, err := s.queries.CreateBot(ctx, sqlc.CreateBotParams{
		OwnerUserID: ownerUUID,
		Name:        botName,
		DisplayName: pgtype.Text{String: displayName, Valid: displayName != ""},
		AvatarUrl:   pgtype.Text{String: avatarURL, Valid: avatarURL != ""},
		Timezone:    timezoneValue,
		IsActive:    isActive,
		Metadata:    payload,
		Status:      BotStatusCreating,
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			return Bot{}, ErrBotNameTaken
		}
		return Bot{}, err
	}
	bot, err := toBot(asSQLCBot(row))
	if err != nil {
		return Bot{}, err
	}
	if err := acl.ApplyPreset(ctx, s.queries, bot.ID, ownerID, aclPresetKey); err != nil {
		if cleanupErr := s.queries.DeleteBotByID(ctx, row.ID); cleanupErr != nil {
			return Bot{}, errors.Join(
				fmt.Errorf("apply acl preset: %w", err),
				fmt.Errorf("cleanup bot after acl preset failure: %w", cleanupErr),
			)
		}
		return Bot{}, fmt.Errorf("apply acl preset: %w", err)
	}
	if err := s.attachCheckSummary(ctx, &bot, asSQLCBot(row)); err != nil {
		return Bot{}, err
	}
	if req.SkipLifecycle {
		return bot, nil
	}
	if req.WaitForReady {
		waitCtx := context.WithoutCancel(ctx)
		if err := s.runCreateLifecycle(waitCtx, bot.ID); err != nil {
			return Bot{}, err
		}
		return s.Get(waitCtx, bot.ID)
	}
	s.enqueueCreateLifecycle(ctx, bot.ID)
	return bot, nil
}

// MarkReady marks a bot lifecycle transition as complete and returns the fresh row.
func (s *Service) MarkReady(ctx context.Context, botID string) (Bot, error) {
	if err := s.updateStatus(ctx, botID, BotStatusReady); err != nil {
		return Bot{}, err
	}
	return s.Get(ctx, botID)
}

// Get returns a bot by its identifier, which may be either a UUID or a name slug.
// This allows name-oriented URLs (/bot/:name) to resolve through the same path
// as UUID-based lookups.
func (s *Service) Get(ctx context.Context, identifier string) (Bot, error) {
	if s.queries == nil {
		return Bot{}, errors.New("bot queries not configured")
	}
	trimmed := strings.TrimSpace(identifier)
	if botUUID, err := db.ParseUUID(trimmed); err == nil {
		return s.getByRow(ctx, func() (any, error) {
			return s.queries.GetBotByID(ctx, botUUID)
		})
	}
	return s.getByRow(ctx, func() (any, error) {
		return s.queries.GetBotByName(ctx, normalizeName(trimmed))
	})
}

// GetForAccess returns a bot row for hot authorization paths without attaching
// runtime check summaries.
func (s *Service) GetForAccess(ctx context.Context, identifier string) (Bot, error) {
	if s.queries == nil {
		return Bot{}, errors.New("bot queries not configured")
	}
	trimmed := strings.TrimSpace(identifier)
	var row any
	var err error
	if botUUID, parseErr := db.ParseUUID(trimmed); parseErr == nil {
		row, err = s.queries.GetBotByID(ctx, botUUID)
	} else {
		row, err = s.queries.GetBotByName(ctx, normalizeName(trimmed))
	}
	if err != nil {
		return Bot{}, err
	}
	return toBot(asSQLCBot(row))
}

func (s *Service) getByRow(ctx context.Context, fetch func() (any, error)) (Bot, error) {
	row, err := fetch()
	if err != nil {
		return Bot{}, err
	}
	bot, err := toBot(asSQLCBot(row))
	if err != nil {
		return Bot{}, err
	}
	if err := s.attachCheckSummary(ctx, &bot, asSQLCBot(row)); err != nil {
		return Bot{}, err
	}
	return bot, nil
}

// CheckNameAvailability validates a candidate name and reports whether it can be
// used for a new bot. excludeBotID, when non-empty, allows the bot currently
// owning the name (e.g. during rename) to be ignored.
func (s *Service) CheckNameAvailability(ctx context.Context, name, excludeBotID string) (NameAvailability, error) {
	if s.queries == nil {
		return NameAvailability{}, errors.New("bot queries not configured")
	}
	normalized := normalizeName(name)
	if reason := validateNameFormat(normalized); reason != "" {
		return NameAvailability{Available: false, Reason: reason}, nil
	}
	taken, err := s.nameTaken(ctx, normalized, excludeBotID)
	if err != nil {
		return NameAvailability{}, err
	}
	if taken {
		return NameAvailability{Available: false, Reason: NameReasonTaken}, nil
	}
	return NameAvailability{Available: true}, nil
}

// nameTaken reports whether the normalized name is already used by a bot other
// than excludeBotID.
func (s *Service) nameTaken(ctx context.Context, normalized, excludeBotID string) (bool, error) {
	existing, err := s.queries.GetBotByName(ctx, normalized)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if excludeBotID != "" && existing.ID.String() == strings.TrimSpace(excludeBotID) {
		return false, nil
	}
	return true, nil
}

// resolveName validates and (when empty) derives a bot name from displayName,
// then ensures it is unique. excludeBotID is ignored during uniqueness checks.
func (s *Service) resolveName(ctx context.Context, rawName, displayName, excludeBotID string) (string, error) {
	normalized := normalizeName(rawName)
	if normalized == "" {
		normalized = slugify(displayName)
	}
	switch validateNameFormat(normalized) {
	case NameReasonInvalid:
		return "", ErrBotNameInvalid
	case NameReasonReserved:
		return "", ErrBotNameReserved
	}
	taken, err := s.nameTaken(ctx, normalized, excludeBotID)
	if err != nil {
		return "", err
	}
	if taken {
		return "", ErrBotNameTaken
	}
	return normalized, nil
}

// ListByOwner returns bots owned by the given user.
func (s *Service) ListByOwner(ctx context.Context, ownerUserID string) ([]Bot, error) {
	if s.queries == nil {
		return nil, errors.New("bot queries not configured")
	}
	ownerUUID, err := db.ParseUUID(ownerUserID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListBotsByOwner(ctx, ownerUUID)
	if err != nil {
		return nil, err
	}
	items := make([]Bot, 0, len(rows))
	for _, row := range rows {
		item, err := toBot(asSQLCBot(row))
		if err != nil {
			return nil, err
		}
		if err := s.attachCheckSummary(ctx, &item, asSQLCBot(row)); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// ListAccessible returns all bots owned by the user.
func (s *Service) ListAccessible(ctx context.Context, channelIdentityID string) ([]Bot, error) {
	if s.queries == nil {
		return nil, errors.New("bot queries not configured")
	}
	userUUID, err := db.ParseUUID(channelIdentityID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListAccessibleBots(ctx, userUUID)
	if err != nil {
		return nil, err
	}
	items := make([]Bot, 0, len(rows))
	for _, row := range rows {
		item, err := toBot(asSQLCBot(row))
		if err != nil {
			return nil, err
		}
		if err := s.attachCheckSummary(ctx, &item, asSQLCBot(row)); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// ValidateUpdate validates bot profile updates without persisting them.
func (s *Service) ValidateUpdate(ctx context.Context, botID string, req UpdateBotRequest) error {
	_, err := s.prepareUpdateParams(ctx, botID, req, true)
	return err
}

// Update updates bot profile fields.
func (s *Service) Update(ctx context.Context, botID string, req UpdateBotRequest) (Bot, error) {
	params, err := s.prepareUpdateParams(ctx, botID, req, true)
	if err != nil {
		return Bot{}, err
	}
	return s.updateWithParams(ctx, params)
}

// UpdateReplacingMetadata updates bot profile fields and writes metadata exactly
// as supplied. It is intended for restore/import paths where scrubbed metadata
// must not preserve existing sensitive fields.
func (s *Service) UpdateReplacingMetadata(ctx context.Context, botID string, req UpdateBotRequest) (Bot, error) {
	params, err := s.prepareUpdateParams(ctx, botID, req, false)
	if err != nil {
		return Bot{}, err
	}
	return s.updateWithParams(ctx, params)
}

// PublishRuntimeConfig invalidates the old runtime epoch before publishing
// bot-scoped workspace files, then holds the reset parent lock for the whole
// external write. Callers must first acquire a bot-scoped reset lease.
func (s *Service) PublishRuntimeConfig(ctx context.Context, botID string, publish func(context.Context) error) error {
	publishErr, guardErr := runtimefence.PublishBotRuntimeConfig(
		ctx,
		s.queries,
		botID,
		botRuntimeConfigPublishTimeout,
		publish,
	)
	return errors.Join(guardErr, publishErr)
}

func (s *Service) updateWithParams(ctx context.Context, params sqlc.UpdateBotProfileParams) (Bot, error) {
	var row sqlc.UpdateBotProfileRow
	err := runtimefence.InResetTransaction(ctx, s.queries, params.ID.String(), "", func(queries dbstore.Queries) error {
		var updateErr error
		row, updateErr = queries.UpdateBotProfile(ctx, params)
		return updateErr
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			return Bot{}, ErrBotNameTaken
		}
		return Bot{}, err
	}
	bot, err := toBot(asSQLCBot(row))
	if err != nil {
		return Bot{}, err
	}
	if err := s.attachCheckSummary(ctx, &bot, asSQLCBot(row)); err != nil {
		return Bot{}, err
	}
	return bot, nil
}

func (s *Service) prepareUpdateParams(ctx context.Context, botID string, req UpdateBotRequest, mergeSensitiveMetadata bool) (sqlc.UpdateBotProfileParams, error) {
	if s.queries == nil {
		return sqlc.UpdateBotProfileParams{}, errors.New("bot queries not configured")
	}
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return sqlc.UpdateBotProfileParams{}, err
	}
	existing, err := s.queries.GetBotByID(ctx, botUUID)
	if err != nil {
		return sqlc.UpdateBotProfileParams{}, err
	}
	displayName := strings.TrimSpace(existing.DisplayName.String)
	avatarURL := strings.TrimSpace(existing.AvatarUrl.String)
	isActive := existing.IsActive
	metadata, err := decodeMetadata(existing.Metadata)
	if err != nil {
		return sqlc.UpdateBotProfileParams{}, err
	}
	if req.DisplayName != nil {
		displayName = strings.TrimSpace(*req.DisplayName)
	}
	if req.AvatarURL != nil {
		avatarURL = strings.TrimSpace(*req.AvatarURL)
	}
	if req.IsActive != nil {
		isActive = *req.IsActive
	}
	timezoneValue := existing.Timezone
	if req.Timezone != nil {
		timezoneValue, err = normalizeOptionalTimezone(req.Timezone)
		if err != nil {
			return sqlc.UpdateBotProfileParams{}, err
		}
	}
	if req.Metadata != nil {
		if mergeSensitiveMetadata {
			metadata = acpprofile.MergeSensitiveFieldsForUpdate(metadata, req.Metadata)
		} else {
			metadata = req.Metadata
		}
	}
	if displayName == "" {
		displayName = "bot-" + uuid.NewString()
	}
	botName := existing.Name
	if req.Name != nil {
		botName, err = s.resolveName(ctx, *req.Name, displayName, existing.ID.String())
		if err != nil {
			return sqlc.UpdateBotProfileParams{}, err
		}
	}
	payload, err := json.Marshal(metadata)
	if err != nil {
		return sqlc.UpdateBotProfileParams{}, err
	}
	return sqlc.UpdateBotProfileParams{
		ID:          botUUID,
		Name:        botName,
		DisplayName: pgtype.Text{String: displayName, Valid: displayName != ""},
		AvatarUrl:   pgtype.Text{String: avatarURL, Valid: avatarURL != ""},
		Timezone:    timezoneValue,
		IsActive:    isActive,
		Metadata:    payload,
	}, nil
}

// TransferOwner transfers bot ownership to another user.
func (s *Service) TransferOwner(ctx context.Context, botID string, ownerUserID string) (Bot, error) {
	if s.queries == nil {
		return Bot{}, errors.New("bot queries not configured")
	}
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return Bot{}, err
	}
	ownerUUID, err := db.ParseUUID(ownerUserID)
	if err != nil {
		return Bot{}, err
	}
	if err := s.ensureUserExists(ctx, ownerUUID); err != nil {
		return Bot{}, err
	}
	row, err := s.queries.UpdateBotOwner(ctx, sqlc.UpdateBotOwnerParams{
		ID:          botUUID,
		OwnerUserID: ownerUUID,
	})
	if err != nil {
		return Bot{}, err
	}
	bot, err := toBot(asSQLCBot(row))
	if err != nil {
		return Bot{}, err
	}
	if err := s.attachCheckSummary(ctx, &bot, asSQLCBot(row)); err != nil {
		return Bot{}, err
	}
	return bot, nil
}

// Delete removes a bot and its associated resources.
func (s *Service) Delete(ctx context.Context, botID string) error {
	if s.queries == nil {
		return errors.New("bot queries not configured")
	}
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	err = runtimefence.InResetTransaction(ctx, s.queries, botID, "", func(queries dbstore.Queries) error {
		row, err := queries.GetBotByID(ctx, botUUID)
		if err != nil {
			return err
		}
		if strings.TrimSpace(row.Status) == BotStatusDeleting {
			return nil
		}
		return queries.UpdateBotStatus(ctx, sqlc.UpdateBotStatusParams{ID: botUUID, Status: BotStatusDeleting})
	})
	if err != nil {
		return err
	}
	s.enqueueDeleteLifecycle(ctx, botUUID.String())
	return nil
}

// ListChecks evaluates runtime resource checks for a bot.
func (s *Service) ListChecks(ctx context.Context, botID string) ([]BotCheck, error) {
	if s.queries == nil {
		return nil, errors.New("bot queries not configured")
	}
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	row, err := s.queries.GetBotByID(ctx, botUUID)
	if err != nil {
		return nil, err
	}
	return s.buildRuntimeChecks(ctx, asSQLCBot(row), true)
}

func (s *Service) enqueueCreateLifecycle(ctx context.Context, botID string) {
	go func() {
		if err := s.runCreateLifecycle(context.WithoutCancel(ctx), botID); err != nil {
			s.logger.Error("bot create lifecycle failed",
				slog.String("bot_id", botID),
				slog.Any("error", err),
			)
		}
	}()
}

func (s *Service) runCreateLifecycle(ctx context.Context, botID string) error {
	lifecycleCtx, cancel := context.WithTimeout(ctx, botLifecycleOperationTimeout)
	defer cancel()

	var setupErr error
	if s.containerLifecycle != nil {
		if err := s.containerLifecycle.SetupBotContainer(lifecycleCtx, botID); err != nil {
			s.logger.Error("bot container setup failed",
				slog.String("bot_id", botID),
				slog.Any("error", err),
			)
			if recordErr := s.RecordContainerSetupFailure(lifecycleCtx, botID, "setup", err); recordErr != nil {
				s.logger.Warn("record bot container setup failure failed",
					slog.String("bot_id", botID),
					slog.Any("error", recordErr),
				)
			}
			if errors.Is(err, workspace.ErrWorkspaceImageIncompatible) ||
				errors.Is(err, workspace.ErrWorkspaceTemplateBootstrapFailed) {
				setupErr = err
			}
		} else if clearErr := s.ClearContainerSetupFailure(lifecycleCtx, botID); clearErr != nil {
			s.logger.Warn("clear bot container setup failure failed",
				slog.String("bot_id", botID),
				slog.Any("error", clearErr),
			)
		}
	}

	if err := s.updateStatus(lifecycleCtx, botID, BotStatusReady); err != nil {
		s.logger.Error("failed to update bot status to ready after create",
			slog.String("bot_id", botID),
			slog.Any("error", err),
		)
		return err
	}
	return setupErr
}

func (s *Service) enqueueDeleteLifecycle(ctx context.Context, botID string) {
	go s.runDeleteLifecycle(context.WithoutCancel(ctx), botID)
}

func (s *Service) runDeleteLifecycle(ctx context.Context, botID string) {
	lifecycleCtx, cancel := context.WithTimeout(ctx, botLifecycleOperationTimeout)
	defer cancel()

	// The revert must succeed even when the failing cleanup consumed the
	// whole lifecycle budget: reverting on the exhausted context would
	// strand the bot in status "deleting" with no retry path.
	revertToReady := func() {
		revertCtx, cancelRevert := context.WithTimeout(context.WithoutCancel(lifecycleCtx), 15*time.Second)
		defer cancelRevert()
		if err := s.updateStatus(revertCtx, botID, BotStatusReady); err != nil {
			s.logger.Error("revert bot status failed", slog.String("bot_id", botID), slog.Any("error", err))
		}
	}

	if s.connectorLifecycle != nil {
		if err := s.connectorLifecycle.CleanupBotConnectors(lifecycleCtx, botID); err != nil {
			s.logger.Error("bot connector cleanup failed",
				slog.String("bot_id", botID),
				slog.Any("error", err),
			)
			revertToReady()
			return
		}
	}

	if s.containerLifecycle != nil {
		if err := s.containerLifecycle.CleanupBotContainer(lifecycleCtx, botID, false); err != nil {
			s.logger.Error("bot container cleanup failed",
				slog.String("bot_id", botID),
				slog.Any("error", err),
			)
		}
	}

	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		s.logger.Error("invalid bot id while finalizing delete",
			slog.String("bot_id", botID),
			slog.Any("error", err),
		)
		revertToReady()
		return
	}
	if err := s.queries.DeleteBotByID(lifecycleCtx, botUUID); err != nil {
		s.logger.Error("failed to delete bot after cleanup",
			slog.String("bot_id", botID),
			slog.Any("error", err),
		)
		revertToReady()
	}
}

func (s *Service) updateStatus(ctx context.Context, botID, status string) error {
	if s.queries == nil {
		return errors.New("bot queries not configured")
	}
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	return s.queries.UpdateBotStatus(ctx, sqlc.UpdateBotStatusParams{
		ID:     botUUID,
		Status: strings.TrimSpace(status),
	})
}

func (s *Service) ensureUserExists(ctx context.Context, userID pgtype.UUID) error {
	if s.queries == nil {
		return errors.New("bot queries not configured")
	}
	_, err := s.queries.GetUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, db.ErrNotFound) {
			return ErrOwnerUserNotFound
		}
		return err
	}
	return nil
}

func asSQLCBot(v any) sqlc.Bot {
	switch r := v.(type) {
	case sqlc.Bot:
		return r
	case sqlc.CreateBotRow:
		return sqlc.Bot{ID: r.ID, OwnerUserID: r.OwnerUserID, Name: r.Name, DisplayName: r.DisplayName, AvatarUrl: r.AvatarUrl, Timezone: r.Timezone, IsActive: r.IsActive, Status: r.Status, Language: r.Language, ReasoningEffort: r.ReasoningEffort, ChatModelID: r.ChatModelID, SearchProviderID: r.SearchProviderID, MemoryProviderID: r.MemoryProviderID, Metadata: r.Metadata, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
	case sqlc.GetBotByIDRow:
		return sqlc.Bot{ID: r.ID, OwnerUserID: r.OwnerUserID, Name: r.Name, DisplayName: r.DisplayName, AvatarUrl: r.AvatarUrl, Timezone: r.Timezone, IsActive: r.IsActive, Status: r.Status, Language: r.Language, ReasoningEffort: r.ReasoningEffort, ChatModelID: r.ChatModelID, SearchProviderID: r.SearchProviderID, MemoryProviderID: r.MemoryProviderID, CompactionEnabled: r.CompactionEnabled, CompactionThreshold: r.CompactionThreshold, CompactionModelID: r.CompactionModelID, Metadata: r.Metadata, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
	case sqlc.GetBotByNameRow:
		return sqlc.Bot{ID: r.ID, OwnerUserID: r.OwnerUserID, Name: r.Name, DisplayName: r.DisplayName, AvatarUrl: r.AvatarUrl, Timezone: r.Timezone, IsActive: r.IsActive, Status: r.Status, Language: r.Language, ReasoningEffort: r.ReasoningEffort, ChatModelID: r.ChatModelID, SearchProviderID: r.SearchProviderID, MemoryProviderID: r.MemoryProviderID, CompactionEnabled: r.CompactionEnabled, CompactionThreshold: r.CompactionThreshold, CompactionModelID: r.CompactionModelID, Metadata: r.Metadata, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
	case sqlc.ListBotsByOwnerRow:
		return sqlc.Bot{ID: r.ID, OwnerUserID: r.OwnerUserID, Name: r.Name, DisplayName: r.DisplayName, AvatarUrl: r.AvatarUrl, Timezone: r.Timezone, IsActive: r.IsActive, Status: r.Status, Language: r.Language, ReasoningEffort: r.ReasoningEffort, ChatModelID: r.ChatModelID, SearchProviderID: r.SearchProviderID, MemoryProviderID: r.MemoryProviderID, Metadata: r.Metadata, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
	case sqlc.ListAccessibleBotsRow:
		return sqlc.Bot{ID: r.ID, OwnerUserID: r.OwnerUserID, Name: r.Name, DisplayName: r.DisplayName, AvatarUrl: r.AvatarUrl, Timezone: r.Timezone, IsActive: r.IsActive, Status: r.Status, Language: r.Language, ReasoningEffort: r.ReasoningEffort, ChatModelID: r.ChatModelID, SearchProviderID: r.SearchProviderID, MemoryProviderID: r.MemoryProviderID, Metadata: r.Metadata, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
	case sqlc.UpdateBotProfileRow:
		return sqlc.Bot{ID: r.ID, OwnerUserID: r.OwnerUserID, Name: r.Name, DisplayName: r.DisplayName, AvatarUrl: r.AvatarUrl, Timezone: r.Timezone, IsActive: r.IsActive, Status: r.Status, Language: r.Language, ReasoningEffort: r.ReasoningEffort, ChatModelID: r.ChatModelID, SearchProviderID: r.SearchProviderID, MemoryProviderID: r.MemoryProviderID, Metadata: r.Metadata, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
	case sqlc.UpdateBotOwnerRow:
		return sqlc.Bot{ID: r.ID, OwnerUserID: r.OwnerUserID, Name: r.Name, DisplayName: r.DisplayName, AvatarUrl: r.AvatarUrl, Timezone: r.Timezone, IsActive: r.IsActive, Status: r.Status, Language: r.Language, ReasoningEffort: r.ReasoningEffort, ChatModelID: r.ChatModelID, SearchProviderID: r.SearchProviderID, MemoryProviderID: r.MemoryProviderID, Metadata: r.Metadata, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
	default:
		return sqlc.Bot{}
	}
}

func toBot(row sqlc.Bot) (Bot, error) {
	displayName := ""
	if row.DisplayName.Valid {
		displayName = row.DisplayName.String
	}
	avatarURL := ""
	if row.AvatarUrl.Valid {
		avatarURL = row.AvatarUrl.String
	}
	timezoneName := ""
	if row.Timezone.Valid {
		timezoneName = row.Timezone.String
	}
	metadata, err := decodeMetadata(row.Metadata)
	if err != nil {
		return Bot{}, err
	}
	createdAt := time.Time{}
	if row.CreatedAt.Valid {
		createdAt = row.CreatedAt.Time
	}
	updatedAt := time.Time{}
	if row.UpdatedAt.Valid {
		updatedAt = row.UpdatedAt.Time
	}
	return Bot{
		ID:              row.ID.String(),
		OwnerUserID:     row.OwnerUserID.String(),
		Name:            row.Name,
		DisplayName:     displayName,
		AvatarURL:       avatarURL,
		Timezone:        timezoneName,
		IsActive:        row.IsActive,
		Status:          strings.TrimSpace(row.Status),
		CheckState:      BotCheckStateUnknown,
		CheckIssueCount: 0,
		Metadata:        metadata,
		CreatedAt:       createdAt,
		UpdatedAt:       updatedAt,
	}, nil
}

func decodeMetadata(payload []byte) (map[string]any, error) {
	if len(payload) == 0 {
		return map[string]any{}, nil
	}
	var data map[string]any
	if err := json.Unmarshal(payload, &data); err != nil {
		return nil, err
	}
	if data == nil {
		data = map[string]any{}
	}
	return data, nil
}

func normalizeOptionalTimezone(raw *string) (pgtype.Text, error) {
	if raw == nil {
		return pgtype.Text{}, nil
	}
	normalized := strings.TrimSpace(*raw)
	if normalized == "" {
		return pgtype.Text{}, nil
	}
	loc, _, err := tzutil.Resolve(normalized)
	if err != nil {
		return pgtype.Text{}, fmt.Errorf("invalid timezone: %w", err)
	}
	return pgtype.Text{String: loc.String(), Valid: true}, nil
}

func (s *Service) attachCheckSummary(ctx context.Context, bot *Bot, row sqlc.Bot) error {
	checks, err := s.buildRuntimeChecks(ctx, row, false)
	if err != nil {
		return err
	}
	checkState, issueCount := summarizeChecks(checks)
	bot.CheckState = checkState
	bot.CheckIssueCount = issueCount
	return nil
}

// buildRuntimeChecks composes builtin checks and optional dynamic checker results.
// includeDynamic is disabled when computing list summary to avoid expensive runtime probes.
func (s *Service) buildRuntimeChecks(ctx context.Context, row sqlc.Bot, includeDynamic bool) ([]BotCheck, error) {
	status := strings.TrimSpace(row.Status)
	checks := make([]BotCheck, 0, 4)

	if status == BotStatusCreating {
		checks = append(checks, BotCheck{
			ID:       BotCheckTypeContainerInit,
			Type:     BotCheckTypeContainerInit,
			TitleKey: "bots.checks.titles.containerInit",
			Status:   BotCheckStatusUnknown,
			Summary:  "Initialization is in progress.",
			Detail:   "Bot resources are still being provisioned.",
		})
		checks = append(checks, BotCheck{
			ID:       BotCheckTypeContainerRecord,
			Type:     BotCheckTypeContainerRecord,
			TitleKey: "bots.checks.titles.containerRecord",
			Status:   BotCheckStatusUnknown,
			Summary:  "Workspace runtime record is pending.",
			Detail:   "The workspace runtime record will be checked after initialization.",
		})
		checks = append(checks, BotCheck{
			ID:       BotCheckTypeContainerTask,
			Type:     BotCheckTypeContainerTask,
			TitleKey: "bots.checks.titles.containerTask",
			Status:   BotCheckStatusUnknown,
			Summary:  "Workspace runtime state is pending.",
			Detail:   "Task state will be checked after initialization.",
		})
		checks = append(checks, BotCheck{
			ID:       BotCheckTypeContainerData,
			Type:     BotCheckTypeContainerData,
			TitleKey: "bots.checks.titles.containerDataPath",
			Status:   BotCheckStatusUnknown,
			Summary:  "Workspace reachability check is pending.",
			Detail:   "Reachability will be checked after initialization.",
		})
		if includeDynamic {
			checks = s.appendDynamicChecks(ctx, row.ID.String(), checks)
		}
		return checks, nil
	}
	if status == BotStatusDeleting {
		checks = append(checks, BotCheck{
			ID:       BotCheckTypeDelete,
			Type:     BotCheckTypeDelete,
			TitleKey: "bots.checks.titles.botDelete",
			Status:   BotCheckStatusUnknown,
			Summary:  "Deletion is in progress.",
			Detail:   "Bot resources are being cleaned up.",
		})
		checks = append(checks, BotCheck{
			ID:       BotCheckTypeContainerRecord,
			Type:     BotCheckTypeContainerRecord,
			TitleKey: "bots.checks.titles.containerRecord",
			Status:   BotCheckStatusUnknown,
			Summary:  "Workspace runtime record check is skipped.",
			Detail:   "The bot is being deleted, so workspace checks are paused.",
		})
		checks = append(checks, BotCheck{
			ID:       BotCheckTypeContainerTask,
			Type:     BotCheckTypeContainerTask,
			TitleKey: "bots.checks.titles.containerTask",
			Status:   BotCheckStatusUnknown,
			Summary:  "Workspace runtime check is skipped.",
			Detail:   "Bot is deleting and task checks are paused.",
		})
		checks = append(checks, BotCheck{
			ID:       BotCheckTypeContainerData,
			Type:     BotCheckTypeContainerData,
			TitleKey: "bots.checks.titles.containerDataPath",
			Status:   BotCheckStatusUnknown,
			Summary:  "Workspace reachability check is skipped.",
			Detail:   "Bot is deleting and reachability checks are paused.",
		})
		if includeDynamic {
			checks = s.appendDynamicChecks(ctx, row.ID.String(), checks)
		}
		return checks, nil
	}

	setupFailure, hasSetupFailure, err := lastContainerSetupFailure(row.Metadata)
	if err != nil {
		return nil, err
	}
	initCheck := BotCheck{
		ID:       BotCheckTypeContainerInit,
		Type:     BotCheckTypeContainerInit,
		TitleKey: "bots.checks.titles.containerInit",
		Status:   BotCheckStatusOK,
		Summary:  "Initialization finished.",
	}
	if hasSetupFailure {
		initCheck.Status = BotCheckStatusError
		initCheck.Summary = "Workspace initialization failed."
		initCheck.Detail = setupFailure.Message
		initCheck.Metadata = setupFailure.metadata()
	}
	checks = append(checks, initCheck)

	containerRow, err := s.queries.GetContainerByBotID(ctx, row.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			recordCheck := BotCheck{
				ID:       BotCheckTypeContainerRecord,
				Type:     BotCheckTypeContainerRecord,
				TitleKey: "bots.checks.titles.containerRecord",
				Status:   BotCheckStatusError,
				Summary:  "Workspace runtime record is missing.",
				Detail:   "No workspace runtime is attached to this bot.",
			}
			if hasSetupFailure {
				recordCheck.Status = BotCheckStatusUnknown
				recordCheck.Summary = "Workspace runtime record was not created."
				recordCheck.Detail = "The workspace runtime record cannot be checked until initialization succeeds."
				recordCheck.Metadata = setupFailure.metadata()
			}
			checks = append(checks, recordCheck)
			checks = append(checks, BotCheck{
				ID:       BotCheckTypeContainerTask,
				Type:     BotCheckTypeContainerTask,
				TitleKey: "bots.checks.titles.containerTask",
				Status:   BotCheckStatusUnknown,
				Summary:  "Workspace runtime state is unknown.",
				Detail:   "The runtime state cannot be determined without a workspace runtime record.",
			})
			checks = append(checks, BotCheck{
				ID:       BotCheckTypeContainerData,
				Type:     BotCheckTypeContainerData,
				TitleKey: "bots.checks.titles.containerDataPath",
				Status:   BotCheckStatusUnknown,
				Summary:  "Workspace reachability is unknown.",
				Detail:   "Reachability cannot be determined without a workspace runtime record.",
			})
			if includeDynamic {
				checks = s.appendDynamicChecks(ctx, row.ID.String(), checks)
			}
			return checks, nil
		}
		return nil, err
	}

	checks = append(checks, BotCheck{
		ID:       BotCheckTypeContainerRecord,
		Type:     BotCheckTypeContainerRecord,
		TitleKey: "bots.checks.titles.containerRecord",
		Status:   BotCheckStatusOK,
		Summary:  "Workspace runtime record exists.",
		Detail:   fmt.Sprintf("runtime_id=%s", strings.TrimSpace(containerRow.ContainerID)),
		Metadata: map[string]any{
			"container_id": strings.TrimSpace(containerRow.ContainerID),
			"namespace":    strings.TrimSpace(containerRow.Namespace),
			"image":        strings.TrimSpace(containerRow.Image),
		},
	})

	taskStatus := strings.TrimSpace(strings.ToLower(containerRow.Status))
	taskCheck := BotCheck{
		ID:       BotCheckTypeContainerTask,
		Type:     BotCheckTypeContainerTask,
		TitleKey: "bots.checks.titles.containerTask",
		Status:   BotCheckStatusWarn,
		Summary:  "Workspace runtime state needs attention.",
	}
	switch taskStatus {
	case "running", "created", "stopped", "paused":
		taskCheck.Status = BotCheckStatusOK
		taskCheck.Summary = "Workspace runtime state is reported."
		taskCheck.Detail = fmt.Sprintf("status=%s", taskStatus)
	case "":
		taskCheck.Detail = "status is empty"
	default:
		taskCheck.Detail = fmt.Sprintf("unexpected status=%s", taskStatus)
	}
	taskCheck.Metadata = map[string]any{"status": taskStatus}
	checks = append(checks, taskCheck)

	dataCheck := BotCheck{
		ID:       BotCheckTypeContainerData,
		Type:     BotCheckTypeContainerData,
		TitleKey: "bots.checks.titles.containerDataPath",
		Status:   BotCheckStatusWarn,
		Summary:  "Workspace reachability needs attention.",
	}
	if s.containerReachability == nil {
		dataCheck.Status = BotCheckStatusUnknown
		dataCheck.Summary = "Workspace reachability check is not configured."
	} else if err := s.containerReachability(ctx, row.ID.String()); err != nil {
		s.logger.Warn("workspace reachability check failed",
			slog.String("bot_id", row.ID.String()), slog.Any("error", err))
		dataCheck.Status = BotCheckStatusError
		dataCheck.Summary = "Workspace is not reachable via gRPC."
		dataCheck.Detail = sanitizeDiagnosticMessage(err.Error(), "workspace reachability check failed")
	} else {
		dataCheck.Status = BotCheckStatusOK
		dataCheck.Summary = "Workspace is reachable via gRPC."
	}
	checks = append(checks, dataCheck)
	if includeDynamic {
		checks = s.appendDynamicChecks(ctx, row.ID.String(), checks)
	}

	return checks, nil
}

// appendDynamicChecks appends checks from registered runtime checkers.
func (s *Service) appendDynamicChecks(ctx context.Context, botID string, checks []BotCheck) []BotCheck {
	for _, checker := range s.checkers {
		items := checker.ListChecks(ctx, botID)
		for _, item := range items {
			item.ID = strings.TrimSpace(item.ID)
			item.Type = strings.TrimSpace(item.Type)
			item.Status = strings.TrimSpace(item.Status)
			if item.ID == "" {
				if item.Type != "" {
					item.ID = item.Type
				} else {
					item.ID = "runtime.unknown"
					if s.logger != nil {
						s.logger.Warn("runtime checker returned check without id and type",
							slog.String("bot_id", botID))
					}
				}
			}
			if item.Type == "" {
				item.Type = item.ID
			}
			if item.Status == "" {
				item.Status = BotCheckStatusUnknown
			}
			checks = append(checks, item)
		}
	}
	return checks
}

func summarizeChecks(checks []BotCheck) (string, int32) {
	if len(checks) == 0 {
		return BotCheckStateUnknown, 0
	}
	var issueCount int32
	unknownCount := 0
	for _, check := range checks {
		switch check.Status {
		case BotCheckStatusWarn, BotCheckStatusError:
			issueCount++
		case BotCheckStatusUnknown:
			unknownCount++
		}
	}
	if issueCount > 0 {
		return BotCheckStateIssue, issueCount
	}
	if unknownCount == len(checks) {
		return BotCheckStateUnknown, 0
	}
	return BotCheckStateOK, 0
}
