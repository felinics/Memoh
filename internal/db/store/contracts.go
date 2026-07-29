package store

import (
	"context"
	"time"
)

type (
	ID     string
	JSON   []byte
	Record map[string]any
	Input  map[string]any
	Patch  map[string]any
	Filter map[string]any
)

type Page struct {
	Limit  int32
	Offset int32
}

type UserRuntimeRecord struct {
	ID        string
	TeamID    string
	UserID    string
	Name      string
	APIToken  string //nolint:gosec // owner-readable Remote Runtime credential by product design
	CreatedAt time.Time
}

type CreateUserRuntimeInput struct {
	UserID   string
	Name     string
	APIToken string //nolint:gosec // owner-readable Remote Runtime credential by product design
}

type UserRuntimeStore interface {
	CreateUserRuntime(ctx context.Context, input CreateUserRuntimeInput) (UserRuntimeRecord, error)
	GetUserRuntimeByAPIToken(ctx context.Context, apiToken string) (UserRuntimeRecord, error)
	ListUserRuntimes(ctx context.Context, userID string) ([]UserRuntimeRecord, error)
	RevokeUserRuntime(ctx context.Context, runtimeID, userID string) error
}

type BotRemoteRuntimeBindingRecord struct {
	ID             string
	BotID          string
	RuntimeID      string
	IsPrimary      bool
	ToolApproval   JSON
	RuntimeName    string
	RuntimeUserID  string
	BotOwnerUserID string
	RuntimeRevoked bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type BotRemoteRuntimeBindingStore interface {
	CreateOrUpdateMount(ctx context.Context, botID, runtimeID string) (BotRemoteRuntimeBindingRecord, error)
	ListMounts(ctx context.Context, botID string) ([]BotRemoteRuntimeBindingRecord, error)
	GetMount(ctx context.Context, botID, targetID string) (BotRemoteRuntimeBindingRecord, error)
	GetPrimaryMount(ctx context.Context, botID string) (BotRemoteRuntimeBindingRecord, error)
	SetPrimary(ctx context.Context, botID, targetID string) error
	UpdateToolApproval(ctx context.Context, botID, targetID string, config JSON) error
	DeleteMount(ctx context.Context, botID, targetID string) error
}

type AccountRecord struct {
	ID                  string
	Username            string
	Email               string
	Role                string
	DisplayName         string
	AvatarURL           string
	Timezone            string
	PasswordHash        string
	HasPasswordHash     bool
	IsActive            bool
	PrincipalActive     bool
	MembershipActive    bool
	Metadata            string
	CreatedAt           time.Time
	UpdatedAt           time.Time
	JoinedAt            time.Time
	MembershipUpdatedAt time.Time
	LastLoginAt         time.Time
	TitleModelID        string
}

type CreateUserInput struct {
	IsActive bool
	Metadata []byte
}

type CreateAccountInput struct {
	UserID       string
	Username     string
	Email        string
	PasswordHash string
	Role         string
	DisplayName  string
	AvatarURL    string
	IsActive     bool
	DataRoot     string
}

type UpdateAccountAdminInput struct {
	UserID   string
	Role     string
	IsActive *bool
}

type UpdateAccountProfileInput struct {
	UserID       string
	DisplayName  string
	AvatarURL    string
	Timezone     string
	Metadata     string
	TitleModelID string
}

type UpdateAccountPasswordInput struct {
	UserID       string
	PasswordHash string
}

type AccountStore interface {
	CountAccounts(ctx context.Context) (int64, error)
	GetByUserID(ctx context.Context, userID string) (AccountRecord, error)
	GetByIdentity(ctx context.Context, identity string) (AccountRecord, error)
	List(ctx context.Context) ([]AccountRecord, error)
	Search(ctx context.Context, query string, limit int32) ([]AccountRecord, error)
	CreateUser(ctx context.Context, input CreateUserInput) (AccountRecord, error)
	CreateAccount(ctx context.Context, input CreateAccountInput) (AccountRecord, error)
	UpdateLastLogin(ctx context.Context, accountID string) error
	UpdateAdmin(ctx context.Context, input UpdateAccountAdminInput) (AccountRecord, error)
	UpdateProfile(ctx context.Context, input UpdateAccountProfileInput) (AccountRecord, error)
	IsValidTitleModel(ctx context.Context, modelID string) (bool, error)
	UpdatePassword(ctx context.Context, input UpdateAccountPasswordInput) error
	RemoveMember(ctx context.Context, userID string) error
}

type BotStore interface {
	Create(ctx context.Context, input Input) (Record, error)
	GetByID(ctx context.Context, botID ID) (Record, error)
	ListByOwner(ctx context.Context, ownerUserID ID) ([]Record, error)
	UpdateProfile(ctx context.Context, botID ID, input Patch) (Record, error)
	UpdateOwner(ctx context.Context, botID ID, ownerUserID ID) (Record, error)
	UpdateStatus(ctx context.Context, botID ID, status string) (Record, error)
	TouchActivity(ctx context.Context, botID ID) error
	Delete(ctx context.Context, botID ID) error
}

type BotReader interface {
	GetByID(ctx context.Context, botID ID) (Record, error)
}

type ContainerReader interface {
	GetContainerByBotID(ctx context.Context, botID ID) (Record, error)
}

type ModelStore interface {
	Create(ctx context.Context, input Input) (Record, error)
	GetByID(ctx context.Context, modelID ID) (Record, error)
	GetByModelID(ctx context.Context, providerModelID string) ([]Record, error)
	List(ctx context.Context, filter Filter) ([]Record, error)
	ListByType(ctx context.Context, modelType string) ([]Record, error)
	ListByProviderID(ctx context.Context, providerID ID) ([]Record, error)
	ListEnabled(ctx context.Context, filter Filter) ([]Record, error)
	Update(ctx context.Context, modelID ID, input Patch) (Record, error)
	Delete(ctx context.Context, modelID ID) error
	Count(ctx context.Context, filter Filter) (int64, error)
	DeleteByProviderAndType(ctx context.Context, providerID ID, modelType string) error
}

type ProviderStore interface {
	Create(ctx context.Context, input Input) (Record, error)
	GetByID(ctx context.Context, providerID ID) (Record, error)
	GetByName(ctx context.Context, name string) (Record, error)
	GetByClientType(ctx context.Context, clientType string) (Record, error)
	List(ctx context.Context) ([]Record, error)
	Update(ctx context.Context, providerID ID, input Patch) (Record, error)
	Delete(ctx context.Context, providerID ID) error
	Count(ctx context.Context) (int64, error)
}

type ProviderOAuthStore interface {
	GetTokenByProvider(ctx context.Context, providerID ID) (Record, error)
	GetTokenByState(ctx context.Context, state string) (Record, error)
	UpdateOAuthState(ctx context.Context, providerID ID, state string) error
	UpsertToken(ctx context.Context, input Input) (Record, error)
	DeleteToken(ctx context.Context, providerID ID) error
}

type UserProviderOAuthStore interface {
	GetTokenByProvider(ctx context.Context, userID ID, providerID ID) (Record, error)
	GetTokenByState(ctx context.Context, state string) (Record, error)
	UpdateOAuthState(ctx context.Context, userID ID, providerID ID, state string) error
	UpsertToken(ctx context.Context, input Input) (Record, error)
	DeleteToken(ctx context.Context, userID ID, providerID ID) error
}

type ProviderCredentialsReader interface {
	GetProviderCredentials(ctx context.Context, providerID ID) (Record, error)
}

type BotSettingsStore interface {
	GetByBotID(ctx context.Context, botID ID) (Record, error)
	Upsert(ctx context.Context, botID ID, input Input) (Record, error)
	DeleteByBotID(ctx context.Context, botID ID) error
}

type SearchProviderStore interface {
	Create(ctx context.Context, input Input) (Record, error)
	GetByID(ctx context.Context, id ID) (Record, error)
	GetRawByID(ctx context.Context, id ID) (Record, error)
	List(ctx context.Context) ([]Record, error)
	ListByProvider(ctx context.Context, provider string) ([]Record, error)
	Update(ctx context.Context, id ID, input Patch) (Record, error)
	Delete(ctx context.Context, id ID) error
}

type MessageRepository interface {
	Create(ctx context.Context, input Input) (Record, error)
	CreateAsset(ctx context.Context, input Input) (Record, error)
	ListByBot(ctx context.Context, filter Filter) ([]Record, error)
	ListBySession(ctx context.Context, sessionID ID, filter Filter) ([]Record, error)
	ListLatestBySession(ctx context.Context, sessionID ID, limit int32) ([]Record, error)
	ListBefore(ctx context.Context, sessionID ID, before string, limit int32) ([]Record, error)
	Search(ctx context.Context, filter Filter) ([]Record, error)
	DeleteByBot(ctx context.Context, botID ID) error
	DeleteBySession(ctx context.Context, sessionID ID) error
	ListAssetsBatch(ctx context.Context, messageIDs []ID) ([]Record, error)
}

type SessionRepository interface {
	Create(ctx context.Context, input Input) (Record, error)
	GetByID(ctx context.Context, sessionID ID) (Record, error)
	ListByBot(ctx context.Context, botID ID) ([]Record, error)
	ListByRoute(ctx context.Context, routeID ID) ([]Record, error)
	UpdateTitle(ctx context.Context, sessionID ID, title string) (Record, error)
	UpdateMetadata(ctx context.Context, sessionID ID, metadata JSON) (Record, error)
	Touch(ctx context.Context, sessionID ID) error
	SoftDelete(ctx context.Context, sessionID ID) error
}

type PipelineSessionEventRepository interface {
	InsertIdempotent(ctx context.Context, input Input) (ID, error)
	ListBySession(ctx context.Context, sessionID ID, filter Filter) ([]Record, error)
	CountBySession(ctx context.Context, sessionID ID) (int64, error)
}

type CompactionRepository interface {
	CreateLog(ctx context.Context, input Input) (Record, error)
	GetLogByID(ctx context.Context, logID ID) (Record, error)
	ListLogsBySession(ctx context.Context, sessionID ID) ([]Record, error)
	ListLogsByBot(ctx context.Context, botID ID, page Page) ([]Record, error)
	CountLogsByBot(ctx context.Context, botID ID) (int64, error)
	ListUncompactedMessagesBySession(ctx context.Context, sessionID ID) ([]Record, error)
	MarkMessagesCompacted(ctx context.Context, messageIDs []ID, logID ID) error
	CompleteLog(ctx context.Context, logID ID, input Patch) (Record, error)
}

type ChannelConfigRepository interface {
	UpsertBotChannelConfig(ctx context.Context, input Input) (Record, error)
	DeleteBotChannelConfig(ctx context.Context, id ID) error
	UpdateBotChannelConfigDisabled(ctx context.Context, id ID, disabled bool) (Record, error)
	GetBotChannelConfig(ctx context.Context, botID ID, channelType string) (Record, error)
	ListBotChannelConfigsByType(ctx context.Context, channelType string) ([]Record, error)
	SaveMatrixSyncSinceToken(ctx context.Context, botID ID, token string) error
	UpsertUserChannelBinding(ctx context.Context, input Input) (Record, error)
	GetUserChannelBinding(ctx context.Context, userID ID, channelType string) (Record, error)
}

type ChannelIdentityRepository interface {
	Create(ctx context.Context, input Input) (Record, error)
	GetByID(ctx context.Context, id ID) (Record, error)
	UpsertByChannelSubject(ctx context.Context, input Input) (Record, error)
	Search(ctx context.Context, filter Filter) ([]Record, error)
	GetUserByID(ctx context.Context, userID ID) (Record, error)
}

type ChannelRouteRepository interface {
	Create(ctx context.Context, input Input) (Record, error)
	Find(ctx context.Context, botID ID, platform string, conversationID string, threadID string) (Record, error)
	ResolveConversation(ctx context.Context, input Input) (Record, error)
	GetByID(ctx context.Context, routeID ID) (Record, error)
	List(ctx context.Context, filter Filter) ([]Record, error)
	Delete(ctx context.Context, routeID ID) error
	UpdateReplyTarget(ctx context.Context, routeID ID, target string) (Record, error)
	UpdateMetadata(ctx context.Context, routeID ID, metadata JSON) (Record, error)
}

type BotACLStore interface {
	Evaluate(ctx context.Context, input Input) (Record, error)
	GetDefaultEffect(ctx context.Context, botID ID) (string, error)
	SetDefaultEffect(ctx context.Context, botID ID, effect string) error
	ListRules(ctx context.Context, botID ID) ([]Record, error)
	CreateRule(ctx context.Context, input Input) (Record, error)
	UpdateRule(ctx context.Context, id ID, input Patch) (Record, error)
	DeleteRule(ctx context.Context, id ID) error
}

type ObservedConversationStore interface {
	ListByChannelIdentity(ctx context.Context, identityID ID) ([]Record, error)
	ListByChannelType(ctx context.Context, channelType string) ([]Record, error)
}

type ScheduleStore interface {
	ListEnabled(ctx context.Context) ([]Record, error)
	Create(ctx context.Context, input Input) (Record, error)
	GetByID(ctx context.Context, id ID) (Record, error)
	ListByBot(ctx context.Context, botID ID) ([]Record, error)
	Update(ctx context.Context, id ID, input Patch) (Record, error)
	Delete(ctx context.Context, id ID) error
	IncrementCalls(ctx context.Context, id ID) (Record, error)
	CreateLog(ctx context.Context, input Input) (Record, error)
	CompleteLog(ctx context.Context, id ID, input Patch) (Record, error)
	ListLogsByBot(ctx context.Context, botID ID, page Page) ([]Record, error)
	ListLogsBySchedule(ctx context.Context, scheduleID ID, page Page) ([]Record, error)
	CountLogsByBot(ctx context.Context, botID ID) (int64, error)
	CountLogsBySchedule(ctx context.Context, scheduleID ID) (int64, error)
	DeleteLogsByBot(ctx context.Context, botID ID) error
}

type HeartbeatStore interface {
	ListEnabledBots(ctx context.Context) ([]Record, error)
	CreateLog(ctx context.Context, input Input) (Record, error)
	CompleteLog(ctx context.Context, id ID, input Patch) (Record, error)
	ListLogsByBot(ctx context.Context, botID ID, page Page) ([]Record, error)
	CountLogsByBot(ctx context.Context, botID ID) (int64, error)
	DeleteLogsByBot(ctx context.Context, botID ID) error
}

type TokenUsageRepository interface {
	GetByDayAndType(ctx context.Context, filter Filter) ([]Record, error)
	GetByModel(ctx context.Context, filter Filter) ([]Record, error)
	ListDetail(ctx context.Context, filter Filter) ([]Record, error)
	CountDetail(ctx context.Context, filter Filter) (int64, error)
}

type MCPConnectionStore interface {
	ListByBot(ctx context.Context, botID ID) ([]Record, error)
	GetByID(ctx context.Context, id ID) (Record, error)
	Create(ctx context.Context, input Input) (Record, error)
	Update(ctx context.Context, id ID, input Patch) (Record, error)
	UpsertByName(ctx context.Context, input Input) (Record, error)
	Delete(ctx context.Context, id ID) error
	UpdateProbeResult(ctx context.Context, id ID, input Patch) error
}

type MCPOAuthStore interface {
	UpsertDiscovery(ctx context.Context, input Input) (Record, error)
	GetToken(ctx context.Context, connectionID ID) (Record, error)
	GetTokenByState(ctx context.Context, state string) (Record, error)
	UpdatePKCEState(ctx context.Context, connectionID ID, input Patch) error
	UpdateClientSecret(ctx context.Context, connectionID ID, secret string) error
	UpdateTokens(ctx context.Context, connectionID ID, input Patch) error
	UpdateConnectionAuthType(ctx context.Context, connectionID ID, authType string) error
	ClearTokens(ctx context.Context, connectionID ID) error
}

type EmailProviderStore interface {
	Create(ctx context.Context, input Input) (Record, error)
	GetByID(ctx context.Context, id ID) (Record, error)
	GetRawByID(ctx context.Context, id ID) (Record, error)
	List(ctx context.Context, filter Filter) ([]Record, error)
	Update(ctx context.Context, id ID, input Patch) (Record, error)
	Delete(ctx context.Context, id ID) error
}

type EmailBindingStore interface {
	Create(ctx context.Context, input Input) (Record, error)
	GetByID(ctx context.Context, id ID) (Record, error)
	ListByBot(ctx context.Context, botID ID) ([]Record, error)
	ListReadableByProvider(ctx context.Context, providerID ID) ([]Record, error)
	Update(ctx context.Context, id ID, input Patch) (Record, error)
	Delete(ctx context.Context, id ID) error
}

type EmailOutboxStore interface {
	Create(ctx context.Context, input Input) (Record, error)
	MarkSent(ctx context.Context, id ID, input Patch) (Record, error)
	MarkFailed(ctx context.Context, id ID, input Patch) (Record, error)
	GetByID(ctx context.Context, id ID) (Record, error)
	ListByBot(ctx context.Context, botID ID, page Page) ([]Record, error)
	CountByBot(ctx context.Context, botID ID) (int64, error)
}

type EmailOAuthTokenStore interface {
	GetByProvider(ctx context.Context, providerID ID) (Record, error)
	Upsert(ctx context.Context, input Input) (Record, error)
	UpdateState(ctx context.Context, providerID ID, state string) error
	GetByState(ctx context.Context, state string) (Record, error)
	Delete(ctx context.Context, providerID ID) error
}

type MemoryProviderStore interface {
	Create(ctx context.Context, input Input) (Record, error)
	GetByID(ctx context.Context, id ID) (Record, error)
	GetDefault(ctx context.Context) (Record, error)
	List(ctx context.Context) ([]Record, error)
	Update(ctx context.Context, id ID, input Patch) (Record, error)
	Delete(ctx context.Context, id ID) error
}

type WorkspaceStore interface {
	GetBotByID(ctx context.Context, botID ID) (Record, error)
	UpdateBotProfile(ctx context.Context, botID ID, input Patch) (Record, error)
	UpsertContainer(ctx context.Context, input Input) (Record, error)
	GetContainerByBotID(ctx context.Context, botID ID) (Record, error)
	ListAutoStartContainers(ctx context.Context) ([]Record, error)
	DeleteContainerByBotID(ctx context.Context, botID ID) error
	UpdateContainerStarted(ctx context.Context, input Patch) (Record, error)
	UpdateContainerStopped(ctx context.Context, input Patch) (Record, error)
	UpdateContainerStatus(ctx context.Context, input Patch) (Record, error)
	ListSnapshotsWithVersionByContainerID(ctx context.Context, containerID string) ([]Record, error)
	ListVersionsByContainerID(ctx context.Context, containerID string) ([]Record, error)
	GetVersionSnapshotRuntimeName(ctx context.Context, versionID ID) (string, error)
	RecordSnapshotVersion(ctx context.Context, input Input) (Record, error)
	InsertLifecycleEvent(ctx context.Context, input Input) error
}

type RegistryStore interface {
	UpsertProvider(ctx context.Context, definition Input) (Record, error)
	UpsertModel(ctx context.Context, providerID ID, definition Input) (Record, error)
}

type AudioCatalogStore interface {
	ListSpeechProviders(ctx context.Context) ([]Record, error)
	ListTranscriptionProviders(ctx context.Context) ([]Record, error)
	ListSpeechModels(ctx context.Context, filter Filter) ([]Record, error)
	ListTranscriptionModels(ctx context.Context, filter Filter) ([]Record, error)
	GetSpeechModelWithProvider(ctx context.Context, modelID ID) (Record, error)
	GetTranscriptionModelWithProvider(ctx context.Context, modelID ID) (Record, error)
	UpdateModel(ctx context.Context, modelID ID, input Patch) (Record, error)
}

type MessageSearch interface {
	SearchMessages(ctx context.Context, filter Filter) ([]Record, error)
}
