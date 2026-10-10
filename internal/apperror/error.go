package apperror

import (
	"errors"
	"net/http"
	"strings"
)

// Code is the stable machine identity shared by every transport. Client logic
// may branch on it; it must never branch on Detail or an underlying cause.
type Code string

const (
	CodeCapabilityNotFound                       Code = "capability.not_found"
	CodeCapabilityApprovalRequired               Code = "capability.approval_required"
	CodeCapabilityOperationFailed                Code = "capability.operation_failed"
	CodeCapabilityRequestInvalid                 Code = "capability.request_invalid"
	CodeCapabilityAccessDenied                   Code = "capability.access_denied"
	CodeWorkspaceDependencyDiscoveryFailed       Code = "workspace_dependency.discovery_failed"
	CodeWorkspaceDependencyDefinitionUnavailable Code = "workspace_dependency.definition_unavailable"
	CodeWorkspaceDependencyDefinitionInvalid     Code = "workspace_dependency.definition_invalid"
	CodeWorkspaceDependencyCatalogUnavailable    Code = "workspace_dependency.catalog_unavailable"
	CodeBotNameTaken                             Code = "bot.name_taken"
	CodeBotNameInvalid                           Code = "bot.name_invalid"
	CodeBotAgentNotFound                         Code = "bot_agent.not_found"
	CodeBotAgentNameTaken                        Code = "bot_agent.name_taken"
	CodeBotAgentInvalidRuntime                   Code = "bot_agent.invalid_runtime"
	CodeBotAgentInvalidMetadata                  Code = "bot_agent.invalid_metadata"
	CodeBotAgentDefaultInUse                     Code = "bot_agent.default_in_use"
	CodeBotAgentUnavailable                      Code = "bot_agent.unavailable"
	CodeBotAgentProviderDirectRuntime            Code = "bot_agent.provider_direct_runtime"
	CodeChannelRuntimeUnavailable                Code = "channel.runtime_unavailable"
	CodeChannelVerificationFailed                Code = "channel.verification_failed"
	CodeAgentChatModelNotConfigured              Code = "agent.chat_model_not_configured"
	CodeChannelEnableFailed                      Code = "channel.enable_failed"
	CodeChannelWebhookEndpointInvalid            Code = "channel.webhook_endpoint_invalid"
	CodeChannelBindingRequired                   Code = "channel.binding_required"
	CodeNetworkProviderNotConfigured             Code = "network.provider_not_configured"
	CodeUserCannotRemoveSelf                     Code = "user.cannot_remove_self"
	CodeCompactionModelUnavailable               Code = "compaction.model_unavailable"
	CodeSettingsReasoningEffortInvalid           Code = "settings.reasoning_effort_invalid"
	CodeSettingsReasoningUnavailable             Code = "settings.reasoning_options_unavailable"
	CodeContextBudgetUnsatisfied                 Code = "context.budget_unsatisfied"
	CodeContextProtectedOverflow                 Code = "context.protected_overflow"
	CodeWorkspaceUnreachable                     Code = "workspace.unreachable"
	CodeWorkspaceTemplateBootstrapFailed         Code = "workspace.template_bootstrap_failed"
	CodeWorkspaceDisplayPrepareFailed            Code = "workspace.display_prepare_failed"
	CodeWorkspaceDependencyNotFound              Code = "workspace_dependency.not_found"
	CodeWorkspaceDependencyRequestInvalid        Code = "workspace_dependency.request_invalid"
	CodeWorkspaceDependencyActionUnsupported     Code = "workspace_dependency.action_unsupported"
	CodeWorkspaceDependencyPlatformUnsupported   Code = "workspace_dependency.platform_unsupported"
	CodeWorkspaceDependencyBusy                  Code = "workspace_dependency.busy"
	CodeWorkspaceDependencyRequired              Code = "workspace_dependency.required"
	CodeWorkspaceDependencyPrerequisitesChanged  Code = "workspace_dependency.prerequisites_changed"
	CodeWorkspaceDependencyWorkspaceNotRunning   Code = "workspace_dependency.workspace_not_running"
	CodeWorkspaceDependencyWorkspaceMissing      Code = "workspace_dependency.workspace_missing"
	CodeWorkspaceDependencyRollbackUnavailable   Code = "workspace_dependency.rollback_unavailable"
	CodeWorkspaceDependencyOperationFailed       Code = "workspace_dependency.operation_failed"
	CodeWorkspaceDependencyOperationInterrupted  Code = "workspace_dependency.operation_interrupted"
	CodeProviderTemplateNotFound                 Code = "provider_template.not_found"
	CodeProviderTemplateDomainInvalid            Code = "provider_template.domain_invalid"
	CodeProviderTemplateDomainMismatch           Code = "provider_template.domain_mismatch"
	CodeProviderTemplateOperationFailed          Code = "provider_template.operation_failed"
	CodeProviderNameTaken                        Code = "provider.name_taken"
	CodeProviderTemplateRequestInvalid           Code = "provider_template.request_invalid"
	CodeSearchProviderTypeConflict               Code = "search_provider.type_conflict"
	CodeSearchProviderInvalidProvider            Code = "search_provider.invalid_provider"
	CodeConnectorRequestInvalid                  Code = "connector.request_invalid"
	CodeConnectorNotConfigured                   Code = "connector.not_configured"
	CodeConnectorNotFound                        Code = "connector.not_found"
	CodeConnectorConflict                        Code = "connector.conflict"
	CodeConnectorRequestRejected                 Code = "connector.request_rejected"
	CodeConnectorUpstreamUnavailable             Code = "connector.upstream_unavailable"
	CodeConnectorOperationFailed                 Code = "connector.operation_failed"
	CodeSkillBuiltinReadOnly                     Code = "skill.builtin_read_only"
	CodeSkillNameTaken                           Code = "skill.name_taken"
	CodeSkillSaveFailed                          Code = "skill.save_failed"
	CodeSkillRegistryReadOnly                    Code = "skill.registry_read_only"
	CodeSkillNameInvalid                         Code = "skill.name_invalid"
	CodeSkillNotFound                            Code = "skill.not_found"
	CodeTTSTextTooLong                           Code = "tts.text_too_long"
	CodeTTSModelNotConfigured                    Code = "tts.model_not_configured"
	CodeWorkspaceArchiveInvalid                  Code = "workspace.archive_invalid"
	CodeBotBackupBundleInvalid                   Code = "bot_backup.bundle_invalid"
	CodeFetchProviderNativeManaged               Code = "fetch_provider.native_managed"
	CodeAppNotFound                              Code = "app.not_found"
	CodeAppRequestInvalid                        Code = "app.request_invalid"
	CodeAppBusy                                  Code = "app.busy"
	CodeAppOperationFailed                       Code = "app.operation_failed"
	CodeAppDependenciesUnavailable               Code = "app.dependencies_unavailable"
	CodeAppPrerequisiteFailed                    Code = "app.prerequisite_failed"
	CodeRegistryUnavailable                      Code = "registry.unavailable"
	CodeRegistryAppNotFound                      Code = "registry.app_not_found"
	CodeRegistryAppInvalid                       Code = "registry.app_invalid"
	CodeRegistryAppInstallFailed                 Code = "registry.app_install_failed"
	CodeProfileRequestInvalid                    Code = "profile.request_invalid"
	CodeProfileTitleModelInvalid                 Code = "profile.title_model_invalid"
	CodeProfileUpdateFailed                      Code = "profile.update_failed"
	CodeACPRequestInvalid                        Code = "acp.request_invalid"
	CodeACPAccessForbidden                       Code = "acp.access_forbidden"
	CodeACPRuntimeNotFound                       Code = "acp.runtime_not_found"
	CodeACPRuntimeConflict                       Code = "acp.runtime_conflict"
	CodeACPRuntimeLimitReached                   Code = "acp.runtime_limit_reached"
	CodeACPOperationFailed                       Code = "acp.operation_failed"
	CodeACPCommandNotFound                       Code = "acp.command_not_found"
	CodeExternalAgentTurnReplacementUnsupported  Code = "external_agent.turn_replacement_unsupported"
	CodeACPModelSelectionUnsupported             Code = "acp.model_selection_unsupported"
	CodeACPModelIDRequired                       Code = "acp.model_id_required"
	CodeACPModelUnavailable                      Code = "acp.model_unavailable"
	CodeACPReasoningUnsupported                  Code = "acp.reasoning_selection_unsupported"
	CodeACPReasoningEffortRequired               Code = "acp.reasoning_effort_required"
	CodeACPReasoningUnavailable                  Code = "acp.reasoning_effort_unavailable"
	CodeACPModeSelectionUnsupported              Code = "acp.mode_selection_unsupported"
	CodeACPModeIDRequired                        Code = "acp.mode_id_required"
	CodeACPModeUnavailable                       Code = "acp.mode_unavailable"
	CodeACPConfigUpdateFailed                    Code = "acp.config_update_failed"
	CodeRuntimeControlUnsupported                Code = "runtime_control.unsupported"
	CodeRuntimeControlCommandUnavailable         Code = "runtime_control.command_unavailable"
	CodeRuntimeControlModeUnavailable            Code = "runtime_control.mode_unavailable"
	CodeRuntimeControlGoalRequiresDefaultMode    Code = "runtime_control.goal_requires_default_mode"
	CodeWorkdirGitBusy                           Code = "workdir.git_busy"
	CodeWorkdirGitBranchUnavailable              Code = "workdir.git_branch_unavailable"
	CodeWorkdirGitSwitchFailed                   Code = "workdir.git_switch_failed"
	CodeWorkdirGitUnavailable                    Code = "workdir.git_unavailable"
	CodeWorkdirRemoteUnsupportedForAgent         Code = "workdir.remote_unsupported_for_agent"
	CodeChatMessageEmpty                         Code = "chat.message_empty"
	CodeRuntimeControlThreadUnavailable          Code = "runtime_control.thread_unavailable"
	CodeRuntimeControlCancelled                  Code = "runtime_control.cancelled"
	CodeRuntimeControlFailed                     Code = "runtime_control.failed"
	CodeRuntimeControlSteerFailed                Code = "runtime_control.steer_failed"
	CodeRuntimeControlForbidden                  Code = "runtime_control.forbidden"
	CodeRuntimeControlRequestInvalid             Code = "runtime_control.request_invalid"
	CodeExternalRuntimeAuthRequired              Code = "external_runtime.auth_required"
	CodeExternalRuntimeUnavailable               Code = "external_runtime.unavailable"
	CodeExternalRuntimeSessionResumeFailed       Code = "external_runtime.session_resume_failed"
	CodeExternalRuntimeUsageLimited              Code = "external_runtime.usage_limited"
	CodeExternalRuntimeRateLimited               Code = "external_runtime.rate_limited"
	CodeExternalRuntimeContextWindowExceeded     Code = "external_runtime.context_window_exceeded"
	CodeExternalRuntimeOverloaded                Code = "external_runtime.overloaded"
	CodeExternalRuntimeUpstreamUnreachable       Code = "external_runtime.upstream_unreachable"
	CodeExternalRuntimeRequestBlocked            Code = "external_runtime.request_blocked"
	CodeToolApprovalForbidden                    Code = "tool_approval.forbidden"
	CodeToolApprovalNotFound                     Code = "tool_approval.not_found"
	CodeToolApprovalExpired                      Code = "tool_approval.expired"
	CodeToolApprovalAmbiguous                    Code = "tool_approval.ambiguous"
	CodeToolApprovalRequestInvalid               Code = "tool_approval.request_invalid"
	CodeToolApprovalOperationFailed              Code = "tool_approval.operation_failed"
	CodeUserInputForbidden                       Code = "user_input.forbidden"
	CodeUserInputExpired                         Code = "user_input.expired"
	CodeUserInputOperationFailed                 Code = "user_input.operation_failed"
	CodeSessionModelPreferenceConflict           Code = "session.model_preference_conflict"
	CodeSessionBusy                              Code = "session_runtime.session_busy"
	CodeSessionInvocationConflict                Code = "session_runtime.invocation_conflict"
	CodeSessionHistoryInconsistent               Code = "session_runtime.history_inconsistent"
	CodeSessionTurnNotLatest                     Code = "session_runtime.turn_not_latest"
	CodeSessionTurnIncomplete                    Code = "session_runtime.turn_incomplete"
	CodeSessionTurnNotFound                      Code = "session_runtime.turn_not_found"
	CodeSessionResetUnavailable                  Code = "session_runtime.reset_unavailable"
	CodeSessionResetConflict                     Code = "session_runtime.reset_conflict"
	CodeHistoryDeleteFailed                      Code = "history.delete_failed"
	CodeHookUserMessageFailed                    Code = "hook.user_message_failed"
	CodeSessionPublishFailed                     Code = "session_runtime.publish_failed"
	CodeSessionAbortFailed                       Code = "session_runtime.abort_failed"
	CodeAgentResponseTimeout                     Code = "agent.response_timeout"
	CodeSessionInterrupted                       Code = "session_runtime.interrupted"
	CodeAgentToolTimeout                         Code = "agent.tool_timeout"
	CodeVideoJobOutcomeUnknown                   Code = "video.job_outcome_unknown"
	CodeScheduleExecutionTimeout                 Code = "schedule.execution_timeout"
	CodeScheduleRunTargetConflict                Code = "schedule.run_target_conflict"
	CodeScheduleModelConflict                    Code = "schedule.model_conflict"
	CodeScheduleModelUnusable                    Code = "schedule.model_unusable"
	CodeScheduleModelRequired                    Code = "schedule.model_required"
	CodeScheduleSessionModeUnsupported           Code = "schedule.session_mode_unsupported"
	CodeAgentResponseInterrupted                 Code = "agent.response_interrupted"
	CodeAgentProviderOverloaded                  Code = "agent.provider_overloaded"
	CodeAgentProviderRateLimited                 Code = "agent.provider_rate_limited"
	CodeAgentProviderQuotaExhausted              Code = "agent.provider_quota_exhausted"
	CodeAgentProviderAuthFailed                  Code = "agent.provider_auth_failed"
	CodeAgentProviderPermissionDenied            Code = "agent.provider_permission_denied"
	CodeAgentProviderRequestRejected             Code = "agent.provider_request_rejected"
	CodeAgentProviderUnreachable                 Code = "agent.provider_unreachable"
	CodeQueueNoActiveRun                         Code = "queue_no_active_run"
	CodeQueueAdmissionOverloaded                 Code = "queue_admission_overloaded"
	CodeQueueAdmissionUnavailable                Code = "queue_admission_unavailable"
	CodeQueueRequestInvalid                      Code = "queue_request_invalid"
	CodeQueueItemNotPending                      Code = "queue_item_not_pending"
	CodeQueueItemNotEditable                     Code = "queue_item_not_editable"

	// Runtime notices: degradations an external agent runtime reports into the
	// conversation (event.RuntimeNotice). Clients localize them from errors.*.
	CodeRuntimeNativeHistoryLost   Code = "native_history_lost"
	CodeRuntimeToolsUnavailable    Code = "tools_unavailable"
	CodeRuntimeElicitationDeclined Code = "elicitation_declined"
	CodeQueueCapacityExceeded      Code = "queue_capacity_exceeded"
	CodeQueueSteerUnsupported      Code = "queue.steer_unsupported"

	CodeContextLifecycleRequestInvalid         Code = "context_lifecycle.request_invalid"
	CodeContextLifecycleAuthenticationRequired Code = "context_lifecycle.authentication_required"
	CodeContextLifecycleAccessDenied           Code = "context_lifecycle.access_denied"
	CodeContextLifecycleNotFound               Code = "context_lifecycle.not_found"
	CodeContextLifecycleLoadFailed             Code = "context_lifecycle.load_failed"
	CodeAgentAuthorizationExpired              Code = "agent_authorization.expired"
	CodeAgentAuthorizationNotReady             Code = "agent_authorization.not_ready"
	CodeAgentAuthorizationLimit                Code = "agent_authorization.limit"
	CodeAgentAuthorizationFailed               Code = "agent_authorization.failed"
	CodeAgentAuthorizationCodeInvalid          Code = "agent_authorization.code_invalid"
	CodeAgentCredentialNotFound                Code = "agent_credential.not_found"                //nolint:gosec // Stable public error code.
	CodeAgentCredentialRequestInvalid          Code = "agent_credential.request_invalid"          //nolint:gosec // Stable public error code.
	CodeAgentCredentialForbidden               Code = "agent_credential.forbidden"                //nolint:gosec // Stable public error code.
	CodeAgentCredentialIncompatible            Code = "agent_credential.incompatible"             //nolint:gosec // Stable public error code.
	CodeAgentCredentialRevoked                 Code = "agent_credential.revoked"                  //nolint:gosec // Stable public error code.
	CodeAgentCredentialReauthRequired          Code = "agent_credential.reauthorization_required" //nolint:gosec // Stable public error code.
	CodeAgentCredentialEncryptionUnavailable   Code = "agent_credential.encryption_unavailable"   //nolint:gosec // Stable public error code.
	CodeAgentCredentialRuntimeBusy             Code = "agent_credential.runtime_busy"             //nolint:gosec // Stable public error code.
	CodeAgentCredentialMaterializationFailed   Code = "agent_credential.materialization_failed"   //nolint:gosec // Stable public error code.
	CodeAgentCredentialUsageAuthExpired        Code = "agent_credential.usage_auth_expired"       //nolint:gosec // Stable public error code.
	CodeAgentCredentialUsageUnavailable        Code = "agent_credential.usage_unavailable"        //nolint:gosec // Stable public error code.

	// Boundary codes for failures that carry no public error of their own:
	// an unclassified failure and a request the caller canceled.
	CodeInternal Code = "internal"
	CodeCanceled Code = "canceled"

	// Framework codes: the HTTP boundary answers a transport-level
	// *echo.HTTPError (routing, method, body limit, binding) with the code for
	// its status.
	CodeHTTPBadRequest           Code = "http.bad_request"
	CodeHTTPUnauthorized         Code = "http.unauthorized"
	CodeHTTPForbidden            Code = "http.forbidden"
	CodeHTTPNotFound             Code = "http.not_found"
	CodeHTTPMethodNotAllowed     Code = "http.method_not_allowed"
	CodeHTTPConflict             Code = "http.conflict"
	CodeHTTPPayloadTooLarge      Code = "http.payload_too_large"
	CodeHTTPUnsupportedMediaType Code = "http.unsupported_media_type"
	CodeHTTPUpgradeRequired      Code = "http.upgrade_required"
	CodeHTTPTooManyRequests      Code = "http.too_many_requests"
	CodeHTTPNotImplemented       Code = "http.not_implemented"
	CodeHTTPBadGateway           Code = "http.bad_gateway"
	CodeHTTPServiceUnavailable   Code = "http.service_unavailable"
	CodeHTTPGatewayTimeout       Code = "http.gateway_timeout"

	// Request field codes: a request that lacks a field or carries an invalid
	// value in one, named in the field arg as the request names it.
	CodeRequestFieldRequired Code = "request.field_required"
	CodeRequestFieldInvalid  Code = "request.field_invalid"

	CodeSessionNotFound Code = "session.not_found"

	// MCP connection codes, and the OAuth state code that MCP shares with
	// provider sign-in.
	CodeMCPEndpointInvalid       Code = "mcp.endpoint_invalid"
	CodeMCPNameTaken             Code = "mcp.name_taken"
	CodeMCPOAuthDiscoveryFailed  Code = "mcp.oauth_discovery_failed"
	CodeMCPOAuthNotDiscovered    Code = "mcp.oauth_not_discovered"
	CodeMCPOAuthClientIDRequired Code = "mcp.oauth_client_id_required"
	CodeOAuthStateInvalid        Code = "oauth.state_invalid"

	// External Agent codes. They keep the values the removed agent feedback
	// protocol published and history rows still store.
	CodeACPAgentNotFound            Code = "acp_agent_not_found"
	CodeACPAgentNotEnabled          Code = "acp_agent_not_enabled"
	CodeACPAgentNotConfigured       Code = "acp_agent_not_configured"
	CodeCodexOAuthIncomplete        Code = "codex_oauth_incomplete"
	CodeCodexAuthTokenMissing       Code = "codex_auth_token_missing" //nolint:gosec // Stable public error code.
	CodeACPAgentAuthInvalid         Code = "acp_agent_auth_invalid"
	CodeNoWorkspaceExec             Code = "no_workspace_exec"
	CodeACPRuntimeOwnerMissing      Code = "acp_runtime_owner_missing"
	CodeACPDiscussUnsupported       Code = "acp_discuss_unsupported"
	CodeGroupChatACPUnsupported     Code = "group_chat_acp_unsupported"
	CodeACPProjectModeInvalid       Code = "acp_project_mode_invalid"
	CodeACPProjectPathInvalid       Code = "acp_project_path_invalid"
	CodeACPDisplayArgsInvalid       Code = "acp_display_args_invalid"
	CodeACPRuntimeStartFailed       Code = "acp_runtime_start_failed"
	CodeACPRuntimeBusy              Code = "acp_runtime_busy"
	CodeACPAttachmentInvalid        Code = "acp_attachment_invalid"
	CodeACPAttachmentUnavailable    Code = "acp_attachment_unavailable"
	CodeRuntimeAgentCommandStale    Code = "runtime_agent_command_stale"
	CodeACPImageInputUnsupported    Code = "acp_image_input_unsupported"
	CodeInvalidChatRuntime          Code = "invalid_chat_runtime"
	CodeAgentDependencyMissing      Code = "agent_dependency_missing"
	CodeExternalAgentAccountUnbound Code = "external_agent.account_unbound"
	// A direct runtime (Codex, Claude Code) was asked to start in a workspace
	// that is not a container.
	CodeExternalAgentContainerWorkspaceRequired Code = "external_agent.container_workspace_required"

	// Run error codes persisted in session_runs.error_code and history
	// metadata. The writers keep their own constants; these register the same
	// values.
	CodeRuntimeRunFailed             Code = "runtime_run_failed"
	CodeRuntimePromptFailed          Code = "runtime_prompt_failed"
	CodeRuntimeOwnerLeaseExpired     Code = "runtime_owner_lease_expired"
	CodeRuntimeLiveBackendLost       Code = "runtime_live_backend_lost"
	CodeRuntimeAdmissionOrphaned     Code = "runtime_admission_orphaned"
	CodeRuntimeFenceActivationFailed Code = "runtime_fence_activation_failed"
	CodeRuntimeReservationFailed     Code = "runtime_reservation_failed"
	CodeRuntimeReservationDeclined   Code = "runtime_reservation_declined"
	CodeHistoryReset                 Code = "history_reset"

	// Channel queue command codes (internal/channel/inbound/queue_command.go).
	CodeQueueInvocationConflict         Code = "queue_invocation_conflict"
	CodeQueueUnsupportedSession         Code = "queue_unsupported_session"
	CodeQueueFollowUpUnsupportedChannel Code = "queue_follow_up_unsupported_channel"
	// Reasons recorded on a rejected live queue item, registered under the
	// value the queue state stores (internal/agent/runtime/session/live_queue.go).
	CodeQueueTargetRunNotActive     Code = "queue_target_run_not_active"
	CodeQueueFollowUpCommandInvalid Code = "queue_follow_up_command_invalid"

	// Slash request refusals (internal/slash), on the Web composer and in IM
	// channels alike.
	CodeSlashAttachmentsUnsupported     Code = "slash.attachments_unsupported"
	CodeSlashPermissionDenied           Code = "slash.permission_denied"
	CodeSlashRequiresWebSocket          Code = "slash.requires_websocket"
	CodeSlashReservedMetadata           Code = "slash.reserved_metadata"
	CodeSlashSkillActivationUnsupported Code = "slash.skill_activation_unsupported"
	CodeSlashSkillAmbiguous             Code = "slash.skill_ambiguous"
	CodeSlashSkillContextTooLarge       Code = "slash.skill_context_too_large"
	CodeSlashSkillDisabled              Code = "slash.skill_disabled"
	CodeSlashSkillNotFound              Code = "slash.skill_not_found"
	CodeSlashSkillNotUsable             Code = "slash.skill_not_usable"
	CodeSlashSkillSyntaxInvalid         Code = "slash.skill_syntax_invalid"
	CodeSlashTooManySkills              Code = "slash.too_many_skills"
	CodeSlashUnknownCommand             Code = "slash.unknown_command"
	CodeSlashUnsupportedInWeb           Code = "slash.unsupported_in_web"

	CodeMemoryCompactUnsupported Code = "memory.compact_unsupported"

	// Bot and workspace codes published by the workspace HTTP handlers and the
	// bot creation, display and dependency event streams.
	CodeBotReadyUpdateFailed                    Code = "bot_ready_update_failed"
	CodeWorkspaceSetupTimeout                   Code = "workspace_setup_timeout"
	CodeWorkspaceSetupFailed                    Code = "workspace_setup_failed"
	CodeWorkspaceDisplayDisabled                Code = "workspace_display_disabled"
	CodeWorkspaceDependencyOperationUnknown     Code = "workspace_dependency_operation_unknown"
	CodeWorkspaceCreateRequestInvalid           Code = "workspace_create_request_invalid"
	CodeWorkspaceCreateFailed                   Code = "workspace_create_failed"
	CodeWorkspaceNotFound                       Code = "workspace_not_found"
	CodeWorkspaceLoadFailed                     Code = "workspace_load_failed"
	CodeWorkspaceMetricsLoadFailed              Code = "workspace_metrics_load_failed"
	CodeWorkspaceResourceLimitsInvalid          Code = "workspace_resource_limits_invalid"
	CodeWorkspaceResourceLimitsRequired         Code = "workspace_resource_limits_required"
	CodeWorkspaceResourceLimitsSaveFailed       Code = "workspace_resource_limits_save_failed"
	CodeWorkspaceDeleteFailed                   Code = "workspace_delete_failed"
	CodeWorkspaceStartFailed                    Code = "workspace_start_failed"
	CodeWorkspaceStopFailed                     Code = "workspace_stop_failed"
	CodeWorkspaceSnapshotsUnsupported           Code = "workspace_snapshots_unsupported"
	CodeWorkspaceSnapshotManagerUnavailable     Code = "workspace_snapshot_manager_unavailable"
	CodeWorkspaceSnapshotRequestInvalid         Code = "workspace_snapshot_request_invalid"
	CodeWorkspaceSnapshotCreateFailed           Code = "workspace_snapshot_create_failed"
	CodeWorkspaceSnapshotsLoadFailed            Code = "workspace_snapshots_load_failed"
	CodeWorkspaceSnapshotterMismatch            Code = "workspace_snapshotter_mismatch"
	CodeWorkspaceSnapshotChainNotFound          Code = "workspace_snapshot_chain_not_found"
	CodeWorkspaceManagerUnavailable             Code = "workspace_manager_unavailable"
	CodeWorkspaceSnapshotRollbackRequestInvalid Code = "workspace_snapshot_rollback_request_invalid"
	CodeWorkspaceSnapshotVersionInvalid         Code = "workspace_snapshot_version_invalid"
	CodeWorkspaceSnapshotRollbackFailed         Code = "workspace_snapshot_rollback_failed"
	CodeWorkspacePreservedDataNotFound          Code = "workspace_preserved_data_not_found"
	CodeWorkspaceRestoreFailed                  Code = "workspace_restore_failed"
)

