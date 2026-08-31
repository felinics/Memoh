package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
)

type HistoryTurn struct {
	ID                 pgtype.UUID        `json:"id"`
	BotID              pgtype.UUID        `json:"bot_id"`
	SessionID          pgtype.UUID        `json:"session_id"`
	Position           int64              `json:"position"`
	RequestMessageID   pgtype.UUID        `json:"request_message_id"`
	AssistantMessageID pgtype.UUID        `json:"assistant_message_id"`
	SupersededByTurnID pgtype.UUID        `json:"superseded_by_turn_id"`
	SupersededAt       pgtype.Timestamptz `json:"superseded_at"`
	SupersededReason   string             `json:"superseded_reason"`
	CreatedAt          pgtype.Timestamptz `json:"created_at"`
	UpdatedAt          pgtype.Timestamptz `json:"updated_at"`
}

// Queries is the transitional database interface implemented by sqlc-backed stores.
// Domain-specific stores should replace this broad interface module by module.
type Queries interface {
	CreateAgentCredential(ctx context.Context, arg dbsqlc.CreateAgentCredentialParams) (dbsqlc.AgentCredential, error)
	GetAgentCredential(ctx context.Context, id pgtype.UUID) (dbsqlc.AgentCredential, error)
	GetBotAgentCredential(ctx context.Context, arg dbsqlc.GetBotAgentCredentialParams) (dbsqlc.GetBotAgentCredentialRow, error)
	SetBotAgentCredential(ctx context.Context, arg dbsqlc.SetBotAgentCredentialParams) (pgtype.UUID, error)
	ClearBotAgentCredential(ctx context.Context, arg dbsqlc.ClearBotAgentCredentialParams) (pgtype.UUID, error)
	CountBotAgentCredentialRefs(ctx context.Context, agentCredentialID pgtype.UUID) (int64, error)
	GetBotAgentRuntime(ctx context.Context, arg dbsqlc.GetBotAgentRuntimeParams) (string, error)
	RevokeAgentCredentialByID(ctx context.Context, id pgtype.UUID) (dbsqlc.AgentCredential, error)
	RevokeAgentCredentialsForBot(ctx context.Context, botID pgtype.UUID) error
	UpdateAgentCredentialPayloadCAS(ctx context.Context, arg dbsqlc.UpdateAgentCredentialPayloadCASParams) (dbsqlc.AgentCredential, error)
	AcquireProviderTemplateSyncLock(ctx context.Context) error
	ApproveToolApprovalRequest(ctx context.Context, arg dbsqlc.ApproveToolApprovalRequestParams) (dbsqlc.ToolApprovalRequest, error)
	BumpBotRuntimeConfigEpoch(ctx context.Context, botID pgtype.UUID) (int64, error)
	CancelPendingToolApprovalsBySession(ctx context.Context, arg dbsqlc.CancelPendingToolApprovalsBySessionParams) ([]dbsqlc.ToolApprovalRequest, error)
	CancelPendingUserInputsBySession(ctx context.Context, arg dbsqlc.CancelPendingUserInputsBySessionParams) ([]dbsqlc.UserInputRequest, error)
	CancelUserInputRequest(ctx context.Context, arg dbsqlc.CancelUserInputRequestParams) (dbsqlc.UserInputRequest, error)
	ClearBotRuntimeData(ctx context.Context, botID pgtype.UUID) error
	ClearMCPOAuthTokens(ctx context.Context, connectionID pgtype.UUID) error
	CompleteCompactionLog(ctx context.Context, arg dbsqlc.CompleteCompactionLogParams) (dbsqlc.BotHistoryMessageCompact, error)
	CompleteCompactionRollup(ctx context.Context, arg dbsqlc.CompleteCompactionRollupParams) (dbsqlc.BotHistoryMessageCompact, error)
	CompleteScheduleLog(ctx context.Context, arg dbsqlc.CompleteScheduleLogParams) (dbsqlc.ScheduleLog, error)
	CountAccounts(ctx context.Context) (int64, error)
	CountCompactionLogsByBot(ctx context.Context, botID pgtype.UUID) (int64, error)
	CountEmailOutboxByBot(ctx context.Context, botID pgtype.UUID) (int64, error)
	CountMemoryProvidersByDefault(ctx context.Context) (int64, error)
	CountMessageAssetsByBot(ctx context.Context, botID pgtype.UUID) (int64, error)
	CountMessagesByBot(ctx context.Context, botID pgtype.UUID) (int64, error)
	CountMessagesBySession(ctx context.Context, sessionID pgtype.UUID) (int64, error)
	CountModels(ctx context.Context) (int64, error)
	CountModelsByType(ctx context.Context, type_ string) (int64, error)
	CountProviders(ctx context.Context) (int64, error)
	CountScheduleLogsByBot(ctx context.Context, botID pgtype.UUID) (int64, error)
	CountScheduleLogsBySchedule(ctx context.Context, scheduleID pgtype.UUID) (int64, error)
	CountSessionEvents(ctx context.Context, sessionID pgtype.UUID) (int64, error)
	NextSessionEventCursor(ctx context.Context) (int64, error)
	CountTokenUsageRecords(ctx context.Context, arg dbsqlc.CountTokenUsageRecordsParams) (int64, error)
	CreateAccount(ctx context.Context, arg dbsqlc.CreateAccountParams) (dbsqlc.CreateAccountRow, error)
	CreateBotAgent(ctx context.Context, arg dbsqlc.CreateBotAgentParams) (dbsqlc.BotAgent, error)
	CreateBot(ctx context.Context, arg dbsqlc.CreateBotParams) (dbsqlc.CreateBotRow, error)
	CreateBotACLRule(ctx context.Context, arg dbsqlc.CreateBotACLRuleParams) (dbsqlc.BotAclRule, error)
	CreateBotUserGrant(ctx context.Context, arg dbsqlc.CreateBotUserGrantParams) (dbsqlc.BotUserGrant, error)
	CreateConnector(ctx context.Context, arg dbsqlc.CreateConnectorParams) (dbsqlc.Connector, error)
	DeleteBotUserGrantByID(ctx context.Context, id pgtype.UUID) error
	UpsertBotChannelAdmin(ctx context.Context, arg dbsqlc.UpsertBotChannelAdminParams) (dbsqlc.BotChannelAdmin, error)
	DeleteBotChannelAdmin(ctx context.Context, arg dbsqlc.DeleteBotChannelAdminParams) error
	GetBotChannelAdmin(ctx context.Context, arg dbsqlc.GetBotChannelAdminParams) (dbsqlc.BotChannelAdmin, error)
	ListBotChannelAdmins(ctx context.Context, botID pgtype.UUID) ([]dbsqlc.ListBotChannelAdminsRow, error)
	CreateChannelLinkCode(ctx context.Context, arg dbsqlc.CreateChannelLinkCodeParams) (dbsqlc.ChannelLinkCode, error)
	GetChannelLinkCodeByToken(ctx context.Context, token string) (dbsqlc.ChannelLinkCode, error)
	MarkChannelLinkCodeConsumed(ctx context.Context, arg dbsqlc.MarkChannelLinkCodeConsumedParams) (dbsqlc.ChannelLinkCode, error)
	UpsertUserChannelIdentityBinding(ctx context.Context, arg dbsqlc.UpsertUserChannelIdentityBindingParams) (dbsqlc.UserChannelIdentityBinding, error)
	ListChannelIdentityBindings(ctx context.Context) ([]dbsqlc.ListChannelIdentityBindingsRow, error)
	ListChannelIdentityBindingsForUser(ctx context.Context, userID pgtype.UUID) ([]dbsqlc.ListChannelIdentityBindingsForUserRow, error)
	ListChannelIdentityBindingsForBot(ctx context.Context, botID pgtype.UUID) ([]dbsqlc.ListChannelIdentityBindingsForBotRow, error)
	DeleteUserChannelIdentityBinding(ctx context.Context, arg dbsqlc.DeleteUserChannelIdentityBindingParams) error
	ListUserIDsByChannelIdentity(ctx context.Context, channelIdentityID pgtype.UUID) ([]pgtype.UUID, error)
	LockBotForRuntimeReset(ctx context.Context, botID pgtype.UUID) (pgtype.UUID, error)
	GetBotUserGrantByID(ctx context.Context, id pgtype.UUID) (dbsqlc.BotUserGrant, error)
	ListBotUserGrants(ctx context.Context, botID pgtype.UUID) ([]dbsqlc.ListBotUserGrantsRow, error)
	ListBotUserGrantsForUser(ctx context.Context, arg dbsqlc.ListBotUserGrantsForUserParams) ([]dbsqlc.ListBotUserGrantsForUserRow, error)
	UpdateBotUserGrantPermissions(ctx context.Context, arg dbsqlc.UpdateBotUserGrantPermissionsParams) (dbsqlc.BotUserGrant, error)
	ValidateLockedBotRuntimeReset(ctx context.Context, arg dbsqlc.ValidateLockedBotRuntimeResetParams) (pgtype.UUID, error)
	ValidateLockedBotSessionRuntimeReset(ctx context.Context, arg dbsqlc.ValidateLockedBotSessionRuntimeResetParams) (pgtype.UUID, error)
	RefreshLockedBotRuntimeReset(ctx context.Context, arg dbsqlc.RefreshLockedBotRuntimeResetParams) (pgtype.Timestamptz, error)
	RefreshLockedBotSessionRuntimeReset(ctx context.Context, arg dbsqlc.RefreshLockedBotSessionRuntimeResetParams) (pgtype.Timestamptz, error)
	ListAccessibleBots(ctx context.Context, ownerUserID pgtype.UUID) ([]dbsqlc.ListAccessibleBotsRow, error)
	CreateBotEmailBinding(ctx context.Context, arg dbsqlc.CreateBotEmailBindingParams) (dbsqlc.BotEmailBinding, error)
	CreateChannelIdentity(ctx context.Context, arg dbsqlc.CreateChannelIdentityParams) (dbsqlc.ChannelIdentity, error)
	CreateChatRoute(ctx context.Context, arg dbsqlc.CreateChatRouteParams) (dbsqlc.CreateChatRouteRow, error)
	CreateCompactionLog(ctx context.Context, arg dbsqlc.CreateCompactionLogParams) (dbsqlc.BotHistoryMessageCompact, error)
	CreateContextLifecycle(ctx context.Context, arg dbsqlc.CreateContextLifecycleParams) (dbsqlc.ContextLifecycle, error)
	CreateEmailOutbox(ctx context.Context, arg dbsqlc.CreateEmailOutboxParams) (dbsqlc.EmailOutbox, error)
	CreateEmailProvider(ctx context.Context, arg dbsqlc.CreateEmailProviderParams) (dbsqlc.EmailProvider, error)
	CreateFetchProvider(ctx context.Context, arg dbsqlc.CreateFetchProviderParams) (dbsqlc.FetchProvider, error)
	CreateMCPConnection(ctx context.Context, arg dbsqlc.CreateMCPConnectionParams) (dbsqlc.McpConnection, error)
	CreateMemoryProvider(ctx context.Context, arg dbsqlc.CreateMemoryProviderParams) (dbsqlc.MemoryProvider, error)
	CreateHistoryTurn(ctx context.Context, arg dbsqlc.CreateHistoryTurnParams) (HistoryTurn, error)
	CreateHistoryTurnWithIDAtPosition(ctx context.Context, arg dbsqlc.CreateHistoryTurnWithIDAtPositionParams) (HistoryTurn, error)
	CreateMessage(ctx context.Context, arg dbsqlc.CreateMessageParams) (dbsqlc.CreateMessageRow, error)
	CreateMessageAsset(ctx context.Context, arg dbsqlc.CreateMessageAssetParams) (dbsqlc.CreateMessageAssetRow, error)
	CreateModel(ctx context.Context, arg dbsqlc.CreateModelParams) (dbsqlc.Model, error)
	CreateModelVariant(ctx context.Context, arg dbsqlc.CreateModelVariantParams) (dbsqlc.ModelVariant, error)
	CreateProvider(ctx context.Context, arg dbsqlc.CreateProviderParams) (dbsqlc.Provider, error)
	CreateProviderFromTemplate(ctx context.Context, arg dbsqlc.CreateProviderFromTemplateParams) (dbsqlc.Provider, error)
	CreateSchedule(ctx context.Context, arg dbsqlc.CreateScheduleParams) (dbsqlc.Schedule, error)
	CreateScheduleLog(ctx context.Context, arg dbsqlc.CreateScheduleLogParams) (dbsqlc.CreateScheduleLogRow, error)
	CreateSearchProvider(ctx context.Context, arg dbsqlc.CreateSearchProviderParams) (dbsqlc.SearchProvider, error)
	CreateSession(ctx context.Context, arg dbsqlc.CreateSessionParams) (dbsqlc.BotSession, error)
	ForkSessionFromAssistantTurn(ctx context.Context, arg dbsqlc.ForkSessionFromAssistantTurnParams) (dbsqlc.ForkSessionFromAssistantTurnRow, error)
	CreateSessionEvent(ctx context.Context, arg dbsqlc.CreateSessionEventParams) (pgtype.UUID, error)
	CreateStorageProvider(ctx context.Context, arg dbsqlc.CreateStorageProviderParams) (dbsqlc.StorageProvider, error)
	CreateSubagentConfig(ctx context.Context, arg dbsqlc.CreateSubagentConfigParams) (dbsqlc.SubagentConfig, error)
	CreateSubagentForkContext(ctx context.Context, arg dbsqlc.CreateSubagentForkContextParams) (dbsqlc.CreateSubagentForkContextRow, error)
	CreateToolApprovalRequest(ctx context.Context, arg dbsqlc.CreateToolApprovalRequestParams) (dbsqlc.ToolApprovalRequest, error)
	CreateUserInputRequest(ctx context.Context, arg dbsqlc.CreateUserInputRequestParams) (dbsqlc.UserInputRequest, error)
	CreateUser(ctx context.Context, arg dbsqlc.CreateUserParams) (dbsqlc.CreateUserRow, error)
	DeleteBotACLRuleByID(ctx context.Context, id pgtype.UUID) error
	TrimAgentSessionStateLines(ctx context.Context, arg dbsqlc.TrimAgentSessionStateLinesParams) (int64, error)
	DeleteAgentSessionStateLineFilesNotIn(ctx context.Context, arg dbsqlc.DeleteAgentSessionStateLineFilesNotInParams) (int64, error)
	DeleteAgentSessionStateLinesBySession(ctx context.Context, sessionID pgtype.UUID) (int64, error)
	DeleteAgentSessionStatesBySession(ctx context.Context, sessionID pgtype.UUID) (int64, error)
	DeleteAgentSessionPublicationsBySession(ctx context.Context, sessionID pgtype.UUID) (int64, error)
	UpsertAgentSessionPublication(ctx context.Context, arg dbsqlc.UpsertAgentSessionPublicationParams) (int64, error)
	GetAgentSessionCanonicalStateShape(ctx context.Context, arg dbsqlc.GetAgentSessionCanonicalStateShapeParams) (dbsqlc.GetAgentSessionCanonicalStateShapeRow, error)
	DeleteBotByID(ctx context.Context, id pgtype.UUID) error
	DeleteBotChannelConfig(ctx context.Context, arg dbsqlc.DeleteBotChannelConfigParams) error
	DeleteBotEmailBinding(ctx context.Context, id pgtype.UUID) error
	DeleteBotSkillPackageInstallation(ctx context.Context, arg dbsqlc.DeleteBotSkillPackageInstallationParams) (dbsqlc.BotSkillPackageInstallation, error)
	DeleteChatRoute(ctx context.Context, id pgtype.UUID) error
	DeleteCompactionLogsByBot(ctx context.Context, botID pgtype.UUID) error
	DeleteContainerByBotID(ctx context.Context, botID pgtype.UUID) error
	DeleteEmailOAuthToken(ctx context.Context, emailProviderID pgtype.UUID) error
	DeleteEmailProvider(ctx context.Context, id pgtype.UUID) error
	DeleteEmailProviderByIDAndUser(ctx context.Context, arg dbsqlc.DeleteEmailProviderByIDAndUserParams) error
	DeleteFetchProvider(ctx context.Context, id pgtype.UUID) error
	DeleteMCPConnection(ctx context.Context, arg dbsqlc.DeleteMCPConnectionParams) error
	DeleteConnector(ctx context.Context, arg dbsqlc.DeleteConnectorParams) error
	DeleteMCPOAuthToken(ctx context.Context, connectionID pgtype.UUID) error
	DeleteMemoryProvider(ctx context.Context, id pgtype.UUID) error
	DeleteMessageAssets(ctx context.Context, messageID pgtype.UUID) error
	ClearHistoryByBot(ctx context.Context, botID pgtype.UUID) error
	DeleteMessagesByIDs(ctx context.Context, ids []pgtype.UUID) error
	ClearHistoryBySession(ctx context.Context, sessionID pgtype.UUID) error
	DeleteModel(ctx context.Context, id pgtype.UUID) error
	DeleteModelByModelID(ctx context.Context, modelID string) error
	DeleteSessionDiscussCursorsByBot(ctx context.Context, botID pgtype.UUID) error
	DeleteSessionEventsByBot(ctx context.Context, botID pgtype.UUID) error
	DeleteModelByProviderAndType(ctx context.Context, arg dbsqlc.DeleteModelByProviderAndTypeParams) error
	DeleteModelByProviderIDAndModelID(ctx context.Context, arg dbsqlc.DeleteModelByProviderIDAndModelIDParams) error
	DeleteProvider(ctx context.Context, id pgtype.UUID) error
	DeleteProviderOAuthToken(ctx context.Context, providerID pgtype.UUID) error
	DeleteSchedule(ctx context.Context, id pgtype.UUID) error
	DisableSchedule(ctx context.Context, id pgtype.UUID) (dbsqlc.Schedule, error)
	DeleteScheduleLogsByBot(ctx context.Context, botID pgtype.UUID) error
	DeleteScheduleLogsBySchedule(ctx context.Context, scheduleID pgtype.UUID) error
	DeleteSearchProvider(ctx context.Context, id pgtype.UUID) error
	DeleteSettingsByBotID(ctx context.Context, id pgtype.UUID) error
	DeleteUserProviderOAuthToken(ctx context.Context, arg dbsqlc.DeleteUserProviderOAuthTokenParams) error
	EvaluateBotACLRule(ctx context.Context, arg dbsqlc.EvaluateBotACLRuleParams) (string, error)
	FailUserInputRequest(ctx context.Context, arg dbsqlc.FailUserInputRequestParams) (dbsqlc.UserInputRequest, error)
	FindChatRoute(ctx context.Context, arg dbsqlc.FindChatRouteParams) (dbsqlc.FindChatRouteRow, error)
	GetAccountByIdentity(ctx context.Context, identity pgtype.Text) (dbsqlc.TeamAccount, error)
	GetAccountByUserID(ctx context.Context, userID pgtype.UUID) (dbsqlc.TeamAccount, error)
	GetAgentSessionPublicationHead(ctx context.Context, arg dbsqlc.GetAgentSessionPublicationHeadParams) (dbsqlc.GetAgentSessionPublicationHeadRow, error)
	GetRuntimeConfigEpoch(ctx context.Context, arg dbsqlc.GetRuntimeConfigEpochParams) (dbsqlc.GetRuntimeConfigEpochRow, error)
	GetBotRuntimeReset(ctx context.Context, botID pgtype.UUID) (dbsqlc.GetBotRuntimeResetRow, error)
	GetAgentSessionState(ctx context.Context, arg dbsqlc.GetAgentSessionStateParams) (dbsqlc.GetAgentSessionStateRow, error)
	GetRuntimeRoundOutcome(ctx context.Context, arg dbsqlc.GetRuntimeRoundOutcomeParams) (string, error)
	GetRuntimeLeadingUserMessageID(ctx context.Context, arg dbsqlc.GetRuntimeLeadingUserMessageIDParams) (pgtype.UUID, error)
	DeleteRuntimeDecisionProjectionsByRun(ctx context.Context, arg dbsqlc.DeleteRuntimeDecisionProjectionsByRunParams) (int64, error)
	GetBotACLDefaultEffect(ctx context.Context, id pgtype.UUID) (string, error)
	GetBotByID(ctx context.Context, id pgtype.UUID) (dbsqlc.GetBotByIDRow, error)
	GetBotByName(ctx context.Context, name string) (dbsqlc.GetBotByNameRow, error)
	GetBotAgentByID(ctx context.Context, arg dbsqlc.GetBotAgentByIDParams) (dbsqlc.BotAgent, error)
	FindActiveBotAgentByRuntimeProvider(ctx context.Context, arg dbsqlc.FindActiveBotAgentByRuntimeProviderParams) (dbsqlc.BotAgent, error)
	ListBotAgents(ctx context.Context, botID pgtype.UUID) ([]dbsqlc.BotAgent, error)
	LockBotForAgentMutation(ctx context.Context, botID pgtype.UUID) (pgtype.UUID, error)
	UpdateBotAgent(ctx context.Context, arg dbsqlc.UpdateBotAgentParams) (dbsqlc.BotAgent, error)
	SoftDeleteBotAgent(ctx context.Context, arg dbsqlc.SoftDeleteBotAgentParams) (dbsqlc.BotAgent, error)
	BotAgentIsDefault(ctx context.Context, arg dbsqlc.BotAgentIsDefaultParams) (bool, error)
	GetBotChannelConfig(ctx context.Context, arg dbsqlc.GetBotChannelConfigParams) (dbsqlc.BotChannelConfig, error)
	GetBotChannelConfigByExternalIdentity(ctx context.Context, arg dbsqlc.GetBotChannelConfigByExternalIdentityParams) (dbsqlc.BotChannelConfig, error)
	GetBotEmailBindingByBotAndProvider(ctx context.Context, arg dbsqlc.GetBotEmailBindingByBotAndProviderParams) (dbsqlc.BotEmailBinding, error)
	GetBotEmailBindingByID(ctx context.Context, id pgtype.UUID) (dbsqlc.BotEmailBinding, error)
	GetBotOverlayConfig(ctx context.Context, id pgtype.UUID) (dbsqlc.GetBotOverlayConfigRow, error)
	GetBotSkillPackageInstallation(ctx context.Context, arg dbsqlc.GetBotSkillPackageInstallationParams) (dbsqlc.BotSkillPackageInstallation, error)
	GetBotSkillPackageInstallationByID(ctx context.Context, arg dbsqlc.GetBotSkillPackageInstallationByIDParams) (dbsqlc.BotSkillPackageInstallation, error)
	GetBotStorageBinding(ctx context.Context, botID pgtype.UUID) (dbsqlc.BotStorageBinding, error)
	GetHistoryTurnByID(ctx context.Context, arg dbsqlc.GetHistoryTurnByIDParams) (HistoryTurn, error)
	GetVisibleHistoryTurnByMessage(ctx context.Context, arg dbsqlc.GetVisibleHistoryTurnByMessageParams) (HistoryTurn, error)
	GetChannelIdentityByChannelSubject(ctx context.Context, arg dbsqlc.GetChannelIdentityByChannelSubjectParams) (dbsqlc.ChannelIdentity, error)
	GetChannelIdentityByID(ctx context.Context, id pgtype.UUID) (dbsqlc.ChannelIdentity, error)
	GetChannelIdentityByIDForUpdate(ctx context.Context, id pgtype.UUID) (dbsqlc.ChannelIdentity, error)
	GetChatRouteByID(ctx context.Context, id pgtype.UUID) (dbsqlc.GetChatRouteByIDRow, error)
	GetCompactionLogByID(ctx context.Context, id pgtype.UUID) (dbsqlc.BotHistoryMessageCompact, error)
	GetContainerByBotID(ctx context.Context, botID pgtype.UUID) (dbsqlc.Container, error)
	GetBotWorkspaceResourceLimits(ctx context.Context, botID pgtype.UUID) (dbsqlc.BotWorkspaceResourceLimit, error)
	GetDefaultMemoryProvider(ctx context.Context) (dbsqlc.MemoryProvider, error)
	GetEmailOAuthTokenByProvider(ctx context.Context, emailProviderID pgtype.UUID) (dbsqlc.EmailOauthToken, error)
	GetEmailOAuthTokenByState(ctx context.Context, state string) (dbsqlc.EmailOauthToken, error)
	GetEmailOutboxByID(ctx context.Context, id pgtype.UUID) (dbsqlc.EmailOutbox, error)
	GetEmailProviderByID(ctx context.Context, id pgtype.UUID) (dbsqlc.EmailProvider, error)
	GetEmailProviderByIDAndUser(ctx context.Context, arg dbsqlc.GetEmailProviderByIDAndUserParams) (dbsqlc.EmailProvider, error)
	GetEmailProviderByNameAndUser(ctx context.Context, arg dbsqlc.GetEmailProviderByNameAndUserParams) (dbsqlc.EmailProvider, error)
	GetFetchProviderByID(ctx context.Context, id pgtype.UUID) (dbsqlc.FetchProvider, error)
	GetFetchProviderByName(ctx context.Context, name string) (dbsqlc.FetchProvider, error)
	GetContextLifecycleByRunID(ctx context.Context, runID pgtype.UUID) (dbsqlc.ContextLifecycle, error)
	GetLatestAssistantContextLifecycleMetadataByRunID(ctx context.Context, runID pgtype.UUID) ([]byte, error)
	GetLatestAssistantUsage(ctx context.Context, sessionID pgtype.UUID) (int64, error)
	GetLatestVisibleHistoryTurnBySession(ctx context.Context, sessionID pgtype.UUID) (HistoryTurn, error)
	GetLatestPendingToolApprovalBySession(ctx context.Context, arg dbsqlc.GetLatestPendingToolApprovalBySessionParams) (dbsqlc.ToolApprovalRequest, error)
	GetLatestPendingUserInputBySession(ctx context.Context, arg dbsqlc.GetLatestPendingUserInputBySessionParams) (dbsqlc.UserInputRequest, error)
	GetLatestSessionModelID(ctx context.Context, sessionID pgtype.UUID) (pgtype.UUID, error)
	GetMessageByIDBySession(ctx context.Context, arg dbsqlc.GetMessageByIDBySessionParams) (dbsqlc.GetMessageByIDBySessionRow, error)
	GetLatestSessionIDByBot(ctx context.Context, botID pgtype.UUID) (pgtype.UUID, error)
	GetSessionRun(ctx context.Context, runID pgtype.UUID) (dbsqlc.SessionRun, error)
	GetMCPConnectionByID(ctx context.Context, arg dbsqlc.GetMCPConnectionByIDParams) (dbsqlc.McpConnection, error)
	GetConnectorByConnectionID(ctx context.Context, arg dbsqlc.GetConnectorByConnectionIDParams) (dbsqlc.Connector, error)
	GetMCPOAuthToken(ctx context.Context, connectionID pgtype.UUID) (dbsqlc.McpOauthToken, error)
	GetMCPOAuthTokenByState(ctx context.Context, stateParam string) (dbsqlc.McpOauthToken, error)
	GetMemoryProviderByID(ctx context.Context, id pgtype.UUID) (dbsqlc.MemoryProvider, error)
	GetModelByID(ctx context.Context, id pgtype.UUID) (dbsqlc.Model, error)
	GetModelByModelID(ctx context.Context, modelID string) (dbsqlc.Model, error)
	GetModelByProviderAndModelID(ctx context.Context, arg dbsqlc.GetModelByProviderAndModelIDParams) (dbsqlc.Model, error)
	GetPendingToolApprovalByReplyMessage(ctx context.Context, arg dbsqlc.GetPendingToolApprovalByReplyMessageParams) (dbsqlc.ToolApprovalRequest, error)
	GetPendingToolApprovalBySessionShortID(ctx context.Context, arg dbsqlc.GetPendingToolApprovalBySessionShortIDParams) (dbsqlc.ToolApprovalRequest, error)
	GetPendingUserInputByReplyMessage(ctx context.Context, arg dbsqlc.GetPendingUserInputByReplyMessageParams) (dbsqlc.UserInputRequest, error)
	GetPendingUserInputBySessionShortID(ctx context.Context, arg dbsqlc.GetPendingUserInputBySessionShortIDParams) (dbsqlc.UserInputRequest, error)
	GetProviderByClientType(ctx context.Context, clientType string) (dbsqlc.Provider, error)
	GetProviderByID(ctx context.Context, id pgtype.UUID) (dbsqlc.Provider, error)
	GetProviderTemplateByID(ctx context.Context, id pgtype.UUID) (dbsqlc.TemplateProviderTemplate, error)
	GetProviderByName(ctx context.Context, name string) (dbsqlc.Provider, error)
	GetProviderOAuthTokenByProvider(ctx context.Context, providerID pgtype.UUID) (dbsqlc.ProviderOauthToken, error)
	GetProviderOAuthTokenByState(ctx context.Context, state string) (dbsqlc.ProviderOauthToken, error)
	GetScheduleByID(ctx context.Context, id pgtype.UUID) (dbsqlc.Schedule, error)
	GetSearchProviderByID(ctx context.Context, id pgtype.UUID) (dbsqlc.SearchProvider, error)
	GetSearchProviderByName(ctx context.Context, name string) (dbsqlc.SearchProvider, error)
	GetSessionByID(ctx context.Context, id pgtype.UUID) (dbsqlc.BotSession, error)
	GetTurnAgentTurnID(ctx context.Context, arg dbsqlc.GetTurnAgentTurnIDParams) (string, error)
	SetRoundAgentTurnID(ctx context.Context, arg dbsqlc.SetRoundAgentTurnIDParams) (int64, error)
	LockSessionForCommitReconciliation(ctx context.Context, arg dbsqlc.LockSessionForCommitReconciliationParams) (pgtype.UUID, error)
	GetSessionDiscussCursor(ctx context.Context, arg dbsqlc.GetSessionDiscussCursorParams) (dbsqlc.BotSessionDiscussCursor, error)
	GetSessionCacheStats(ctx context.Context, sessionID pgtype.UUID) (dbsqlc.GetSessionCacheStatsRow, error)
	GetSessionUsedSkills(ctx context.Context, sessionID pgtype.UUID) ([]string, error)
	GetSettingsByBotID(ctx context.Context, id pgtype.UUID) (dbsqlc.GetSettingsByBotIDRow, error)
	GetSnapshotByContainerAndRuntimeName(ctx context.Context, arg dbsqlc.GetSnapshotByContainerAndRuntimeNameParams) (dbsqlc.Snapshot, error)
	GetSpeechModelWithProvider(ctx context.Context, id pgtype.UUID) (dbsqlc.GetSpeechModelWithProviderRow, error)
	GetStorageProviderByID(ctx context.Context, id pgtype.UUID) (dbsqlc.StorageProvider, error)
	GetSubagentConfig(ctx context.Context, sessionID pgtype.UUID) (dbsqlc.SubagentConfig, error)
	GetStorageProviderByName(ctx context.Context, name string) (dbsqlc.StorageProvider, error)
	GetTokenUsageByDayAndType(ctx context.Context, arg dbsqlc.GetTokenUsageByDayAndTypeParams) ([]dbsqlc.GetTokenUsageByDayAndTypeRow, error)
	GetTokenUsageByModel(ctx context.Context, arg dbsqlc.GetTokenUsageByModelParams) ([]dbsqlc.GetTokenUsageByModelRow, error)
	GetToolApprovalRequest(ctx context.Context, id pgtype.UUID) (dbsqlc.ToolApprovalRequest, error)
	ListPendingToolApprovalsByRun(ctx context.Context, runID pgtype.UUID) ([]dbsqlc.ToolApprovalRequest, error)
	GetUserInputRequest(ctx context.Context, id pgtype.UUID) (dbsqlc.UserInputRequest, error)
	ListPendingUserInputsByRun(ctx context.Context, runID pgtype.UUID) ([]dbsqlc.UserInputRequest, error)
	ReclaimWaitingDecisionSessionRun(ctx context.Context, arg dbsqlc.ReclaimWaitingDecisionSessionRunParams) (dbsqlc.SessionRun, error)
	GetRespondableUserInputRequest(ctx context.Context, arg dbsqlc.GetRespondableUserInputRequestParams) (dbsqlc.UserInputRequest, error)
	GetUserInputRequestBySessionToolCall(ctx context.Context, arg dbsqlc.GetUserInputRequestBySessionToolCallParams) (dbsqlc.UserInputRequest, error)
	GetTranscriptionModelWithProvider(ctx context.Context, id pgtype.UUID) (dbsqlc.GetTranscriptionModelWithProviderRow, error)
	GetUserByID(ctx context.Context, id pgtype.UUID) (dbsqlc.User, error)
	GetVideoModelWithProvider(ctx context.Context, id pgtype.UUID) (dbsqlc.GetVideoModelWithProviderRow, error)
	GetUserChannelBinding(ctx context.Context, arg dbsqlc.GetUserChannelBindingParams) (dbsqlc.UserChannelBinding, error)
	GetUserProviderOAuthToken(ctx context.Context, arg dbsqlc.GetUserProviderOAuthTokenParams) (dbsqlc.UserProviderOauthToken, error)
	GetUserProviderOAuthTokenByState(ctx context.Context, state string) (dbsqlc.UserProviderOauthToken, error)
	GetVersionSnapshotRuntimeName(ctx context.Context, arg dbsqlc.GetVersionSnapshotRuntimeNameParams) (string, error)
	IncrementScheduleCalls(ctx context.Context, id pgtype.UUID) (dbsqlc.Schedule, error)
	InsertLifecycleEvent(ctx context.Context, arg dbsqlc.InsertLifecycleEventParams) error
	InsertAgentSessionStateLines(ctx context.Context, arg dbsqlc.InsertAgentSessionStateLinesParams) (dbsqlc.InsertAgentSessionStateLinesRow, error)
	ListAgentSessionStateLinePage(ctx context.Context, arg dbsqlc.ListAgentSessionStateLinePageParams) ([]dbsqlc.ListAgentSessionStateLinePageRow, error)
	InsertVersion(ctx context.Context, arg dbsqlc.InsertVersionParams) (dbsqlc.ContainerVersion, error)
	ListAccounts(ctx context.Context) ([]dbsqlc.TeamAccount, error)
	ListCompactionArtifactLineageBySession(ctx context.Context, sessionID pgtype.UUID) ([]dbsqlc.BotHistoryMessageCompact, error)
	ListCompactionArtifactParentIDsBySuccessor(ctx context.Context, arg dbsqlc.ListCompactionArtifactParentIDsBySuccessorParams) ([]pgtype.UUID, error)
	ListActiveMessagesSince(ctx context.Context, arg dbsqlc.ListActiveMessagesSinceParams) ([]dbsqlc.ListActiveMessagesSinceRow, error)
	ListActiveMessagesSinceBySession(ctx context.Context, arg dbsqlc.ListActiveMessagesSinceBySessionParams) ([]dbsqlc.ListActiveMessagesSinceBySessionRow, error)
	ListActiveMessagesSinceWithinBytes(ctx context.Context, arg dbsqlc.ListActiveMessagesSinceWithinBytesParams) ([]dbsqlc.ListActiveMessagesSinceWithinBytesRow, error)
	ListActiveMessagesSinceBySessionWithinBytes(ctx context.Context, arg dbsqlc.ListActiveMessagesSinceBySessionWithinBytesParams) ([]dbsqlc.ListActiveMessagesSinceBySessionWithinBytesRow, error)
	MeasureActiveMessagesBySession(ctx context.Context, arg dbsqlc.MeasureActiveMessagesBySessionParams) (dbsqlc.MeasureActiveMessagesBySessionRow, error)
	ListAutoStartContainers(ctx context.Context) ([]dbsqlc.Container, error)
	ListBotACLRules(ctx context.Context, botID pgtype.UUID) ([]dbsqlc.ListBotACLRulesRow, error)
	ListBotChannelConfigsByType(ctx context.Context, channelType string) ([]dbsqlc.BotChannelConfig, error)
	ListBotEmailBindings(ctx context.Context, botID pgtype.UUID) ([]dbsqlc.BotEmailBinding, error)
	ListBotEmailBindingsByProvider(ctx context.Context, emailProviderID pgtype.UUID) ([]dbsqlc.BotEmailBinding, error)
	ListBotsByOwner(ctx context.Context, ownerUserID pgtype.UUID) ([]dbsqlc.ListBotsByOwnerRow, error)
	ListChatRouteThreadProjectionsByIDs(ctx context.Context, arg dbsqlc.ListChatRouteThreadProjectionsByIDsParams) ([]dbsqlc.ListChatRouteThreadProjectionsByIDsRow, error)
	ListChatRoutes(ctx context.Context, chatID pgtype.UUID) ([]dbsqlc.ListChatRoutesRow, error)
	ListCompactionLogsByBot(ctx context.Context, arg dbsqlc.ListCompactionLogsByBotParams) ([]dbsqlc.BotHistoryMessageCompact, error)
	ListEmailOutboxByBot(ctx context.Context, arg dbsqlc.ListEmailOutboxByBotParams) ([]dbsqlc.EmailOutbox, error)
	ListEmailProviders(ctx context.Context) ([]dbsqlc.EmailProvider, error)
	ListEmailProvidersByProvider(ctx context.Context, provider string) ([]dbsqlc.EmailProvider, error)
	ListEmailProvidersByUser(ctx context.Context, userID pgtype.UUID) ([]dbsqlc.EmailProvider, error)
	ListEmailProvidersByUserAndProvider(ctx context.Context, arg dbsqlc.ListEmailProvidersByUserAndProviderParams) ([]dbsqlc.EmailProvider, error)
	ListFetchProviders(ctx context.Context) ([]dbsqlc.FetchProvider, error)
	ListFetchProvidersByProvider(ctx context.Context, provider string) ([]dbsqlc.FetchProvider, error)
	ListEnabledModels(ctx context.Context) ([]dbsqlc.Model, error)
	ListEnabledModelsByProviderClientType(ctx context.Context, clientType string) ([]dbsqlc.Model, error)
	ListEnabledModelsByType(ctx context.Context, type_ string) ([]dbsqlc.Model, error)
	ListEnabledSchedules(ctx context.Context) ([]dbsqlc.Schedule, error)
	ListBotSkillPackageInstallations(ctx context.Context, botID pgtype.UUID) ([]dbsqlc.BotSkillPackageInstallation, error)
	ListMCPConnectionsByBotID(ctx context.Context, botID pgtype.UUID) ([]dbsqlc.McpConnection, error)
	ListConnectorsByBotID(ctx context.Context, botID pgtype.UUID) ([]dbsqlc.Connector, error)
	ListMemoryProviders(ctx context.Context) ([]dbsqlc.MemoryProvider, error)
	ListMessageAssets(ctx context.Context, messageID pgtype.UUID) ([]dbsqlc.ListMessageAssetsRow, error)
	ListMessageAssetsBatch(ctx context.Context, messageIds []pgtype.UUID) ([]dbsqlc.ListMessageAssetsBatchRow, error)
	AppendMessageToHistoryTurnByRequest(ctx context.Context, arg dbsqlc.AppendMessageToHistoryTurnByRequestParams) (pgtype.UUID, error)
	AppendMessageToLatestHistoryTurn(ctx context.Context, arg dbsqlc.AppendMessageToLatestHistoryTurnParams) (pgtype.UUID, error)
	BindHistoryTurnAssistantByRequest(ctx context.Context, arg dbsqlc.BindHistoryTurnAssistantByRequestParams) (HistoryTurn, error)
	BindLatestHistoryTurnAssistant(ctx context.Context, arg dbsqlc.BindLatestHistoryTurnAssistantParams) (HistoryTurn, error)
	LinkMessageToHistoryTurn(ctx context.Context, arg dbsqlc.LinkMessageToHistoryTurnParams) (pgtype.UUID, error)
	LinkUnassignedMessagesAfterHistoryTurnAssistant(ctx context.Context, turnID pgtype.UUID) error
	HideMessagesByHistoryTurn(ctx context.Context, turnID pgtype.UUID) error
	ListAllMessagesForBackup(ctx context.Context, botID pgtype.UUID) ([]dbsqlc.ListAllMessagesForBackupRow, error)
	ListAllProviderTemplateModels(ctx context.Context, providerTemplateID pgtype.UUID) ([]dbsqlc.TemplateProviderTemplateModel, error)
	ListAllProviderTemplates(ctx context.Context) ([]dbsqlc.TemplateProviderTemplate, error)
	ListHistoryTurnsByBot(ctx context.Context, botID pgtype.UUID) ([]HistoryTurn, error)
	ListMessages(ctx context.Context, botID pgtype.UUID) ([]dbsqlc.ListMessagesRow, error)
	GetMessageByExternalIDBySession(ctx context.Context, arg dbsqlc.GetMessageByExternalIDBySessionParams) (dbsqlc.GetMessageByExternalIDBySessionRow, error)
	GetLocatedMessageByExternalIDBySession(ctx context.Context, arg dbsqlc.GetLocatedMessageByExternalIDBySessionParams) (dbsqlc.GetLocatedMessageByExternalIDBySessionRow, error)
	GetVisibleMessageCursorByExternalIDBySession(ctx context.Context, arg dbsqlc.GetVisibleMessageCursorByExternalIDBySessionParams) (dbsqlc.GetVisibleMessageCursorByExternalIDBySessionRow, error)
	GetVisibleMessageCursorByIDBySession(ctx context.Context, arg dbsqlc.GetVisibleMessageCursorByIDBySessionParams) (dbsqlc.GetVisibleMessageCursorByIDBySessionRow, error)
	LocateMessagesWindowByExternalIDBySession(ctx context.Context, arg dbsqlc.LocateMessagesWindowByExternalIDBySessionParams) ([]dbsqlc.LocateMessagesWindowByExternalIDBySessionRow, error)
	ListMessagesAfterBySession(ctx context.Context, arg dbsqlc.ListMessagesAfterBySessionParams) ([]dbsqlc.ListMessagesAfterBySessionRow, error)
	ListMessagesAfterCursorBySession(ctx context.Context, arg dbsqlc.ListMessagesAfterCursorBySessionParams) ([]dbsqlc.ListMessagesAfterCursorBySessionRow, error)
	ListMessagesAfterMessageBySession(ctx context.Context, arg dbsqlc.ListMessagesAfterMessageBySessionParams) ([]dbsqlc.ListMessagesAfterMessageBySessionRow, error)
	ListMessagesBefore(ctx context.Context, arg dbsqlc.ListMessagesBeforeParams) ([]dbsqlc.ListMessagesBeforeRow, error)
	ListMessagesBeforeBySession(ctx context.Context, arg dbsqlc.ListMessagesBeforeBySessionParams) ([]dbsqlc.ListMessagesBeforeBySessionRow, error)
	ListMessagesBeforeCursorBySession(ctx context.Context, arg dbsqlc.ListMessagesBeforeCursorBySessionParams) ([]dbsqlc.ListMessagesBeforeCursorBySessionRow, error)
	ListMessagesBeforeMessageBySession(ctx context.Context, arg dbsqlc.ListMessagesBeforeMessageBySessionParams) ([]dbsqlc.ListMessagesBeforeMessageBySessionRow, error)
	ListMessageRefsByCompactID(ctx context.Context, compactID pgtype.UUID) ([]dbsqlc.ListMessageRefsByCompactIDRow, error)
	ListMessagesByCompactID(ctx context.Context, compactID pgtype.UUID) ([]dbsqlc.ListMessagesByCompactIDRow, error)
	ListMessagesBySession(ctx context.Context, sessionID pgtype.UUID) ([]dbsqlc.ListMessagesBySessionRow, error)
	ListSubagentForkContext(ctx context.Context, sessionID pgtype.UUID) ([]dbsqlc.ListSubagentForkContextRow, error)
	ListMessagesLatest(ctx context.Context, arg dbsqlc.ListMessagesLatestParams) ([]dbsqlc.ListMessagesLatestRow, error)
	ListMessagesLatestBySession(ctx context.Context, arg dbsqlc.ListMessagesLatestBySessionParams) ([]dbsqlc.ListMessagesLatestBySessionRow, error)
	ListMessagesLatestUIBySession(ctx context.Context, arg dbsqlc.ListMessagesLatestUIBySessionParams) ([]dbsqlc.ListMessagesLatestUIBySessionRow, error)
	ListMessagesSince(ctx context.Context, arg dbsqlc.ListMessagesSinceParams) ([]dbsqlc.ListMessagesSinceRow, error)
	ListMessagesSinceBySession(ctx context.Context, arg dbsqlc.ListMessagesSinceBySessionParams) ([]dbsqlc.ListMessagesSinceBySessionRow, error)
	ListVisibleMessagesFromBySession(ctx context.Context, arg dbsqlc.ListVisibleMessagesFromBySessionParams) ([]dbsqlc.ListVisibleMessagesFromBySessionRow, error)
	ListModels(ctx context.Context) ([]dbsqlc.Model, error)
	ListModelsByModelID(ctx context.Context, modelID string) ([]dbsqlc.Model, error)
	ListModelsByProviderClientType(ctx context.Context, clientType string) ([]dbsqlc.Model, error)
	ListModelsByProviderID(ctx context.Context, providerID pgtype.UUID) ([]dbsqlc.Model, error)
	ListModelsByProviderIDAndType(ctx context.Context, arg dbsqlc.ListModelsByProviderIDAndTypeParams) ([]dbsqlc.Model, error)
	ListModelsByType(ctx context.Context, type_ string) ([]dbsqlc.Model, error)
	ListModelVariantsByModelUUID(ctx context.Context, modelUuid pgtype.UUID) ([]dbsqlc.ModelVariant, error)
	ListObservedConversationsByChannelIdentity(ctx context.Context, arg dbsqlc.ListObservedConversationsByChannelIdentityParams) ([]dbsqlc.ListObservedConversationsByChannelIdentityRow, error)
	ListObservedConversationsByChannelType(ctx context.Context, arg dbsqlc.ListObservedConversationsByChannelTypeParams) ([]dbsqlc.ListObservedConversationsByChannelTypeRow, error)
	ListPendingToolApprovalsBySession(ctx context.Context, arg dbsqlc.ListPendingToolApprovalsBySessionParams) ([]dbsqlc.ToolApprovalRequest, error)
	ListPendingUserInputsBySession(ctx context.Context, arg dbsqlc.ListPendingUserInputsBySessionParams) ([]dbsqlc.UserInputRequest, error)
	ListProviders(ctx context.Context) ([]dbsqlc.Provider, error)
	ListProviderTemplateModels(ctx context.Context, providerTemplateID pgtype.UUID) ([]dbsqlc.TemplateProviderTemplateModel, error)
	ListProviderTemplates(ctx context.Context, domain string) ([]dbsqlc.ListProviderTemplatesRow, error)
	ListReadableBindingsByProvider(ctx context.Context, emailProviderID pgtype.UUID) ([]dbsqlc.BotEmailBinding, error)
	ListRecentAssistantMessagesBySession(ctx context.Context, arg dbsqlc.ListRecentAssistantMessagesBySessionParams) ([]dbsqlc.ListRecentAssistantMessagesBySessionRow, error)
	HasUnmaterializedContextLifecycleMetadataBySession(ctx context.Context, sessionID pgtype.UUID) (bool, error)
	ListRecentContextLifecyclesBySession(ctx context.Context, arg dbsqlc.ListRecentContextLifecyclesBySessionParams) ([]dbsqlc.ListRecentContextLifecyclesBySessionRow, error)
	ListScheduleLogsByBot(ctx context.Context, arg dbsqlc.ListScheduleLogsByBotParams) ([]dbsqlc.ListScheduleLogsByBotRow, error)
	ListScheduleLogsBySchedule(ctx context.Context, arg dbsqlc.ListScheduleLogsByScheduleParams) ([]dbsqlc.ListScheduleLogsByScheduleRow, error)
	ListSchedulesByBot(ctx context.Context, botID pgtype.UUID) ([]dbsqlc.Schedule, error)
	ListSearchProviders(ctx context.Context) ([]dbsqlc.SearchProvider, error)
	ListSearchProvidersByProvider(ctx context.Context, provider string) ([]dbsqlc.SearchProvider, error)
	ListSessionDiscussCursorsByBot(ctx context.Context, botID pgtype.UUID) ([]dbsqlc.BotSessionDiscussCursor, error)
	ListSessionEventsByBot(ctx context.Context, botID pgtype.UUID) ([]dbsqlc.BotSessionEvent, error)
	ListSessionEventsBySession(ctx context.Context, sessionID pgtype.UUID) ([]dbsqlc.BotSessionEvent, error)
	ListSessionEventsBySessionAfter(ctx context.Context, arg dbsqlc.ListSessionEventsBySessionAfterParams) ([]dbsqlc.BotSessionEvent, error)
	ListSessionsByBot(ctx context.Context, botID pgtype.UUID) ([]dbsqlc.ListSessionsByBotRow, error)
	ListSessionsByBotAndCreatedByUser(ctx context.Context, arg dbsqlc.ListSessionsByBotAndCreatedByUserParams) ([]dbsqlc.ListSessionsByBotAndCreatedByUserRow, error)
	ListSessionsByBotAndCreatedByUserPaged(ctx context.Context, arg dbsqlc.ListSessionsByBotAndCreatedByUserPagedParams) ([]dbsqlc.ListSessionsByBotAndCreatedByUserPagedRow, error)
	ListSessionsByBotPaged(ctx context.Context, arg dbsqlc.ListSessionsByBotPagedParams) ([]dbsqlc.ListSessionsByBotPagedRow, error)
	ListSessionsByRoute(ctx context.Context, routeID pgtype.UUID) ([]dbsqlc.BotSession, error)
	ListSnapshotsByContainerID(ctx context.Context, containerID string) ([]dbsqlc.Snapshot, error)
	ListSnapshotsWithVersionByContainerID(ctx context.Context, containerID string) ([]dbsqlc.ListSnapshotsWithVersionByContainerIDRow, error)
	ListSpeechModels(ctx context.Context) ([]dbsqlc.ListSpeechModelsRow, error)
	ListSpeechModelsByProviderID(ctx context.Context, providerID pgtype.UUID) ([]dbsqlc.Model, error)
	ListSpeechProviders(ctx context.Context) ([]dbsqlc.Provider, error)
	ListStorageProviders(ctx context.Context) ([]dbsqlc.StorageProvider, error)
	ListSubagentSessionsByParent(ctx context.Context, parentSessionID pgtype.UUID) ([]dbsqlc.BotSession, error)
	ListTerminalSessionRunsNeedingContextLifecycle(ctx context.Context, batchSize int32) ([]dbsqlc.ListTerminalSessionRunsNeedingContextLifecycleRow, error)
	ListTokenUsageRecords(ctx context.Context, arg dbsqlc.ListTokenUsageRecordsParams) ([]dbsqlc.ListTokenUsageRecordsRow, error)
	ListToolApprovalsBySession(ctx context.Context, arg dbsqlc.ListToolApprovalsBySessionParams) ([]dbsqlc.ToolApprovalRequest, error)
	ListToolApprovalsBySessionToolCalls(ctx context.Context, arg dbsqlc.ListToolApprovalsBySessionToolCallsParams) ([]dbsqlc.ToolApprovalRequest, error)
	ListUserInputsBySession(ctx context.Context, arg dbsqlc.ListUserInputsBySessionParams) ([]dbsqlc.UserInputRequest, error)
	ListUserInputsBySessionToolCalls(ctx context.Context, arg dbsqlc.ListUserInputsBySessionToolCallsParams) ([]dbsqlc.UserInputRequest, error)
	ListTranscriptionModels(ctx context.Context) ([]dbsqlc.ListTranscriptionModelsRow, error)
	ListTranscriptionModelsByProviderID(ctx context.Context, providerID pgtype.UUID) ([]dbsqlc.Model, error)
	ListTranscriptionProviders(ctx context.Context) ([]dbsqlc.Provider, error)
	ListUncompactedMessagesBySession(ctx context.Context, sessionID pgtype.UUID) ([]dbsqlc.ListUncompactedMessagesBySessionRow, error)
	ListVideoModels(ctx context.Context) ([]dbsqlc.ListVideoModelsRow, error)
	ListVideoModelsByProviderID(ctx context.Context, providerID pgtype.UUID) ([]dbsqlc.Model, error)
	ListVideoProviders(ctx context.Context) ([]dbsqlc.Provider, error)
	ListUserChannelBindingsByPlatform(ctx context.Context, channelType string) ([]dbsqlc.UserChannelBinding, error)
	ListVersionsByContainerID(ctx context.Context, containerID string) ([]dbsqlc.ListVersionsByContainerIDRow, error)
	MarkMessagesCompacted(ctx context.Context, arg dbsqlc.MarkMessagesCompactedParams) (int64, error)
	NextVersion(ctx context.Context, containerID string) (int32, error)
	RejectToolApprovalRequest(ctx context.Context, arg dbsqlc.RejectToolApprovalRequestParams) (dbsqlc.ToolApprovalRequest, error)
	RedeemChannelLinkCode(ctx context.Context, arg dbsqlc.RedeemChannelLinkCodeParams) (dbsqlc.UserChannelIdentityBinding, error)
	SaveMatrixSyncSinceToken(ctx context.Context, arg dbsqlc.SaveMatrixSyncSinceTokenParams) (int64, error)
	SearchAccounts(ctx context.Context, arg dbsqlc.SearchAccountsParams) ([]dbsqlc.TeamAccount, error)
	SearchChannelIdentities(ctx context.Context, arg dbsqlc.SearchChannelIdentitiesParams) ([]dbsqlc.ChannelIdentity, error)
	SearchMessages(ctx context.Context, arg dbsqlc.SearchMessagesParams) ([]dbsqlc.SearchMessagesRow, error)
	SetBotACLDefaultEffect(ctx context.Context, arg dbsqlc.SetBotACLDefaultEffectParams) error
	SetRouteActiveSession(ctx context.Context, arg dbsqlc.SetRouteActiveSessionParams) error
	SetSessionNextTurnPosition(ctx context.Context, arg dbsqlc.SetSessionNextTurnPositionParams) error
	SoftDeleteSession(ctx context.Context, id pgtype.UUID) error
	SoftDeleteSessionsByBot(ctx context.Context, botID pgtype.UUID) error
	SubmitUserInputRequest(ctx context.Context, arg dbsqlc.SubmitUserInputRequestParams) (dbsqlc.UserInputRequest, error)
	TouchBotActivity(ctx context.Context, botID pgtype.UUID) error
	TouchSession(ctx context.Context, id pgtype.UUID) error
	UpdateAccountAdmin(ctx context.Context, arg dbsqlc.UpdateAccountAdminParams) (dbsqlc.UpdateAccountAdminRow, error)
	UpdateAccountLastLogin(ctx context.Context, userID pgtype.UUID) (pgtype.UUID, error)
	UpdateAccountPassword(ctx context.Context, arg dbsqlc.UpdateAccountPasswordParams) (pgtype.UUID, error)
	UpdateAccountProfile(ctx context.Context, arg dbsqlc.UpdateAccountProfileParams) (dbsqlc.UpdateAccountProfileRow, error)
	UpdateBotACLRule(ctx context.Context, arg dbsqlc.UpdateBotACLRuleParams) (dbsqlc.BotAclRule, error)
	UpdateBotChannelConfigDisabled(ctx context.Context, arg dbsqlc.UpdateBotChannelConfigDisabledParams) (dbsqlc.BotChannelConfig, error)
	UpdateBotEmailBinding(ctx context.Context, arg dbsqlc.UpdateBotEmailBindingParams) (dbsqlc.BotEmailBinding, error)
	UpdateBotOwner(ctx context.Context, arg dbsqlc.UpdateBotOwnerParams) (dbsqlc.UpdateBotOwnerRow, error)
	UpdateBotProfile(ctx context.Context, arg dbsqlc.UpdateBotProfileParams) (dbsqlc.UpdateBotProfileRow, error)
	UpdateBotStatus(ctx context.Context, arg dbsqlc.UpdateBotStatusParams) error
	UpdateChatRouteMetadata(ctx context.Context, arg dbsqlc.UpdateChatRouteMetadataParams) error
	UpdateChatRouteReplyTarget(ctx context.Context, arg dbsqlc.UpdateChatRouteReplyTargetParams) error
	UpdateContainerStarted(ctx context.Context, botID pgtype.UUID) error
	UpdateContainerStatus(ctx context.Context, arg dbsqlc.UpdateContainerStatusParams) error
	UpdateContainerStopped(ctx context.Context, botID pgtype.UUID) error
	UpdateEmailOAuthState(ctx context.Context, arg dbsqlc.UpdateEmailOAuthStateParams) error
	UpdateEmailOutboxFailed(ctx context.Context, arg dbsqlc.UpdateEmailOutboxFailedParams) error
	UpdateEmailOutboxSent(ctx context.Context, arg dbsqlc.UpdateEmailOutboxSentParams) error
	UpdateEmailProvider(ctx context.Context, arg dbsqlc.UpdateEmailProviderParams) (dbsqlc.EmailProvider, error)
	UpdateEmailProviderByIDAndUser(ctx context.Context, arg dbsqlc.UpdateEmailProviderByIDAndUserParams) (dbsqlc.EmailProvider, error)
	UpdateFetchProvider(ctx context.Context, arg dbsqlc.UpdateFetchProviderParams) (dbsqlc.FetchProvider, error)
	UpdateMCPConnection(ctx context.Context, arg dbsqlc.UpdateMCPConnectionParams) (dbsqlc.McpConnection, error)
	UpdateMCPConnectionActive(ctx context.Context, arg dbsqlc.UpdateMCPConnectionActiveParams) error
	UpdateMCPConnectionAuthType(ctx context.Context, arg dbsqlc.UpdateMCPConnectionAuthTypeParams) error
	UpdateMCPConnectionProbeResult(ctx context.Context, arg dbsqlc.UpdateMCPConnectionProbeResultParams) error
	UpdateConnectorEnabled(ctx context.Context, arg dbsqlc.UpdateConnectorEnabledParams) (int64, error)
	UpdateMCPOAuthClientSecret(ctx context.Context, arg dbsqlc.UpdateMCPOAuthClientSecretParams) error
	UpdateMCPOAuthPKCEState(ctx context.Context, arg dbsqlc.UpdateMCPOAuthPKCEStateParams) error
	UpdateMCPOAuthTokens(ctx context.Context, arg dbsqlc.UpdateMCPOAuthTokensParams) error
	UpdateMemoryProvider(ctx context.Context, arg dbsqlc.UpdateMemoryProviderParams) (dbsqlc.MemoryProvider, error)
	UpdateModel(ctx context.Context, arg dbsqlc.UpdateModelParams) (dbsqlc.Model, error)
	UpdateProvider(ctx context.Context, arg dbsqlc.UpdateProviderParams) (dbsqlc.Provider, error)
	SetProviderTemplateActive(ctx context.Context, arg dbsqlc.SetProviderTemplateActiveParams) error
	SetProviderTemplateModelActive(ctx context.Context, arg dbsqlc.SetProviderTemplateModelActiveParams) error
	UpdateProviderOAuthState(ctx context.Context, arg dbsqlc.UpdateProviderOAuthStateParams) error
	UpdateSchedule(ctx context.Context, arg dbsqlc.UpdateScheduleParams) (dbsqlc.Schedule, error)
	UpdateSearchProvider(ctx context.Context, arg dbsqlc.UpdateSearchProviderParams) (dbsqlc.SearchProvider, error)
	UpdateSessionMetadata(ctx context.Context, arg dbsqlc.UpdateSessionMetadataParams) (dbsqlc.BotSession, error)
	UpdateSessionRuntimeMetadata(ctx context.Context, arg dbsqlc.UpdateSessionRuntimeMetadataParams) (dbsqlc.BotSession, error)
	UpdateSessionTitle(ctx context.Context, arg dbsqlc.UpdateSessionTitleParams) (dbsqlc.BotSession, error)
	UpdateSessionTypeAndMetadata(ctx context.Context, arg dbsqlc.UpdateSessionTypeAndMetadataParams) (dbsqlc.BotSession, error)
	UpdateAbortedContextLifecycleSnapshot(ctx context.Context, arg dbsqlc.UpdateAbortedContextLifecycleSnapshotParams) (dbsqlc.ContextLifecycle, error)
	UpdateToolApprovalPromptMessage(ctx context.Context, arg dbsqlc.UpdateToolApprovalPromptMessageParams) (dbsqlc.ToolApprovalRequest, error)
	UpdateUserInputAssistantMessage(ctx context.Context, arg dbsqlc.UpdateUserInputAssistantMessageParams) (dbsqlc.UserInputRequest, error)
	UpdateUserInputInteraction(ctx context.Context, arg dbsqlc.UpdateUserInputInteractionParams) (dbsqlc.UserInputRequest, error)
	UpdateUserInputPromptMessage(ctx context.Context, arg dbsqlc.UpdateUserInputPromptMessageParams) (dbsqlc.UserInputRequest, error)
	UpdateUserInputToolResultMessage(ctx context.Context, arg dbsqlc.UpdateUserInputToolResultMessageParams) (dbsqlc.UserInputRequest, error)
	ReplaceHistoryTurn(ctx context.Context, arg dbsqlc.ReplaceHistoryTurnParams) (dbsqlc.ReplaceHistoryTurnRow, error)
	SupersedeHistoryTurn(ctx context.Context, arg dbsqlc.SupersedeHistoryTurnParams) (HistoryTurn, error)
	UpdateUserProviderOAuthState(ctx context.Context, arg dbsqlc.UpdateUserProviderOAuthStateParams) error
	UpsertAccountByUsername(ctx context.Context, arg dbsqlc.UpsertAccountByUsernameParams) (dbsqlc.UpsertAccountByUsernameRow, error)
	UpsertAbortedContextLifecycle(ctx context.Context, arg dbsqlc.UpsertAbortedContextLifecycleParams) (dbsqlc.ContextLifecycle, error)
	UpsertTerminalContextLifecycle(ctx context.Context, arg dbsqlc.UpsertTerminalContextLifecycleParams) (dbsqlc.ContextLifecycle, error)
	UpsertAgentSessionState(ctx context.Context, arg dbsqlc.UpsertAgentSessionStateParams) (dbsqlc.AgentSessionState, error)
	PruneAgentSessionStateVersions(ctx context.Context, arg dbsqlc.PruneAgentSessionStateVersionsParams) (int64, error)
	UpsertBotChannelConfig(ctx context.Context, arg dbsqlc.UpsertBotChannelConfigParams) (dbsqlc.BotChannelConfig, error)
	UpsertBotSettings(ctx context.Context, arg dbsqlc.UpsertBotSettingsParams) (dbsqlc.UpsertBotSettingsRow, error)
	UpsertBotStorageBinding(ctx context.Context, arg dbsqlc.UpsertBotStorageBindingParams) (dbsqlc.BotStorageBinding, error)
	UpsertBotWorkspaceResourceLimits(ctx context.Context, arg dbsqlc.UpsertBotWorkspaceResourceLimitsParams) (dbsqlc.BotWorkspaceResourceLimit, error)
	UpsertChannelIdentityByChannelSubject(ctx context.Context, arg dbsqlc.UpsertChannelIdentityByChannelSubjectParams) (dbsqlc.ChannelIdentity, error)
	UpsertContainer(ctx context.Context, arg dbsqlc.UpsertContainerParams) error
	UpsertEmailOAuthToken(ctx context.Context, arg dbsqlc.UpsertEmailOAuthTokenParams) (dbsqlc.EmailOauthToken, error)
	UpsertMCPConnectionByName(ctx context.Context, arg dbsqlc.UpsertMCPConnectionByNameParams) (dbsqlc.McpConnection, error)
	UpsertBotSkillPackageInstallation(ctx context.Context, arg dbsqlc.UpsertBotSkillPackageInstallationParams) (dbsqlc.BotSkillPackageInstallation, error)
	UpsertMCPOAuthDiscovery(ctx context.Context, arg dbsqlc.UpsertMCPOAuthDiscoveryParams) (dbsqlc.McpOauthToken, error)
	UpsertProviderOAuthToken(ctx context.Context, arg dbsqlc.UpsertProviderOAuthTokenParams) (dbsqlc.ProviderOauthToken, error)
	UpsertRegistryModel(ctx context.Context, arg dbsqlc.UpsertRegistryModelParams) (dbsqlc.Model, error)
	UpsertRegistryProvider(ctx context.Context, arg dbsqlc.UpsertRegistryProviderParams) (dbsqlc.Provider, error)
	UpsertProviderTemplate(ctx context.Context, arg dbsqlc.UpsertProviderTemplateParams) (dbsqlc.TemplateProviderTemplate, error)
	UpsertProviderTemplateModel(ctx context.Context, arg dbsqlc.UpsertProviderTemplateModelParams) (dbsqlc.TemplateProviderTemplateModel, error)
	UpsertSessionDiscussCursor(ctx context.Context, arg dbsqlc.UpsertSessionDiscussCursorParams) (dbsqlc.BotSessionDiscussCursor, error)
	UpsertSnapshot(ctx context.Context, arg dbsqlc.UpsertSnapshotParams) (dbsqlc.Snapshot, error)
	UpsertUserChannelBinding(ctx context.Context, arg dbsqlc.UpsertUserChannelBindingParams) (dbsqlc.UserChannelBinding, error)
	UpsertUserProviderOAuthToken(ctx context.Context, arg dbsqlc.UpsertUserProviderOAuthTokenParams) (dbsqlc.UserProviderOauthToken, error)
	WithTx(tx pgx.Tx) Queries
}
