package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	acpprofile "github.com/memohai/memoh/domains/agent/acp/profile"
	"github.com/memohai/memoh/domains/api/access/acl"
	runtimedomain "github.com/memohai/memoh/domains/runtime"
	"github.com/memohai/memoh/domains/runtime/workspace"
	tzutil "github.com/memohai/memoh/internal/timezone"
)

// Service provides bot CRUD and membership management.
type Service struct {
	bots                  BotStore
	grants                GrantStore
	users                 UserReader
	containers            ContainerReader
	logger                *slog.Logger
	aclPresets            ACLPresetApplier
	containerLifecycle    ContainerLifecycle
	checkers              []RuntimeChecker
	containerReachability func(ctx context.Context, botID string) error
	purgers               []BotDataPurger
	tombstones            TombstoneReader
}

// ACLPresetApplier initializes ACL state after a bot row is created.
type ACLPresetApplier interface {
	ApplyPreset(context.Context, string, string, string) error
}

const (
	botLifecycleOperationTimeout = 5 * time.Minute
)

var (
	ErrBotNotFound       = errors.New("bot not found")
	ErrBotAccessDenied   = errors.New("bot access denied")
	ErrOwnerUserNotFound = errors.New("owner user not found")
	ErrBotNameTaken      = errors.New("bot name already taken")
	ErrBotNameInvalid    = errors.New("bot name is invalid")
	ErrBotNameReserved   = errors.New("bot name is reserved")
	ErrContainerNotFound = errors.New("bot container not found")
)

// NewService creates a new bot service.
func NewService(log *slog.Logger, botStore BotStore, grantStore GrantStore, users UserReader, containers ContainerReader, presetAppliers ...ACLPresetApplier) *Service {
	if log == nil {
		log = slog.Default()
	}
	service := &Service{
		bots:       botStore,
		grants:     grantStore,
		users:      users,
		containers: containers,
		logger:     log.With(slog.String("service", "bots")),
	}
	if len(presetAppliers) > 0 {
		service.aclPresets = presetAppliers[0]
	}
	return service
}

// SetContainerLifecycle registers a container lifecycle handler for bot operations.
func (s *Service) SetContainerLifecycle(lc ContainerLifecycle) {
	s.containerLifecycle = lc
}

// SetContainerReachability registers a function that checks whether a bot's
// container is reachable via gRPC. Returns nil on success, error otherwise.
func (s *Service) SetContainerReachability(fn func(ctx context.Context, botID string) error) {
	s.containerReachability = fn
}

// AddBotDataPurger registers an owner that holds bot-scoped rows. Purgers run
// in registration order before the bot row itself is removed.
func (s *Service) AddBotDataPurger(p BotDataPurger) {
	if p != nil {
		s.purgers = append(s.purgers, p)
	}
}

// SetTombstoneReader enables resuming deletes that were interrupted mid-saga.
func (s *Service) SetTombstoneReader(r TombstoneReader) {
	s.tombstones = r
}

// AddRuntimeChecker registers an additional runtime checker.
func (s *Service) AddRuntimeChecker(c RuntimeChecker) {
	if c != nil {
		s.checkers = append(s.checkers, c)
	}
}

// AuthorizeAccess checks whether userID may access the given bot (owner or admin only).
func (s *Service) AuthorizeAccess(ctx context.Context, userID, botID string, isAdmin bool) (Bot, error) {
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
	if isAdmin || bot.OwnerUserID == userID {
		return bot, nil
	}
	return Bot{}, ErrBotAccessDenied
}