// Fault is who a failure is attributed to. The values are the fault values of
// the error contract: the fault field of a Problem and the fault metadata of
// an RPC ErrorInfo. A catalog entry may declare client, server or dependency;
// canceled is attributed at a boundary from the caller's context and is never
// declared.
type Fault string

const (
	// FaultClient means the caller's request was refused by this process's
	// rules; the caller must change the request.
	FaultClient Fault = "client"
	// FaultServer means this process failed: its code, data or configuration,
	// including a bad request this process sent downstream.
	FaultServer Fault = "server"
	// FaultDependency means a service outside this process failed or refused
	// the call, such as an LLM provider, an external agent runtime, an
	// internal downstream service or the network.
	FaultDependency Fault = "dependency"
	// FaultCanceled means the caller canceled, or the caller's deadline passed.
	FaultCanceled Fault = "canceled"
)

// ParseFault reads a fault value received as a string, such as the fault
// metadata of an RPC ErrorInfo. It reports false for any other string.
func ParseFault(s string) (Fault, bool) {
	f := Fault(s)
	switch f {
	case FaultClient, FaultServer, FaultDependency, FaultCanceled:
		return f, true
	default:
		return "", false
	}
}

// Definition is the single catalog entry for a public error contract.
// Type URIs and frontend i18n keys are derived mechanically from Code.
type Definition struct {
	HTTPStatus  int
	Detail      string
	AllowedArgs []string
	// Fault is the attribution of the code when the status alone would give
	// the wrong one, as for a provider's 429 or 502. Empty leaves it to the
	// error chain: a 4xx status is a client fault, and a 5xx status is this
	// process's fault unless its cause is marked as a dependency's.
	Fault Fault
}

// codesync(error-catalog): Detail strings double as the no-locale fallback for
// clients; the localized copies live under errors.* in
// apps/web/src/i18n/locales/{en,zh,ja}.json. Keep both sides in sync.
var catalog = map[Code]Definition{
	CodeAgentAuthorizationExpired:     {HTTPStatus: http.StatusGone, Detail: "This authorization has expired. Connect your account again."},
	CodeAgentAuthorizationNotReady:    {HTTPStatus: http.StatusConflict, Detail: "Finish connecting your account before creating the Bot."},
	CodeAgentAuthorizationLimit:       {HTTPStatus: http.StatusTooManyRequests, Detail: "Close other pending authorizations and try again."},
	CodeAgentAuthorizationFailed:      {HTTPStatus: http.StatusServiceUnavailable, Detail: "Account authorization failed. Please try again."},
	CodeAgentAuthorizationCodeInvalid: {HTTPStatus: http.StatusBadRequest, Detail: "The authorization code is invalid or expired. Copy the complete code from Claude or connect again."},
	CodeAgentCredentialNotFound: {
		HTTPStatus: http.StatusNotFound,
		Detail:     "The Agent credential was not found.",
	},
	CodeAgentCredentialRequestInvalid: {
		HTTPStatus: http.StatusBadRequest,
		Detail:     "The Agent credential request is invalid.",
	},
	CodeAgentCredentialForbidden: {
		HTTPStatus: http.StatusForbidden,
		Detail:     "You cannot use this Agent credential.",
	},
	CodeAgentCredentialIncompatible: {
		HTTPStatus: http.StatusUnprocessableEntity,
		Detail:     "This credential is not compatible with the selected Agent.",
	},
	CodeAgentCredentialRevoked: {
		HTTPStatus: http.StatusConflict,
		Detail:     "This Agent credential has been revoked.",
	},
	CodeAgentCredentialReauthRequired: {
		HTTPStatus: http.StatusConflict,
		Detail:     "This Agent credential needs to be connected again.",
	},
	CodeAgentCredentialEncryptionUnavailable: {
		HTTPStatus: http.StatusServiceUnavailable,
		Detail:     "Agent credential storage is not configured on this server.",
	},
	CodeAgentCredentialRuntimeBusy: {
		HTTPStatus: http.StatusConflict,
		Detail:     "The Agent credential cannot be changed while the Agent is running.",
	},
	CodeAgentCredentialMaterializationFailed: {
		HTTPStatus: http.StatusInternalServerError,
		Detail:     "The Agent credential could not be prepared for this runtime.",
	},
	CodeAgentCredentialUsageAuthExpired: {
		HTTPStatus: http.StatusConflict,
		Detail:     "The account sign-in has expired. It renews after the next conversation; if usage still cannot load, connect the account again.",
	},
	CodeAgentCredentialUsageUnavailable: {
		HTTPStatus: http.StatusBadGateway,
		Detail:     "Usage limits could not be loaded right now. Try again later.",
	},
	CodeBotNameTaken: {
		HTTPStatus:  http.StatusConflict,
		Detail:      "This name is already taken.",
		AllowedArgs: []string{"field"},
	},
	CodeBotNameInvalid: {
		HTTPStatus: http.StatusBadRequest,
		Detail:     "The bot name is invalid or reserved.",
	},
	CodeBotAgentNotFound: {
		HTTPStatus: http.StatusNotFound,
		Detail:     "This Agent is no longer available.",
	},
	CodeBotAgentNameTaken: {
		HTTPStatus: http.StatusConflict,
		Detail:     "This Agent name is already taken.",
	},
	CodeBotAgentInvalidRuntime: {
		HTTPStatus: http.StatusBadRequest,
		Detail:     "The selected Agent runtime is not supported.",
	},
	CodeBotAgentInvalidMetadata: {
		HTTPStatus: http.StatusBadRequest,
		Detail:     "The Agent configuration is invalid.",
	},
	CodeBotAgentDefaultInUse: {
		HTTPStatus: http.StatusConflict,
		Detail:     "Choose another default Agent before disabling or deleting this one.",
	},
	CodeBotAgentUnavailable: {
		HTTPStatus:  http.StatusConflict,
		Detail:      "This Agent is disabled or not configured.",
		AllowedArgs: []string{"field"},
	},
	CodeBotAgentProviderDirectRuntime: {
		HTTPStatus:  http.StatusBadRequest,
		Detail:      "This provider runs as a direct runtime. Create the Agent with the codex or claude-code runtime instead.",
		AllowedArgs: []string{"runtime"},
	},
	CodeChannelRuntimeUnavailable: {
		HTTPStatus: http.StatusServiceUnavailable,
		Detail:     "The channel service could not be reached.",
	},
	CodeChannelVerificationFailed: {
		HTTPStatus: http.StatusBadGateway,
		Detail:     "The channel configuration could not be verified. Check the credentials, then try again.",
	},
	CodeChannelEnableFailed: {
		HTTPStatus: http.StatusBadRequest,
		Detail:     "The channel could not be enabled.",
	},
	CodeChannelWebhookEndpointInvalid: {
		HTTPStatus: http.StatusBadRequest,
		Detail:     "The webhook endpoint is invalid.",
	},
	CodeChannelBindingRequired: {
		HTTPStatus: http.StatusConflict,
		Detail:     "The recipient is not bound to this channel.",
	},
	CodeNetworkProviderNotConfigured: {
		HTTPStatus: http.StatusConflict,
		Detail:     "No network provider is configured for this bot.",
	},
	CodeUserCannotRemoveSelf: {
		HTTPStatus: http.StatusBadRequest,
		Detail:     "You cannot remove yourself.",
	},
	CodeCompactionModelUnavailable: {
		HTTPStatus:  http.StatusBadRequest,
		Detail:      "The compaction model is unavailable.",
		AllowedArgs: []string{"reason"},
	},
	CodeSettingsReasoningEffortInvalid: {
		HTTPStatus:  http.StatusBadRequest,
		Detail:      "The selected reasoning level is not supported by the chat model.",
		AllowedArgs: []string{"effort"},
	},
	CodeSettingsReasoningUnavailable: {
		HTTPStatus: http.StatusServiceUnavailable,
		Detail:     "The chat model's reasoning options could not be resolved. Please try again.",
	},
	CodeContextBudgetUnsatisfied: {
		HTTPStatus: http.StatusUnprocessableEntity,
		Detail:     "The model context window is too small for this request. Run /compact to summarize older history, shorten the request, or switch to a model with a larger context window.",
	},
	CodeContextProtectedOverflow: {
		HTTPStatus: http.StatusUnprocessableEntity,
		Detail:     "Required context exceeds the model context budget. Run /compact to summarize older history, or switch to a model with a larger context window.",
	},
	CodeWorkspaceUnreachable: {
		HTTPStatus: http.StatusServiceUnavailable,
		Detail:     "The workspace could not be reached.",
	},
	CodeWorkspaceTemplateBootstrapFailed: {
		HTTPStatus: http.StatusInternalServerError,
		Detail:     "The workspace files could not be initialized.",
	},
	// Distinct from workspace.unreachable: preparation started but broke
	// mid-flight, so "could not be reached" would mislead the user.
	CodeWorkspaceDisplayPrepareFailed: {
		HTTPStatus: http.StatusInternalServerError,
		Detail:     "Display preparation failed.",
	},
	// Workspace dependencies (design docs/design/workspace-dependencies.md
	// §11). The 409 family tells the UI what to offer instead: start or
	// create the workspace, wait for the other operation, bring the remote
	// computer online.
	CodeWorkspaceDependencyDiscoveryFailed:       {HTTPStatus: http.StatusServiceUnavailable, Detail: "Workspace dependencies could not be inspected. Please try again."},
	CodeWorkspaceDependencyCatalogUnavailable:    {HTTPStatus: http.StatusServiceUnavailable, Detail: "The dependency catalog is unavailable. Please try again later."},
	CodeWorkspaceDependencyDefinitionInvalid:     {HTTPStatus: http.StatusBadGateway, Detail: "The dependency definition could not be verified. Please refresh the catalog."},
	CodeWorkspaceDependencyDefinitionUnavailable: {HTTPStatus: http.StatusServiceUnavailable, Detail: "The dependency definition is not cached. Please reconnect to Supermarket and try again."},
	CodeWorkspaceDependencyNotFound: {
		HTTPStatus: http.StatusNotFound,
		Detail:     "This dependency does not exist.",
	},
	CodeWorkspaceDependencyRequestInvalid: {
		HTTPStatus: http.StatusBadRequest,
		Detail:     "The dependency request is invalid.",
	},
	CodeWorkspaceDependencyActionUnsupported: {
		HTTPStatus: http.StatusUnprocessableEntity,
		Detail:     "This action is not available for the dependency.",
	},
	CodeWorkspaceDependencyPlatformUnsupported: {
		HTTPStatus: http.StatusUnprocessableEntity,
		Detail:     "This dependency is not available on the workspace platform.",
	},
	CodeWorkspaceDependencyBusy: {
		HTTPStatus: http.StatusConflict,
		Detail:     "Another operation on this dependency is in progress.",
	},
	CodeWorkspaceDependencyRequired: {
		HTTPStatus:  http.StatusConflict,
		Detail:      "Other installed dependencies still need this dependency. Remove them first.",
		AllowedArgs: []string{"dependents"},
	},
	CodeWorkspaceDependencyPrerequisitesChanged: {
		HTTPStatus: http.StatusConflict,
		Detail:     "The dependencies this one needs changed after you confirmed. Review the operation again.",
	},
	CodeWorkspaceDependencyWorkspaceNotRunning: {
		HTTPStatus: http.StatusConflict,
		Detail:     "The workspace is not running. Start it first.",
	},
	CodeWorkspaceDependencyWorkspaceMissing: {
		HTTPStatus: http.StatusConflict,
		Detail:     "The workspace has not been created yet.",
	},
	CodeWorkspaceDependencyRollbackUnavailable: {
		HTTPStatus: http.StatusConflict,
		Detail:     "No previous version to roll back to.",
	},
	CodeWorkspaceDependencyOperationFailed: {
		HTTPStatus: http.StatusInternalServerError,
		Detail:     "The dependency operation failed.",
	},
	// A Server that stopped, or a workspace that went away, cut the
	// operation short: its outcome is unknown and the log is gone, so the
	// user retries rather than reading a diagnosis that no longer exists.
	CodeWorkspaceDependencyOperationInterrupted: {
		HTTPStatus: http.StatusInternalServerError,
		Detail:     "The operation was interrupted before it finished. Try it again.",
	},
	CodeProviderTemplateNotFound: {
		HTTPStatus: http.StatusNotFound,
		Detail:     "The provider template was not found.",
	},
	CodeProviderTemplateDomainInvalid: {
		HTTPStatus: http.StatusBadRequest,
		Detail:     "The provider template domain is invalid.",
	},
	CodeProviderTemplateDomainMismatch: {
		HTTPStatus: http.StatusBadRequest,
		Detail:     "The provider template cannot be used for this provider type.",
	},
	CodeProviderTemplateOperationFailed: {
		HTTPStatus: http.StatusInternalServerError,
		Detail:     "The provider template operation failed.",
	},
	CodeProviderNameTaken: {
		HTTPStatus: http.StatusConflict,
		Detail:     "This provider name is already taken.",
	},
	CodeProviderTemplateRequestInvalid: {
		HTTPStatus: http.StatusBadRequest,
		Detail:     "The provider template request is invalid.",
	},
	CodeSearchProviderTypeConflict: {
		HTTPStatus: http.StatusConflict,
		Detail:     "This web search provider is already configured.",
	},
	CodeSearchProviderInvalidProvider: {
		HTTPStatus: http.StatusBadRequest,
		Detail:     "This web search provider is not supported.",
	},
	CodeConnectorRequestInvalid: {
		HTTPStatus: http.StatusBadRequest,
		Detail:     "The connector request is invalid.",
	},
	CodeConnectorNotConfigured: {
		HTTPStatus: http.StatusServiceUnavailable,
		Detail:     "Connectors are not configured on this server.",
	},
	CodeConnectorNotFound: {
		HTTPStatus: http.StatusNotFound,
		Detail:     "This connector is no longer available.",
	},
	CodeConnectorConflict: {
		HTTPStatus: http.StatusConflict,
		Detail:     "This connector conflicts with an existing connection.",
	},
	CodeConnectorRequestRejected: {
		HTTPStatus: http.StatusBadRequest,
		Detail:     "Connect-It rejected the connector request.",
	},
	CodeConnectorUpstreamUnavailable: {
		HTTPStatus: http.StatusBadGateway,
		Detail:     "Connect-It could not complete the request. Please try again shortly.",
	},
	CodeConnectorOperationFailed: {
		HTTPStatus: http.StatusInternalServerError,
		Detail:     "The connector operation failed. Please try again.",
	},
	CodeSkillBuiltinReadOnly: {
		HTTPStatus: http.StatusConflict,
		Detail:     "Built-in Skills are managed by Memoh and cannot be edited or deleted.",
	},
	CodeSkillNameTaken: {
		HTTPStatus: http.StatusConflict,
		Detail:     "A Skill with this name already exists.",
	},
	CodeSkillSaveFailed: {
		HTTPStatus: http.StatusInternalServerError,
		Detail:     "The Skill could not be saved.",
	},
	CodeSkillRegistryReadOnly: {
		HTTPStatus: http.StatusConflict,
		Detail:     "Registry App Skills are managed by their App and cannot be changed directly.",
	},
	CodeSkillNameInvalid: {
		HTTPStatus: http.StatusBadRequest,
		Detail:     "The Skill needs a valid name in its YAML frontmatter.",
	},
	CodeSkillNotFound: {
		HTTPStatus: http.StatusNotFound,
		Detail:     "This Skill was not found. Refresh the list and try again.",
	},
	CodeTTSTextTooLong: {
		HTTPStatus:  http.StatusBadRequest,
		Detail:      "The text is too long to synthesize.",
		AllowedArgs: []string{"max"},
	},
	CodeTTSModelNotConfigured: {
		HTTPStatus: http.StatusConflict,
		Detail:     "This bot has no text-to-speech model configured.",
	},
	CodeWorkspaceArchiveInvalid: {
		HTTPStatus: http.StatusBadRequest,
		Detail:     "The archive cannot be extracted.",
	},
	CodeBotBackupBundleInvalid: {
		HTTPStatus: http.StatusBadRequest,
		Detail:     "The backup file is not valid.",
	},
	CodeFetchProviderNativeManaged: {
		HTTPStatus: http.StatusConflict,
		Detail:     "The built-in fetch provider is managed by Memoh.",
	},
	CodeAppNotFound: {
		HTTPStatus: http.StatusNotFound,
		Detail:     "This App installation was not found.",
	},
	CodeAppRequestInvalid: {
		HTTPStatus: http.StatusBadRequest,
		Detail:     "The App request is invalid.",
	},
	CodeAppBusy: {
		HTTPStatus: http.StatusConflict,
		Detail:     "Another operation on this App is in progress.",
	},
	CodeAppOperationFailed: {
		HTTPStatus: http.StatusInternalServerError,
		Detail:     "The App operation failed.",
	},
	CodeAppDependenciesUnavailable: {
		HTTPStatus: http.StatusServiceUnavailable,
		Detail:     "Workspace dependencies are unavailable on this server, so the App cannot manage them.",
	},
	CodeAppPrerequisiteFailed: {
		HTTPStatus: http.StatusConflict,
		Detail:     "A dependency this one needs failed to install. Fix that dependency first, then retry.",
	},
	CodeRegistryUnavailable: {
		HTTPStatus: http.StatusBadGateway,
		Detail:     "The Supermarket is unavailable.",
	},
	CodeRegistryAppNotFound: {
		HTTPStatus: http.StatusNotFound,
		Detail:     "The App was not found.",
	},
	CodeRegistryAppInvalid: {
		HTTPStatus: http.StatusBadGateway,
		Detail:     "The App is invalid.",
	},
	CodeRegistryAppInstallFailed: {
		HTTPStatus: http.StatusInternalServerError,
		Detail:     "The App could not be installed.",
	},
	CodeProfileTitleModelInvalid: {
		HTTPStatus: http.StatusBadRequest,
		Detail:     "The selected title model is unavailable or is not a chat model.",
	},
	CodeProfileRequestInvalid: {
		HTTPStatus: http.StatusBadRequest,
		Detail:     "The profile update request is invalid.",
	},
	CodeProfileUpdateFailed: {
		HTTPStatus: http.StatusInternalServerError,
		Detail:     "The profile could not be updated.",
	},
	CodeACPRequestInvalid: {
		HTTPStatus: http.StatusBadRequest,
		Detail:     "The Agent runtime request is invalid. Check the request and try again.",
	},
	CodeACPAccessForbidden: {
		HTTPStatus: http.StatusForbidden,
		Detail:     "You do not have permission to control this Agent runtime.",
	},
	CodeACPRuntimeNotFound: {
		HTTPStatus: http.StatusNotFound,
		Detail:     "The ACP runtime is no longer available.",
	},
	CodeACPRuntimeConflict: {
		HTTPStatus: http.StatusConflict,
		Detail:     "The Agent runtime is not ready for this operation. Refresh and try again.",
	},
	CodeACPRuntimeLimitReached: {
		HTTPStatus: http.StatusTooManyRequests,
		Detail:     "Too many Agent runtimes are active. Close one and try again.",
	},
	CodeACPOperationFailed: {
		HTTPStatus: http.StatusInternalServerError,
		Detail:     "The Agent runtime operation failed. Please try again.",
	},
	CodeACPCommandNotFound: {
		HTTPStatus:  http.StatusConflict,
		Detail:      "The Agent command was not found in the workspace. Install it from the workspace terminal, or set the Agent command to an absolute path.",
		AllowedArgs: []string{"command"},
	},
	CodeExternalAgentTurnReplacementUnsupported: {
		HTTPStatus: http.StatusBadRequest,
		Detail:     "Retry and edit are unavailable for external agent sessions. Send a new message instead.",
	},
	CodeRuntimeControlUnsupported:             {HTTPStatus: http.StatusBadRequest, Detail: "This runtime does not support this control."},
	CodeRuntimeControlCommandUnavailable:      {HTTPStatus: http.StatusConflict, Detail: "This runtime command is no longer available. Refresh and try again."},
	CodeRuntimeControlModeUnavailable:         {HTTPStatus: http.StatusBadRequest, Detail: "This mode is unavailable. Refresh and choose a supported mode."},
	CodeRuntimeControlGoalRequiresDefaultMode: {HTTPStatus: http.StatusConflict, Detail: "Exit Plan mode before creating or resuming a goal."},
	CodeWorkdirGitBusy:                        {HTTPStatus: http.StatusConflict, Detail: "An agent is using this working directory. Wait for it to finish before switching branches."},
	CodeWorkdirGitBranchUnavailable:           {HTTPStatus: http.StatusBadRequest, Detail: "This local branch is unavailable. Refresh and select an existing branch."},
	CodeWorkdirGitSwitchFailed:                {HTTPStatus: http.StatusConflict, Detail: "Git could not switch branches. Check uncommitted changes and whether the branch is used by another worktree."},
	CodeWorkdirGitUnavailable:                 {HTTPStatus: http.StatusInternalServerError, Detail: "The Git working directory could not be read. Check the workspace and try again."},
	CodeWorkdirRemoteUnsupportedForAgent:      {HTTPStatus: http.StatusBadRequest, Detail: "External agent sessions cannot use a remote computer. Choose a workdir on the native workspace instead."},
	CodeChatMessageEmpty:                      {HTTPStatus: http.StatusBadRequest, Detail: "Enter a message or attach a file."},
	CodeRuntimeControlThreadUnavailable:       {HTTPStatus: http.StatusConflict, Detail: "Start a conversation before using this operation."},
	CodeRuntimeControlCancelled:               {HTTPStatus: http.StatusConflict, Detail: "The runtime operation was cancelled."},
	CodeRuntimeControlSteerFailed:             {HTTPStatus: http.StatusConflict, Detail: "The additional instruction could not be delivered. Send it again after this turn finishes."},
	CodeRuntimeControlFailed:                  {HTTPStatus: http.StatusInternalServerError, Detail: "The runtime control could not be completed. Try again."},
	CodeRuntimeControlForbidden:               {HTTPStatus: http.StatusForbidden, Detail: "You do not have access to this conversation."},
	CodeRuntimeControlRequestInvalid:          {HTTPStatus: http.StatusBadRequest, Detail: "The runtime control request is invalid."},
	CodeExternalRuntimeAuthRequired: {
		HTTPStatus: http.StatusConflict,
		Detail:     "The External Agent runtime requires account authorization before it can be used.",
	},
	CodeExternalRuntimeUnavailable: {
		HTTPStatus: http.StatusServiceUnavailable,
		Detail:     "The external agent runtime for this session is not available on this server.",
	},
	// The request to resume the external agent's own session (a Codex thread)
	// failed before the agent could accept or refuse it, so a retry may still
	// resume it. The turn is treated as not run; the user retries or starts a
	// fresh conversation.
	CodeExternalRuntimeSessionResumeFailed: {
		HTTPStatus: http.StatusBadGateway,
		Detail:     "The session could not be resumed. Try again or start a new conversation.",
		Fault:      FaultDependency,
	},
	// The external agent's own account has no usage left: a plan allowance
	// that resets, a billing quota that does not, or a plan that does not
	// include the agent. The agent reports them as one condition, so the copy
	// covers both waiting and checking the plan.
	CodeExternalRuntimeUsageLimited: {
		HTTPStatus: http.StatusTooManyRequests,
		Detail:     "The external agent's account has no usage left. Try again after the limit resets, or check the account's plan and billing.",
		Fault:      FaultDependency,
	},
	CodeExternalRuntimeRateLimited: {
		HTTPStatus: http.StatusTooManyRequests,
		Detail:     "The external agent was rate limited by its model service. Please wait a moment before sending again.",
		Fault:      FaultDependency,
	},
	// Like the native runtime's context.* codes: nothing failed, the
	// conversation has to shrink before a turn can run.
	CodeExternalRuntimeContextWindowExceeded: {
		HTTPStatus: http.StatusUnprocessableEntity,
		Detail:     "This conversation no longer fits in the model's context window. Compact the context or start a new conversation.",
	},
	CodeExternalRuntimeOverloaded: {
		HTTPStatus: http.StatusServiceUnavailable,
		Detail:     "The external agent's model service is unavailable or overloaded right now. Try again in a moment, or switch to another model.",
		Fault:      FaultDependency,
	},
	CodeExternalRuntimeUpstreamUnreachable: {
		HTTPStatus: http.StatusBadGateway,
		Detail:     "The external agent could not reach its model service, or the connection dropped. Check the network and the agent's service address, then try again.",
		Fault:      FaultDependency,
	},
	// The model service refused the request under its own policy. Nothing
	// failed, and the same request is refused again, so this is the request's
	// fault rather than a dependency's.
	CodeExternalRuntimeRequestBlocked: {
		HTTPStatus: http.StatusUnprocessableEntity,
		Detail:     "The model service's safety policy blocked this request. Change the request and send it again.",
	},
	CodeACPModelSelectionUnsupported: {
		HTTPStatus: http.StatusBadRequest,
		Detail:     "This external agent does not support model selection.",
	},
	CodeACPModelIDRequired: {
		HTTPStatus: http.StatusBadRequest,
		Detail:     "Choose a model and try again.",
	},
	CodeACPModelUnavailable: {
		HTTPStatus: http.StatusBadRequest,
		Detail:     "The selected model is no longer available for this external agent.",
	},
	CodeACPReasoningUnsupported: {
		HTTPStatus: http.StatusBadRequest,
		Detail:     "This external agent does not support reasoning effort selection.",
	},
	CodeACPReasoningEffortRequired: {
		HTTPStatus: http.StatusBadRequest,
		Detail:     "Choose a reasoning effort and try again.",
	},
	CodeACPReasoningUnavailable: {
		HTTPStatus: http.StatusBadRequest,
		Detail:     "The selected reasoning effort is no longer available for this external agent.",
	},
	CodeACPModeSelectionUnsupported: {
		HTTPStatus: http.StatusBadRequest,
		Detail:     "This external agent does not support session mode selection.",
	},
	CodeACPModeIDRequired: {
		HTTPStatus: http.StatusBadRequest,
		Detail:     "Choose a session mode and try again.",
	},
	CodeACPModeUnavailable: {
		HTTPStatus: http.StatusBadRequest,
		Detail:     "The selected session mode is no longer available for this external agent.",
	},
	CodeACPConfigUpdateFailed: {
		HTTPStatus: http.StatusBadGateway,
		Detail:     "The external agent could not apply the selected settings. Please retry.",
		Fault:      FaultDependency,
	},
	CodeCapabilityAccessDenied:     {HTTPStatus: http.StatusForbidden, Detail: "You need Manage permission on this bot to manage its capabilities."},
	CodeCapabilityRequestInvalid:   {HTTPStatus: http.StatusBadRequest, Detail: "The capability request is invalid. Check the action and its parameters."},
	CodeCapabilityOperationFailed:  {HTTPStatus: http.StatusBadGateway, Detail: "The capability operation failed. Check its status before retrying."},
	CodeCapabilityApprovalRequired: {HTTPStatus: http.StatusForbidden, Detail: "This capability change requires an approved management request."},
	CodeCapabilityNotFound:         {HTTPStatus: http.StatusNotFound, Detail: "The requested capability could not be found."},
	CodeToolApprovalForbidden: {
		HTTPStatus: http.StatusForbidden,
		Detail:     "You do not have permission to answer this approval request.",
	},
	CodeToolApprovalNotFound: {
		HTTPStatus: http.StatusNotFound,
		Detail:     "This approval request could not be found.",
	},
	CodeToolApprovalExpired: {
		HTTPStatus: http.StatusConflict,
		Detail:     "This approval request has expired or was already answered.",
	},
	CodeToolApprovalAmbiguous: {
		HTTPStatus: http.StatusConflict,
		Detail:     "More than one approval request matches this response.",
	},
	CodeToolApprovalRequestInvalid: {
		HTTPStatus: http.StatusBadRequest,
		Detail:     "The approval response is invalid.",
	},
	CodeToolApprovalOperationFailed: {
		HTTPStatus: http.StatusInternalServerError,
		Detail:     "The approval response could not be processed.",
	},
	CodeUserInputForbidden: {
		HTTPStatus: http.StatusForbidden,
		Detail:     "You do not have permission to answer this question.",
	},
	CodeUserInputExpired: {
		HTTPStatus: http.StatusConflict,
		Detail:     "This question has expired or was already answered.",
	},
	CodeUserInputOperationFailed: {
		HTTPStatus: http.StatusInternalServerError,
		Detail:     "The answer could not be processed.",
	},
	// A session runs one turn at a time, so this is ordinary backpressure and
	// the same submission succeeds once the session frees up. It is the one
	// conflict in this catalog that a client should retry unchanged.
	CodeSessionModelPreferenceConflict: {HTTPStatus: http.StatusConflict, Detail: "The conversation model selection has changed. Refresh and try again."},
	CodeSessionBusy: {
		HTTPStatus: http.StatusConflict,
		Detail:     "This conversation is still working on the previous message. Please try again shortly.",
	},
	// Distinct from session_busy: retrying changes nothing, because the same
	// retry identity was already used for different input.
	CodeSessionInvocationConflict: {
		HTTPStatus: http.StatusConflict,
		Detail:     "This request was already submitted with different content.",
	},
	// history_inconsistent is reserved for server-side persistence faults:
	// a round could not be written, or its commit outcome is unknown. Client
	// state that has merely gone stale is reported by the 409 codes below.
	CodeSessionHistoryInconsistent: {
		HTTPStatus: http.StatusInternalServerError,
		Detail:     "The conversation could not be saved. Refresh and try again.",
	},
	// The user-message hook failed before the input was accepted by the agent.
	// The underlying hook/configuration error remains private; the stable code
	// lets the client preserve the failed input and retry through the hook.
	CodeHookUserMessageFailed: {
		HTTPStatus: http.StatusBadRequest,
		Detail:     "The message could not be accepted by the message hook. Check the Hook configuration and try again.",
	},
	// The client named a turn that is no longer the latest visible turn (or
	// was never persisted). Reloading the conversation resolves it.
	CodeSessionTurnNotFound: {
		HTTPStatus: http.StatusNotFound,
		Detail:     "The message was not found in this conversation. Refresh and try again.",
	},
	CodeSessionTurnNotLatest: {
		HTTPStatus: http.StatusConflict,
		Detail:     "The conversation has newer messages. Refresh and try again.",
	},
	// The latest turn has a request message but no assistant reply on record,
	// so it can be edited but not retried.
	CodeSessionTurnIncomplete: {
		HTTPStatus: http.StatusConflict,
		Detail:     "This message cannot be retried right now. Refresh the conversation.",
	},
	// The server was deployed without the runtime reset coordinator; only an
	// operator can fix it.
	CodeSessionResetUnavailable: {
		HTTPStatus: http.StatusServiceUnavailable,
		Detail:     "Conversation history cannot be reset right now. Contact the developers.",
	},
	// Another operation holds or took over the history reset lease.
	CodeSessionResetConflict: {
		HTTPStatus: http.StatusConflict,
		Detail:     "The conversation is busy. Please try again shortly.",
	},
	CodeHistoryDeleteFailed: {
		HTTPStatus: http.StatusInternalServerError,
		Detail:     "The conversation history could not be deleted. Please try again.",
	},
	// The run's live state could not be published to the Session Runtime
	// (Redis unavailable, or this process lost the run). History is not
	// involved; the client sees the run stop and can retry shortly.
	CodeSessionPublishFailed: {
		HTTPStatus: http.StatusServiceUnavailable,
		Detail:     "The conversation could not be updated. Please try again shortly.",
	},
	// The session was deleted but its in-flight run could not be stopped.
	CodeSessionAbortFailed: {
		HTTPStatus: http.StatusInternalServerError,
		Detail:     "The conversation was deleted, but its running task could not be stopped. Refresh and try again.",
	},
	CodeSessionInterrupted:       {HTTPStatus: http.StatusServiceUnavailable, Detail: "The server interrupted this run during shutdown. It can resume from saved progress after restart."},
	CodeAgentToolTimeout:         {HTTPStatus: http.StatusGatewayTimeout, Detail: "The tool stopped reporting progress. Review its saved result before retrying."},
	CodeScheduleExecutionTimeout: {HTTPStatus: http.StatusGatewayTimeout, Detail: "This scheduled run reached its execution limit. Review its progress or increase the limit."},
	CodeScheduleRunTargetConflict: {
		HTTPStatus:  http.StatusBadRequest,
		Detail:      "The run target, runtime, Agent and session settings of this schedule cannot be combined. Adjust them and try again.",
		AllowedArgs: []string{"field"},
	},
	CodeScheduleModelConflict: {
		HTTPStatus:  http.StatusBadRequest,
		Detail:      "The model setting does not fit this schedule's runtime. Choose the model field that matches the runtime.",
		AllowedArgs: []string{"field"},
	},
	CodeScheduleModelUnusable: {
		HTTPStatus:  http.StatusBadRequest,
		Detail:      "This model cannot run a schedule. Choose an enabled chat model.",
		AllowedArgs: []string{"field"},
	},
	CodeScheduleModelRequired: {
		HTTPStatus:  http.StatusConflict,
		Detail:      "This bot has no default model, so the schedule needs an explicit model. Choose a model or set a default one.",
		AllowedArgs: []string{"field"},
	},
	CodeScheduleSessionModeUnsupported: {
		HTTPStatus: http.StatusConflict,
		Detail:     "Scheduled runs can only continue chat or schedule sessions. Choose another target session.",
	},
	CodeVideoJobOutcomeUnknown: {HTTPStatus: http.StatusBadGateway, Detail: "The video job status could not be confirmed. Check the saved job before creating another video."},
	// Model provider codes. The provider is outside Memoh whoever holds the
	// credential, so each is a dependency fault regardless of its status: a
	// rejected key or an exhausted quota is the provider's answer, not a
	// request Memoh refused. A content moderation refusal would be the one
	// client fault.
	CodeAgentResponseTimeout: {
		HTTPStatus: http.StatusGatewayTimeout,
		Detail:     "The model did not respond in time. Please try again.",
		Fault:      FaultDependency,
	},
	CodeAgentResponseInterrupted: {
		HTTPStatus: http.StatusBadGateway,
		Detail:     "The model response was interrupted. Please try again.",
		Fault:      FaultDependency,
	},
	CodeAgentProviderOverloaded: {
		HTTPStatus: http.StatusServiceUnavailable,
		Detail:     "The model provider is unavailable or overloaded right now. Please try again in a moment.",
		Fault:      FaultDependency,
	},
	CodeAgentProviderRateLimited: {
		HTTPStatus: http.StatusTooManyRequests,
		Detail:     "The model provider rate limit was reached. Please wait a moment before sending again.",
		Fault:      FaultDependency,
	},
	// A provider's rejected key or exhausted quota answers 502: a 401 would
	// sign the Web client out, and RFC 9110 reserves 402.
	CodeAgentProviderQuotaExhausted: {
		HTTPStatus: http.StatusBadGateway,
		Detail:     "The model provider account has no remaining balance or quota.",
		Fault:      FaultDependency,
	},
	CodeAgentProviderAuthFailed: {
		HTTPStatus: http.StatusBadGateway,
		Detail:     "The model provider rejected the credentials. Check the provider API key.",
		Fault:      FaultDependency,
	},
	CodeAgentProviderPermissionDenied: {
		HTTPStatus: http.StatusBadGateway,
		Detail:     "The model provider denied access to this model or resource. Check that the provider account can use this model.",
		Fault:      FaultDependency,
	},
	CodeAgentProviderRequestRejected: {
		HTTPStatus: http.StatusBadGateway,
		Detail:     "The model provider rejected the request. Check the model settings, or try another model.",
		Fault:      FaultDependency,
	},
	CodeAgentProviderUnreachable: {
		HTTPStatus: http.StatusBadGateway,
		Detail:     "Memoh could not reach the model provider. Check the provider address and that the service is running.",
		Fault:      FaultDependency,
	},
	CodeQueueSteerUnsupported: {
		HTTPStatus: http.StatusConflict,
		Detail:     "This run cannot accept steer input. Wait for it to finish and send a new message.",
	},
	CodeRuntimeNativeHistoryLost: {
		HTTPStatus: http.StatusServiceUnavailable,
		Detail:     "The agent could not restore its previous context and started over. Earlier messages remain as history only. Provide any context needed to continue.",
	},
	CodeRuntimeToolsUnavailable: {
		HTTPStatus: http.StatusServiceUnavailable,
		Detail:     "Memoh tools are unavailable for this conversation. Start a new session to restore them.",
	},
	CodeRuntimeElicitationDeclined: {
		HTTPStatus: http.StatusUnprocessableEntity,
		Detail:     "A tool requested an interaction that could not be shown. The request was declined.",
	},
	CodeQueueNoActiveRun: {
		HTTPStatus: http.StatusConflict,
		Detail:     "There is no active run to receive this queued input.",
	},
	CodeQueueAdmissionOverloaded: {
		HTTPStatus: http.StatusTooManyRequests,
		Detail:     "The queue is busy. Please retry this request shortly.",
	},
	CodeQueueAdmissionUnavailable: {
		HTTPStatus: http.StatusServiceUnavailable,
		Detail:     "The queue admission service is temporarily unavailable. Please retry shortly.",
	},
	CodeQueueRequestInvalid: {
		HTTPStatus: http.StatusBadRequest,
		Detail:     "The queue request is invalid.",
	},
	CodeQueueItemNotPending: {
		HTTPStatus: http.StatusConflict,
		Detail:     "This queue item is no longer accepted and pending.",
	},
	CodeQueueItemNotEditable: {
		HTTPStatus: http.StatusForbidden,
		Detail:     "Only the sender can edit this queued message.",
	},
	CodeQueueCapacityExceeded: {
		HTTPStatus: http.StatusConflict,
		Detail:     "This session queue is full. Cancel or wait for pending items before adding more.",
	},
	CodeContextLifecycleRequestInvalid: {
		HTTPStatus: http.StatusBadRequest,
		Detail:     "The context lifecycle request is invalid.",
	},
	CodeContextLifecycleAuthenticationRequired: {
		HTTPStatus: http.StatusUnauthorized,
		Detail:     "Sign in to view context lifecycle diagnostics.",
	},
	CodeContextLifecycleAccessDenied: {
		HTTPStatus: http.StatusForbidden,
		Detail:     "You do not have access to context lifecycle diagnostics.",
	},
	CodeContextLifecycleNotFound: {
		HTTPStatus: http.StatusNotFound,
		Detail:     "The conversation was not found.",
	},
	CodeContextLifecycleLoadFailed: {
		HTTPStatus: http.StatusInternalServerError,
		Detail:     "Context lifecycle diagnostics could not be loaded. Please try again.",
	},
	// Returned when a failure has no public error of its own.
	CodeInternal: {HTTPStatus: http.StatusInternalServerError, Detail: "Something went wrong on the server. Please try again."},
	// 499 is the de facto status for a request the client canceled.
	CodeCanceled:                 {HTTPStatus: 499, Detail: "The request was canceled."},
	CodeHTTPBadRequest:           {HTTPStatus: http.StatusBadRequest, Detail: "The request is invalid."},
	CodeHTTPUnauthorized:         {HTTPStatus: http.StatusUnauthorized, Detail: "Sign in to continue."},
	CodeHTTPForbidden:            {HTTPStatus: http.StatusForbidden, Detail: "You do not have permission to perform this action."},
	CodeHTTPNotFound:             {HTTPStatus: http.StatusNotFound, Detail: "The requested resource was not found."},
	CodeHTTPMethodNotAllowed:     {HTTPStatus: http.StatusMethodNotAllowed, Detail: "This request method is not allowed here."},
	CodeHTTPConflict:             {HTTPStatus: http.StatusConflict, Detail: "The request conflicts with the current state. Refresh and try again."},
	CodeHTTPPayloadTooLarge:      {HTTPStatus: http.StatusRequestEntityTooLarge, Detail: "The request is too large."},
	CodeHTTPUnsupportedMediaType: {HTTPStatus: http.StatusUnsupportedMediaType, Detail: "The request content type is not supported."},
	CodeHTTPUpgradeRequired:      {HTTPStatus: http.StatusUpgradeRequired, Detail: "This endpoint requires a different protocol."},
	CodeHTTPTooManyRequests:      {HTTPStatus: http.StatusTooManyRequests, Detail: "Too many requests. Please wait a moment and try again."},
	CodeHTTPNotImplemented:       {HTTPStatus: http.StatusNotImplemented, Detail: "This operation is not supported by the server."},
	CodeHTTPBadGateway:           {HTTPStatus: http.StatusBadGateway, Detail: "An upstream service returned an invalid response. Please try again."},
	CodeHTTPServiceUnavailable:   {HTTPStatus: http.StatusServiceUnavailable, Detail: "The service is temporarily unavailable. Please try again shortly."},
	CodeHTTPGatewayTimeout:       {HTTPStatus: http.StatusGatewayTimeout, Detail: "An upstream service did not respond in time. Please try again."},
	CodeRequestFieldRequired:     {HTTPStatus: http.StatusBadRequest, Detail: "A required field is missing.", AllowedArgs: []string{"field"}},
	CodeRequestFieldInvalid:      {HTTPStatus: http.StatusBadRequest, Detail: "A field has an invalid value.", AllowedArgs: []string{"field"}},
	CodeSessionNotFound:          {HTTPStatus: http.StatusNotFound, Detail: "The conversation was not found."},
	CodeMCPEndpointInvalid: {
		HTTPStatus:  http.StatusBadRequest,
		Detail:      "Specify either a command or a URL for the MCP server, not both and not neither.",
		AllowedArgs: []string{"server"},
	},
	CodeMCPNameTaken: {
		HTTPStatus:  http.StatusConflict,
		Detail:      "An MCP connection with this name already exists.",
		AllowedArgs: []string{"field"},
	},
	CodeMCPOAuthDiscoveryFailed: {
		HTTPStatus: http.StatusBadGateway,
		Detail:     "OAuth discovery against the MCP server failed.",
		Fault:      FaultDependency,
	},
	CodeMCPOAuthNotDiscovered: {
		HTTPStatus: http.StatusConflict,
		Detail:     "OAuth has not been discovered for this connection.",
	},
	CodeMCPOAuthClientIDRequired: {
		HTTPStatus: http.StatusBadRequest,
		Detail:     "The authorization server cannot register a client automatically; a client_id is required.",
	},
	CodeOAuthStateInvalid: {
		HTTPStatus: http.StatusBadRequest,
		Detail:     "The authorization is unknown or has expired.",
	},
	CodeACPAgentNotFound:         {HTTPStatus: http.StatusBadRequest, Detail: "The selected external agent is unavailable."},
	CodeACPAgentNotEnabled:       {HTTPStatus: http.StatusForbidden, Detail: "The selected external agent is disabled for this bot."},
	CodeACPAgentNotConfigured:    {HTTPStatus: http.StatusBadRequest, Detail: "External agent setup is incomplete for this bot."},
	CodeCodexOAuthIncomplete:     {HTTPStatus: http.StatusConflict, Detail: "Codex ChatGPT sign-in is not complete for this bot workspace."},
	CodeCodexAuthTokenMissing:    {HTTPStatus: http.StatusConflict, Detail: "Codex authentication is missing or incomplete for this bot workspace."},
	CodeACPAgentAuthInvalid:      {HTTPStatus: http.StatusConflict, Detail: "External agent authentication is invalid. Update the agent setup and try again."},
	CodeNoWorkspaceExec:          {HTTPStatus: http.StatusForbidden, Detail: "You do not have permission to run workspace commands for this bot."},
	CodeACPRuntimeOwnerMissing:   {HTTPStatus: http.StatusConflict, Detail: "This external-agent session has no runtime owner. Start a new session to continue."},
	CodeACPDiscussUnsupported:    {HTTPStatus: http.StatusBadRequest, Detail: "This external agent cannot run in discuss mode."},
	CodeGroupChatACPUnsupported:  {HTTPStatus: http.StatusBadRequest, Detail: "Group chats cannot create a chat-mode external-agent session. Use /new codex or /new discuss codex."},
	CodeACPProjectModeInvalid:    {HTTPStatus: http.StatusBadRequest, Detail: "The external agent project mode is invalid."},
	CodeACPProjectPathInvalid:    {HTTPStatus: http.StatusBadRequest, Detail: "The external agent project path must be absolute."},
	CodeACPDisplayArgsInvalid:    {HTTPStatus: http.StatusBadRequest, Detail: "The external agent display arguments are invalid."},
	CodeACPRuntimeStartFailed:    {HTTPStatus: http.StatusInternalServerError, Detail: "External agent runtime failed to start."},
	CodeACPRuntimeBusy:           {HTTPStatus: http.StatusConflict, Detail: "External agent runtime is already processing a turn for this session."},
	CodeACPAttachmentInvalid:     {HTTPStatus: http.StatusBadRequest, Detail: "The attachment is invalid. Please attach it again."},
	CodeACPAttachmentUnavailable: {HTTPStatus: http.StatusBadRequest, Detail: "The attachment could not be made available to the external agent. Please attach it again."},
	CodeRuntimeAgentCommandStale: {HTTPStatus: http.StatusConflict, Detail: "The agent no longer offers this command. Reopen the command picker and try again."},
	CodeACPImageInputUnsupported: {HTTPStatus: http.StatusBadRequest, Detail: "This external agent cannot read the attached image."},
	CodeInvalidChatRuntime:       {HTTPStatus: http.StatusBadRequest, Detail: "The selected chat runtime is invalid."},
	CodeAgentDependencyMissing:   {HTTPStatus: http.StatusConflict, Detail: "A dependency the agent needs is not installed in this workspace. Install it from the bot's dependencies, or wait for the running installation to finish, then send the message again.", AllowedArgs: []string{"dep_id", "install_task_id", "operation_in_progress"}},
	// The IM identity is not linked to a Memoh account; link carries the /link command reference.
	CodeExternalAgentAccountUnbound:             {HTTPStatus: http.StatusForbidden, Detail: "Your chat account is not linked to a Memoh account, so it cannot use this bot's workspace. Link it from Profile, Connected Accounts, then try again.", AllowedArgs: []string{"link"}},
	CodeExternalAgentContainerWorkspaceRequired: {HTTPStatus: http.StatusConflict, Detail: "This agent runtime needs a container workspace. Switch the bot to its container workspace, then try again."},
	CodeAgentChatModelNotConfigured:             {HTTPStatus: http.StatusConflict, Detail: "No chat model is selected. Set a default chat model in Bot settings → General, then try again."},
	CodeRuntimeRunFailed:                        {HTTPStatus: http.StatusInternalServerError, Detail: "The response could not be completed. Please try again."},
	CodeRuntimePromptFailed:                     {HTTPStatus: http.StatusBadGateway, Detail: "The agent runtime could not complete this response. Please try again.", Fault: FaultDependency},
	// Reaper codes: the run was ended because its owner or live state disappeared.
	CodeRuntimeOwnerLeaseExpired: {HTTPStatus: http.StatusServiceUnavailable, Detail: "The server handling this response stopped responding, so the response was ended. Please try again."},
	CodeRuntimeLiveBackendLost:   {HTTPStatus: http.StatusServiceUnavailable, Detail: "The live state of this response was lost, so the response was ended. Please try again."},
	CodeRuntimeAdmissionOrphaned: {HTTPStatus: http.StatusServiceUnavailable, Detail: "This response never started and was cleaned up. Please try again."},
	// Admission codes: a claimed run was abandoned before it started.
	CodeRuntimeFenceActivationFailed:            {HTTPStatus: http.StatusServiceUnavailable, Detail: "The response could not be started. Please try again shortly."},
	CodeRuntimeReservationFailed:                {HTTPStatus: http.StatusServiceUnavailable, Detail: "The response could not be started. Please try again shortly."},
	CodeRuntimeReservationDeclined:              {HTTPStatus: http.StatusConflict, Detail: "Another server took over this conversation before the response started. Please try again."},
	CodeHistoryReset:                            {HTTPStatus: http.StatusConflict, Detail: "This response was canceled because the conversation history was reset."},
	CodeQueueInvocationConflict:                 {HTTPStatus: http.StatusConflict, Detail: "This message was already submitted with different content."},
	CodeQueueUnsupportedSession:                 {HTTPStatus: http.StatusConflict, Detail: "Queue controls are not available in discussion sessions."},
	CodeQueueFollowUpUnsupportedChannel:         {HTTPStatus: http.StatusConflict, Detail: "Queued follow-ups are not available on this channel. Add to the current reply instead, or queue from the web app."},
	CodeQueueTargetRunNotActive:                 {HTTPStatus: http.StatusConflict, Detail: "The response ended before this instruction reached it. Send it as a new message."},
	CodeQueueFollowUpCommandInvalid:             {HTTPStatus: http.StatusInternalServerError, Detail: "This queued message could not be started. Send it again as a new message."},
	CodeSlashAttachmentsUnsupported:             {HTTPStatus: http.StatusBadRequest, Detail: "Slash commands cannot include attachments. Remove the attachments and send the command again."},
	CodeSlashPermissionDenied:                   {HTTPStatus: http.StatusForbidden, Detail: "You do not have permission to use this command."},
	CodeSlashRequiresWebSocket:                  {HTTPStatus: http.StatusBadRequest, Detail: "Skill activation requires a live chat connection. Reconnect and try again."},
	CodeSlashReservedMetadata:                   {HTTPStatus: http.StatusBadRequest, Detail: "This message carries reserved skill metadata, which clients cannot supply."},
	CodeSlashSkillActivationUnsupported:         {HTTPStatus: http.StatusConflict, Detail: "Skills can only be activated in a chat session that uses the bot's own model. Switch to such a session and try again."},
	CodeSlashSkillAmbiguous:                     {HTTPStatus: http.StatusConflict, Detail: "More than one skill has this name. Rename or disable one of them in the bot's skills, then try again."},
	CodeSlashSkillContextTooLarge:               {HTTPStatus: http.StatusBadRequest, Detail: "The selected skills are too large for one message. Select fewer skills and try again."},
	CodeSlashSkillDisabled:                      {HTTPStatus: http.StatusConflict, Detail: "This skill is disabled. Enable it in the bot's skills, then try again."},
	CodeSlashSkillNotFound:                      {HTTPStatus: http.StatusNotFound, Detail: "No skill has this name. Check the name and try again."},
	CodeSlashSkillNotUsable:                     {HTTPStatus: http.StatusConflict, Detail: "This skill is not available for chat."},
	CodeSlashSkillSyntaxInvalid:                 {HTTPStatus: http.StatusBadRequest, Detail: "Use /<skill-name> [prompt] to activate a skill."},
	CodeSlashTooManySkills:                      {HTTPStatus: http.StatusBadRequest, Detail: "Too many skills in one message. Activate fewer skills and try again."},
	CodeSlashUnknownCommand:                     {HTTPStatus: http.StatusBadRequest, Detail: "Unknown slash command. Send /help to see the available commands."},
	CodeSlashUnsupportedInWeb:                   {HTTPStatus: http.StatusBadRequest, Detail: "This slash command is not available in Web chat."},
	CodeMemoryCompactUnsupported:                {HTTPStatus: http.StatusNotImplemented, Detail: "The selected memory provider does not support memory compaction. Choose a provider that supports it in the bot's memory settings."},
	CodeBotReadyUpdateFailed:                    {HTTPStatus: http.StatusInternalServerError, Detail: "The bot could not be loaded after its workspace was set up. Refresh the page."},
	CodeWorkspaceSetupTimeout:                   {HTTPStatus: http.StatusGatewayTimeout, Detail: "Workspace setup is still in progress. Check the bot's workspace page."},
	CodeWorkspaceSetupFailed:                    {HTTPStatus: http.StatusInternalServerError, Detail: "Something went wrong while setting up the workspace."},
	CodeWorkspaceDisplayDisabled:                {HTTPStatus: http.StatusConflict, Detail: "Workspace desktop is not enabled."},
	CodeWorkspaceDependencyOperationUnknown:     {HTTPStatus: http.StatusGatewayTimeout, Detail: "The operation result is not yet confirmed. Refresh dependencies to check its status."},
	CodeWorkspaceCreateRequestInvalid:           {HTTPStatus: http.StatusBadRequest, Detail: "The workspace request is invalid."},
	CodeWorkspaceCreateFailed:                   {HTTPStatus: http.StatusInternalServerError, Detail: "Failed to create the workspace. Please try again."},
	CodeWorkspaceNotFound:                       {HTTPStatus: http.StatusNotFound, Detail: "This bot has no workspace."},
	CodeWorkspaceLoadFailed:                     {HTTPStatus: http.StatusInternalServerError, Detail: "Failed to load workspace info. Please try again."},
	CodeWorkspaceMetricsLoadFailed:              {HTTPStatus: http.StatusInternalServerError, Detail: "Failed to load workspace metrics. Please try again."},
	CodeWorkspaceResourceLimitsInvalid:          {HTTPStatus: http.StatusBadRequest, Detail: "The resource limits are invalid. Limits must be non-negative."},
	CodeWorkspaceResourceLimitsRequired:         {HTTPStatus: http.StatusBadRequest, Detail: "Resource limits are required."},
	CodeWorkspaceResourceLimitsSaveFailed:       {HTTPStatus: http.StatusInternalServerError, Detail: "Failed to save resource limits. Please try again."},
	CodeWorkspaceDeleteFailed:                   {HTTPStatus: http.StatusInternalServerError, Detail: "Failed to delete the workspace. Please try again."},
	CodeWorkspaceStartFailed:                    {HTTPStatus: http.StatusInternalServerError, Detail: "Failed to start the workspace. Please try again."},
	CodeWorkspaceStopFailed:                     {HTTPStatus: http.StatusInternalServerError, Detail: "Failed to stop the workspace. Please try again."},
	CodeWorkspaceSnapshotsUnsupported:           {HTTPStatus: http.StatusNotImplemented, Detail: "Snapshots are not supported by this workspace runtime."},
	CodeWorkspaceSnapshotManagerUnavailable:     {HTTPStatus: http.StatusInternalServerError, Detail: "Snapshots are not configured on this server."},
	CodeWorkspaceSnapshotRequestInvalid:         {HTTPStatus: http.StatusBadRequest, Detail: "The snapshot request is invalid."},
	CodeWorkspaceSnapshotCreateFailed:           {HTTPStatus: http.StatusInternalServerError, Detail: "Failed to create the snapshot. Please try again."},
	CodeWorkspaceSnapshotsLoadFailed:            {HTTPStatus: http.StatusInternalServerError, Detail: "Failed to load snapshots. Please try again."},
	CodeWorkspaceSnapshotterMismatch:            {HTTPStatus: http.StatusBadRequest, Detail: "The snapshotter does not match the workspace runtime."},
	CodeWorkspaceSnapshotChainNotFound:          {HTTPStatus: http.StatusInternalServerError, Detail: "The workspace snapshot history could not be found."},
	CodeWorkspaceManagerUnavailable:             {HTTPStatus: http.StatusInternalServerError, Detail: "Workspace manager is not configured."},
	CodeWorkspaceSnapshotRollbackRequestInvalid: {HTTPStatus: http.StatusBadRequest, Detail: "The rollback request is invalid."},
	CodeWorkspaceSnapshotVersionInvalid:         {HTTPStatus: http.StatusBadRequest, Detail: "The snapshot version is invalid."},
	CodeWorkspaceSnapshotRollbackFailed:         {HTTPStatus: http.StatusInternalServerError, Detail: "Failed to roll back the workspace. Please try again."},
	CodeWorkspacePreservedDataNotFound:          {HTTPStatus: http.StatusNotFound, Detail: "No preserved workspace data was found."},
	CodeWorkspaceRestoreFailed:                  {HTTPStatus: http.StatusInternalServerError, Detail: "Failed to restore workspace data. Please try again."},
}