// Create creates a new bot owned by owner user.
func (s *Service) Create(ctx context.Context, ownerUserID string, req CreateBotRequest) (Bot, error) {
	if s.bots == nil {
		return Bot{}, errors.New("bot queries not configured")
	}
	ownerID := strings.TrimSpace(ownerUserID)
	if ownerID == "" {
		return Bot{}, errors.New("owner user id is required")
	}
	ownerID, err := parseUUID(ownerID)
	if err != nil {
		return Bot{}, err
	}
	if err := s.ensureUserExists(ctx, ownerID); err != nil {
		return Bot{}, err
	}
	aclPresetKey := acl.NormalizePresetKey(req.AclPreset)
	if _, err := acl.ResolvePreset(aclPresetKey); err != nil {
		return Bot{}, err
	}
	if s.aclPresets == nil {
		return Bot{}, errors.New("acl preset applier not configured")
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
	row, err := s.bots.CreateBot(ctx, CreateInput{
		OwnerUserID: ownerID,
		Name:        botName,
		DisplayName: displayName,
		AvatarURL:   avatarURL,
		Timezone:    timezoneValue,
		IsActive:    isActive,
		Metadata:    payload,
		Status:      BotStatusCreating,
	})
	if err != nil {
		return Bot{}, err
	}
	bot, err := toBot(row)
	if err != nil {
		return Bot{}, err
	}
	if err := s.aclPresets.ApplyPreset(ctx, bot.ID, ownerID, aclPresetKey); err != nil {
		if cleanupErr := s.bots.DeleteBot(ctx, row.ID); cleanupErr != nil {
			return Bot{}, errors.Join(
				fmt.Errorf("apply acl preset: %w", err),
				fmt.Errorf("cleanup bot after acl preset failure: %w", cleanupErr),
			)
		}
		return Bot{}, fmt.Errorf("apply acl preset: %w", err)
	}
	if err := s.attachCheckSummary(ctx, &bot, row); err != nil {
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
	if s.bots == nil {
		return Bot{}, errors.New("bot queries not configured")
	}
	trimmed := strings.TrimSpace(identifier)
	if botID, err := parseUUID(trimmed); err == nil {
		return s.getByRow(ctx, func() (Record, error) {
			return s.bots.GetBotByID(ctx, botID)
		})
	}
	return s.getByRow(ctx, func() (Record, error) {
		return s.bots.GetBotByName(ctx, normalizeName(trimmed))
	})
}

// GetForAccess returns a bot row for hot authorization paths without attaching
// runtime check summaries.
func (s *Service) GetForAccess(ctx context.Context, identifier string) (Bot, error) {
	if s.bots == nil {
		return Bot{}, errors.New("bot queries not configured")
	}
	trimmed := strings.TrimSpace(identifier)
	var row Record
	var err error
	if botID, parseErr := parseUUID(trimmed); parseErr == nil {
		row, err = s.bots.GetBotByID(ctx, botID)
	} else {
		row, err = s.bots.GetBotByName(ctx, normalizeName(trimmed))
	}
	if err != nil {
		return Bot{}, err
	}
	return toBot(row)
}

func (s *Service) getByRow(ctx context.Context, fetch func() (Record, error)) (Bot, error) {
	row, err := fetch()
	if err != nil {
		return Bot{}, err
	}
	bot, err := toBot(row)
	if err != nil {
		return Bot{}, err
	}
	if err := s.attachCheckSummary(ctx, &bot, row); err != nil {
		return Bot{}, err
	}
	return bot, nil
}

// CheckNameAvailability validates a candidate name and reports whether it can be
// used for a new bot. excludeBotID, when non-empty, allows the bot currently
// owning the name (e.g. during rename) to be ignored.
func (s *Service) CheckNameAvailability(ctx context.Context, name, excludeBotID string) (NameAvailability, error) {
	if s.bots == nil {
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
	existing, err := s.bots.GetBotByName(ctx, normalized)
	if err != nil {
		if errors.Is(err, ErrBotNotFound) {
			return false, nil
		}
		return false, err
	}
	if excludeBotID != "" && existing.ID == strings.TrimSpace(excludeBotID) {
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
	if s.bots == nil {
		return nil, errors.New("bot queries not configured")
	}
	ownerUserID, err := parseUUID(ownerUserID)
	if err != nil {
		return nil, err
	}
	rows, err := s.bots.ListBotsByOwner(ctx, ownerUserID)
	if err != nil {
		return nil, err
	}
	items := make([]Bot, 0, len(rows))
	for _, row := range rows {
		item, err := toBot(row)
		if err != nil {
			return nil, err
		}
		if err := s.attachCheckSummary(ctx, &item, row); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// ListAccessible returns all bots owned by the user.
func (s *Service) ListAccessible(ctx context.Context, channelIdentityID string) ([]Bot, error) {
	if s.bots == nil {
		return nil, errors.New("bot queries not configured")
	}
	channelIdentityID, err := parseUUID(channelIdentityID)
	if err != nil {
		return nil, err
	}
	rows, err := s.bots.ListAccessibleBots(ctx, channelIdentityID)
	if err != nil {
		return nil, err
	}
	items := make([]Bot, 0, len(rows))
	for _, row := range rows {
		item, err := toBot(row)
		if err != nil {
			return nil, err
		}
		if err := s.attachCheckSummary(ctx, &item, row); err != nil {
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

func (s *Service) updateWithParams(ctx context.Context, params UpdateInput) (Bot, error) {
	row, err := s.bots.UpdateBot(ctx, params)
	if err != nil {
		return Bot{}, err
	}
	bot, err := toBot(row)
	if err != nil {
		return Bot{}, err
	}
	if err := s.attachCheckSummary(ctx, &bot, row); err != nil {
		return Bot{}, err
	}
	return bot, nil
}

func (s *Service) prepareUpdateParams(ctx context.Context, botID string, req UpdateBotRequest, mergeSensitiveMetadata bool) (UpdateInput, error) {
	if s.bots == nil {
		return UpdateInput{}, errors.New("bot queries not configured")
	}
	botID, err := parseUUID(botID)
	if err != nil {
		return UpdateInput{}, err
	}
	existing, err := s.bots.GetBotByID(ctx, botID)
	if err != nil {
		return UpdateInput{}, err
	}
	displayName := strings.TrimSpace(existing.DisplayName)
	avatarURL := strings.TrimSpace(existing.AvatarURL)
	isActive := existing.IsActive
	metadata, err := decodeMetadata(existing.Metadata)
	if err != nil {
		return UpdateInput{}, err
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
			return UpdateInput{}, err
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
		botName, err = s.resolveName(ctx, *req.Name, displayName, existing.ID)
		if err != nil {
			return UpdateInput{}, err
		}
	}
	payload, err := json.Marshal(metadata)
	if err != nil {
		return UpdateInput{}, err
	}
	return UpdateInput{
		ID:          botID,
		Name:        botName,
		DisplayName: displayName,
		AvatarURL:   avatarURL,
		Timezone:    timezoneValue,
		IsActive:    isActive,
		Metadata:    payload,
	}, nil
}

// TransferOwner transfers bot ownership to another user.
func (s *Service) TransferOwner(ctx context.Context, botID string, ownerUserID string) (Bot, error) {
	if s.bots == nil {
		return Bot{}, errors.New("bot queries not configured")
	}
	botID, err := parseUUID(botID)
	if err != nil {
		return Bot{}, err
	}
	ownerUserID, err = parseUUID(ownerUserID)
	if err != nil {
		return Bot{}, err
	}
	if err := s.ensureUserExists(ctx, ownerUserID); err != nil {
		return Bot{}, err
	}
	row, err := s.bots.UpdateBotOwner(ctx, botID, ownerUserID)
	if err != nil {
		return Bot{}, err
	}
	bot, err := toBot(row)
	if err != nil {
		return Bot{}, err
	}
	if err := s.attachCheckSummary(ctx, &bot, row); err != nil {
		return Bot{}, err
	}
	return bot, nil
}

// Delete removes a bot and its associated resources.
func (s *Service) Delete(ctx context.Context, botID string) error {
	if s.bots == nil {
		return errors.New("bot queries not configured")
	}
	botID, err := parseUUID(botID)
	if err != nil {
		return err
	}
	row, err := s.bots.GetBotByID(ctx, botID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(row.Status) == BotStatusDeleting {
		return nil
	}
	if err := s.bots.UpdateBotStatus(ctx, botID, BotStatusDeleting); err != nil {
		return err
	}
	s.enqueueDeleteLifecycle(ctx, botID)
	return nil
}

// ListChecks evaluates runtime resource checks for a bot.
func (s *Service) ListChecks(ctx context.Context, botID string) ([]BotCheck, error) {
	if s.bots == nil {
		return nil, errors.New("bot queries not configured")
	}
	botID, err := parseUUID(botID)
	if err != nil {
		return nil, err
	}
	row, err := s.bots.GetBotByID(ctx, botID)
	if err != nil {
		return nil, err
	}
	return s.buildRuntimeChecks(ctx, row, true)
}

func (s *Service) enqueueCreateLifecycle(ctx context.Context, botID string) {
	go func() {
		if err := s.runCreateLifecycle(context.WithoutCancel(ctx), botID); err != nil {
			s.logger.ErrorContext(ctx, "bot create lifecycle failed",
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
			s.logger.ErrorContext(lifecycleCtx, "bot container setup failed",
				slog.String("bot_id", botID),
				slog.Any("error", err),
			)
			if recordErr := s.RecordContainerSetupFailure(lifecycleCtx, botID, "setup", err); recordErr != nil {
				s.logger.WarnContext(lifecycleCtx, "record bot container setup failure failed",
					slog.String("bot_id", botID),
					slog.Any("error", recordErr),
				)
			}
			if errors.Is(err, runtimedomain.ErrWorkspaceImageIncompatible) ||
				errors.Is(err, workspace.ErrWorkspaceTemplateBootstrapFailed) {
				setupErr = err
			}
		} else if clearErr := s.ClearContainerSetupFailure(lifecycleCtx, botID); clearErr != nil {
			s.logger.WarnContext(lifecycleCtx, "clear bot container setup failure failed",
				slog.String("bot_id", botID),
				slog.Any("error", clearErr),
			)
		}
	}

	if err := s.updateStatus(lifecycleCtx, botID, BotStatusReady); err != nil {
		s.logger.ErrorContext(lifecycleCtx, "failed to update bot status to ready after create",
			slog.String("bot_id", botID),
			slog.Any("error", err),
		)
		return err
	}
	return setupErr
}

func (s *Service) enqueueDeleteLifecycle(ctx context.Context, botID string) {
	go func() {
		lifecycleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), botLifecycleOperationTimeout)
		defer cancel()

		if err := s.runDeleteLifecycle(lifecycleCtx, botID); err != nil {
			s.logger.ErrorContext(ctx, "bot delete lifecycle failed",
				slog.String("bot_id", botID),
				slog.Any("error", err),
			)
		}
	}()
}

// runDeleteLifecycle tears a bot down in dependency order: workspace container,
// then every owner holding bot-scoped rows, then the bot row itself.
//
// Epoch v2 dropped the cross-schema cascades that used to erase the dependent
// rows, so the bot row must outlive them: it stays in the deleting state until
// the last purge succeeds, which keeps it invisible to callers while leaving a
// tombstone for ResumeDeletes to retry.
func (s *Service) runDeleteLifecycle(ctx context.Context, botID string) error {
	if s.containerLifecycle != nil {
		// The container is an external resource rather than a row, and a
		// backend outage must not strand the bot in the deleting state.
		if err := s.containerLifecycle.CleanupBotContainer(ctx, botID, false); err != nil {
			s.logger.ErrorContext(ctx, "bot container cleanup failed",
				slog.String("bot_id", botID),
				slog.Any("error", err),
			)
		}
	}

	for _, purger := range s.purgers {
		if err := purger.PurgeBotData(ctx, botID); err != nil {
			return fmt.Errorf("purge %s data: %w", purger.Owner(), err)
		}
	}

	if err := s.bots.DeleteBot(ctx, botID); err != nil {
		return fmt.Errorf("delete bot row: %w", err)
	}
	return nil
}

// ResumeDeletes finishes deletes that were interrupted before every owner had
// purged its bot-scoped rows. It is safe to call repeatedly because each purge
// is idempotent.
func (s *Service) ResumeDeletes(ctx context.Context) error {
	if s.tombstones == nil {
		return nil
	}
	pending, err := s.tombstones.ListDeletingBots(ctx)
	if err != nil {
		return fmt.Errorf("list deleting bots: %w", err)
	}
	var failed int
	for _, row := range pending {
		if err := s.runDeleteLifecycle(ctx, row.ID); err != nil {
			failed++
			s.logger.ErrorContext(ctx, "resume bot delete failed",
				slog.String("bot_id", row.ID),
				slog.Any("error", err),
			)
			continue
		}
		s.logger.InfoContext(ctx, "resumed interrupted bot delete", slog.String("bot_id", row.ID))
	}
	if failed > 0 {
		return fmt.Errorf("resume %d of %d interrupted bot deletes failed", failed, len(pending))
	}
	return nil
}

func (s *Service) updateStatus(ctx context.Context, botID, status string) error {
	if s.bots == nil {
		return errors.New("bot queries not configured")
	}
	botID, err := parseUUID(botID)
	if err != nil {
		return err
	}
	return s.bots.UpdateBotStatus(ctx, botID, strings.TrimSpace(status))
}

func (s *Service) ensureUserExists(ctx context.Context, userID string) error {
	if s.users == nil {
		return errors.New("bot queries not configured")
	}
	_, err := s.users.GetUser(ctx, userID)
	return err
}

func toBot(row Record) (Bot, error) {
	metadata, err := decodeMetadata(row.Metadata)
	if err != nil {
		return Bot{}, err
	}
	return Bot{
		ID:              row.ID,
		OwnerUserID:     row.OwnerUserID,
		Name:            row.Name,
		DisplayName:     row.DisplayName,
		AvatarURL:       row.AvatarURL,
		Timezone:        row.Timezone,
		IsActive:        row.IsActive,
		Status:          strings.TrimSpace(row.Status),
		CheckState:      BotCheckStateUnknown,
		CheckIssueCount: 0,
		Metadata:        metadata,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
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

func normalizeOptionalTimezone(raw *string) (string, error) {
	if raw == nil {
		return "", nil
	}
	normalized := strings.TrimSpace(*raw)
	if normalized == "" {
		return "", nil
	}
	loc, _, err := tzutil.Resolve(normalized)
	if err != nil {
		return "", fmt.Errorf("invalid timezone: %w", err)
	}
	return loc.String(), nil
}

func (s *Service) attachCheckSummary(ctx context.Context, bot *Bot, row Record) error {
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
func (s *Service) buildRuntimeChecks(ctx context.Context, row Record, includeDynamic bool) ([]BotCheck, error) {
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
			checks = s.appendDynamicChecks(ctx, row.ID, checks)
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
			checks = s.appendDynamicChecks(ctx, row.ID, checks)
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

	if s.containers == nil {
		return nil, errors.New("bot container reader not configured")
	}
	containerRow, err := s.containers.GetContainerByBotID(ctx, row.ID)
	if err != nil {
		if errors.Is(err, ErrContainerNotFound) {
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
				checks = s.appendDynamicChecks(ctx, row.ID, checks)
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
	} else if err := s.containerReachability(ctx, row.ID); err != nil {
		s.logger.WarnContext(ctx, "workspace reachability check failed",
			slog.String("bot_id", row.ID), slog.Any("error", err))
		dataCheck.Status = BotCheckStatusError
		dataCheck.Summary = "Workspace is not reachable via gRPC."
		dataCheck.Detail = sanitizeDiagnosticMessage(err.Error(), "workspace reachability check failed")
	} else {
		dataCheck.Status = BotCheckStatusOK
		dataCheck.Summary = "Workspace is reachable via gRPC."
	}
	checks = append(checks, dataCheck)
	if includeDynamic {
		checks = s.appendDynamicChecks(ctx, row.ID, checks)
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
						s.logger.WarnContext(ctx, "runtime checker returned check without id and type",
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

func parseUUID(value string) (string, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return "", fmt.Errorf("invalid UUID: %w", err)
	}
	return parsed.String(), nil
}