// Error keeps the public contract separate from private diagnostics. The cause
// is intentionally not exposed through Unwrap; transport boundaries may log it
// through CauseOf without making infrastructure details part of the API.
type Error struct {
	code  Code
	args  map[string]string
	cause error
}

// New creates a public application error without an infrastructure cause.
func New(code Code, args map[string]string) *Error {
	return &Error{code: code, args: sanitizeArgs(code, args)}
}

// FieldRequired is the answer to a request that lacks field. field is the
// name the request uses for it: the JSON key, query parameter or path
// parameter, as written there, with dots for a nested key.
func FieldRequired(field string) *Error {
	return New(CodeRequestFieldRequired, map[string]string{"field": field})
}

// FieldInvalid is the answer to a request whose field holds a value this
// process cannot accept; cause says why and stays private. field is named as
// for FieldRequired.
func FieldInvalid(field string, cause error) *Error {
	return Wrap(CodeRequestFieldInvalid, cause, map[string]string{"field": field})
}

// Wrap retains a private cause for boundary logging. Only catalog-allowed args
// are kept for serialization.
func Wrap(code Code, cause error, args map[string]string) *Error {
	return &Error{code: code, args: sanitizeArgs(code, args), cause: cause}
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return string(e.code)
}

func As(err error) (*Error, bool) {
	var appErr *Error
	if !errors.As(err, &appErr) {
		return nil, false
	}
	return appErr, true
}

func CodeOf(err error) Code {
	appErr, ok := As(err)
	if !ok {
		return ""
	}
	return appErr.code
}

func ArgsOf(err error) map[string]string {
	appErr, ok := As(err)
	if !ok {
		return map[string]string{}
	}
	return cloneArgs(appErr.args)
}

// CauseOf is intentionally separate from errors.Unwrap: infrastructure errors
// are retained for boundary logging without becoming a domain-level contract.
func CauseOf(err error) error {
	appErr, ok := As(err)
	if !ok {
		return nil
	}
	return appErr.cause
}

// Cause returns the private cause for diagnostic traversal. The errs package
// walks Cause() as well as Unwrap to find the origin, stack and attributes of
// a failure. Error deliberately has no Unwrap: errors.Is and errors.As stop at
// a public error, so code above it cannot branch on what it wraps.
func (e *Error) Cause() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func Lookup(code Code) (Definition, bool) {
	definition, ok := catalog[code]
	definition.AllowedArgs = append([]string(nil), definition.AllowedArgs...)
	return definition, ok
}

func TypeURI(code Code) string {
	return "urn:memoh:error:" + string(code)
}

func cloneArgs(args map[string]string) map[string]string {
	cloned := make(map[string]string, len(args))
	for key, value := range args {
		key = strings.TrimSpace(key)
		if key != "" {
			cloned[key] = value
		}
	}
	return cloned
}

// sanitizeArgs is the public-data boundary for error metadata. Callers may
// provide useful internal context, but only keys declared by the catalog are
// allowed onto the wire.
func sanitizeArgs(code Code, args map[string]string) map[string]string {
	definition, ok := catalog[code]
	if !ok || len(definition.AllowedArgs) == 0 {
		return map[string]string{}
	}

	allowed := make(map[string]struct{}, len(definition.AllowedArgs))
	for _, key := range definition.AllowedArgs {
		allowed[key] = struct{}{}
	}
	sanitized := make(map[string]string, len(args))
	for key, value := range args {
		key = strings.TrimSpace(key)
		if _, ok := allowed[key]; ok {
			sanitized[key] = value
		}
	}
	return sanitized
}
