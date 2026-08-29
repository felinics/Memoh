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
	CodeBotNameTaken                     Code = "bot.name_taken"
	CodeBotAgentNotFound                 Code = "bot_agent.not_found"
	CodeBotAgentNameTaken                Code = "bot_agent.name_taken"
	CodeBotAgentInvalidRuntime           Code = "bot_agent.invalid_runtime"
	CodeBotAgentInvalidMetadata          Code = "bot_agent.invalid_metadata"
	CodeBotAgentDefaultInUse             Code = "bot_agent.default_in_use"
	CodeBotAgentUnavailable              Code = "bot_agent.unavailable"
	CodeChannelRuntimeUnavailable        Code = "channel.runtime_unavailable"
	CodeCompactionModelUnavailable       Code = "compaction.model_unavailable"
	CodeSettingsReasoningEffortInvalid   Code = "settings.reasoning_effort_invalid"
	CodeSettingsReasoningUnavailable     Code = "settings.reasoning_options_unavailable"
	CodeContextBudgetUnsatisfied         Code = "context.budget_unsatisfied"
	CodeContextProtectedOverflow         Code = "context.protected_overflow"
	CodeWorkspaceUnreachable             Code = "workspace.unreachable"
	CodeWorkspaceReadPermissionRequired  Code = "workspace.read_permission_required"
	CodeWorkspaceTargetInUse             Code = "workspace.target_in_use"
	CodeWorkspaceImageIncompatible       Code = "workspace.image_incompatible"
	CodeWorkspaceTemplateBootstrapFailed Code = "workspace.template_bootstrap_failed"
	CodeWorkspaceDisplayPrepareFailed    Code = "workspace.display_prepare_failed"
	CodeProviderTemplateNotFound         Code = "provider_template.not_found"
	CodeProviderTemplateDomainInvalid    Code = "provider_template.domain_invalid"
	CodeProviderTemplateDomainMismatch   Code = "provider_template.domain_mismatch"
	CodeProviderTemplateOperationFailed  Code = "provider_template.operation_failed"
	CodeProviderNameTaken                Code = "provider.name_taken"
	CodeProviderTemplateRequestInvalid   Code = "provider_template.request_invalid"
	CodeSearchProviderTypeConflict       Code = "search_provider.type_conflict"
	CodeConnectorRequestInvalid          Code = "connector.request_invalid"
	CodeConnectorNotConfigured           Code = "connector.not_configured"
	CodeConnectorNotFound                Code = "connector.not_found"
	CodeConnectorConflict                Code = "connector.conflict"
	CodeConnectorRequestRejected         Code = "connector.request_rejected"
	CodeConnectorUpstreamUnavailable     Code = "connector.upstream_unavailable"
	CodeConnectorOperationFailed         Code = "connector.operation_failed"
	CodeSkillBuiltinReadOnly             Code = "skill.builtin_read_only"
	CodeSkillNameTaken                   Code = "skill.name_taken"
	CodeSkillSaveFailed                  Code = "skill.save_failed"
	CodeRegistryUnavailable              Code = "registry.unavailable"
	CodeRegistryPackageNotFound          Code = "registry.package_not_found"
	CodeRegistryPackageInvalid           Code = "registry.package_invalid"
	CodeRegistryPackageInstallFailed     Code = "registry.package_install_failed"
	CodeProfileRequestInvalid            Code = "profile.request_invalid"
	CodeProfileTitleModelInvalid         Code = "profile.title_model_invalid"
	CodeProfileUpdateFailed              Code = "profile.update_failed"
	CodeACPRequestInvalid                Code = "acp.request_invalid"
	CodeACPAccessForbidden               Code = "acp.access_forbidden"
	CodeACPRuntimeNotFound               Code = "acp.runtime_not_found"
	CodeACPRuntimeConflict               Code = "acp.runtime_conflict"
	CodeACPRuntimeLimitReached           Code = "acp.runtime_limit_reached"
	CodeACPOperationFailed               Code = "acp.operation_failed"
	CodeACPTurnReplacementUnsupported    Code = "acp.turn_replacement_unsupported"
	CodeACPModelSelectionUnsupported     Code = "acp.model_selection_unsupported"
	CodeACPModelIDRequired               Code = "acp.model_id_required"
	CodeACPModelUnavailable              Code = "acp.model_unavailable"
	CodeACPReasoningUnsupported          Code = "acp.reasoning_selection_unsupported"
	CodeACPReasoningEffortRequired       Code = "acp.reasoning_effort_required"
	CodeACPReasoningUnavailable          Code = "acp.reasoning_effort_unavailable"
	CodeACPModeSelectionUnsupported      Code = "acp.mode_selection_unsupported"
	CodeACPModeIDRequired                Code = "acp.mode_id_required"
	CodeACPModeUnavailable               Code = "acp.mode_unavailable"
	CodeACPConfigUpdateFailed            Code = "acp.config_update_failed"
	CodeToolApprovalForbidden            Code = "tool_approval.forbidden"
	CodeToolApprovalNotFound             Code = "tool_approval.not_found"
	CodeToolApprovalExpired              Code = "tool_approval.expired"
	CodeToolApprovalAmbiguous            Code = "tool_approval.ambiguous"
	CodeToolApprovalRequestInvalid       Code = "tool_approval.request_invalid"
	CodeToolApprovalOperationFailed      Code = "tool_approval.operation_failed"
	CodeUserInputForbidden               Code = "user_input.forbidden"
	CodeUserInputExpired                 Code = "user_input.expired"
	CodeUserInputOperationFailed         Code = "user_input.operation_failed"
	CodeSessionBusy                      Code = "session_runtime.session_busy"
	CodeSessionInvocationConflict        Code = "session_runtime.invocation_conflict"
	CodeSessionHistoryInconsistent       Code = "session_runtime.history_inconsistent"
	CodeAgentResponseTimeout             Code = "agent.response_timeout"
	CodeAgentResponseInterrupted         Code = "agent.response_interrupted"

	CodeContextLifecycleRequestInvalid         Code = "context_lifecycle.request_invalid"
	CodeContextLifecycleAuthenticationRequired Code = "context_lifecycle.authentication_required"
	CodeContextLifecycleAccessDenied           Code = "context_lifecycle.access_denied"
	CodeContextLifecycleNotFound               Code = "context_lifecycle.not_found"
	CodeContextLifecycleLoadFailed             Code = "context_lifecycle.load_failed"
)

// Definition is the single catalog entry for a public error contract.
// Type URIs and frontend i18n keys are derived mechanically from Code.
type Definition struct {
	HTTPStatus  int
	Detail      string
	AllowedArgs []string
}

// codesync(error-catalog): Detail strings double as the no-locale fallback for
// clients; the localized copies live under errors.* in
// apps/web/src/i18n/locales/{en,zh,ja}.json. Keep both sides in sync.
var catalog = map[Code]Definition{
	CodeBotNameTaken: {
		HTTPStatus:  http.StatusConflict,
		Detail:      "This name is already taken.",
		AllowedArgs: []string{"field"},
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
	CodeChannelRuntimeUnavailable: {
		HTTPStatus: http.StatusServiceUnavailable,
		Detail:     "The channel service could not be reached.",
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
		Detail:     "The model context window is too small for this request.",
	},
	CodeContextProtectedOverflow: {
		HTTPStatus: http.StatusUnprocessableEntity,
		Detail:     "Required context exceeds the model context budget.",
	},
	CodeWorkspaceUnreachable: {
		HTTPStatus: http.StatusServiceUnavailable,
		Detail:     "The workspace could not be reached.",
	},
	CodeWorkspaceReadPermissionRequired: {
		HTTPStatus: http.StatusForbidden,
		Detail:     "Ask the bot owner for file-read access before using this connected computer.",
	},
	CodeWorkspaceTargetInUse: {
		HTTPStatus: http.StatusConflict,
		Detail:     "This computer is still used by a folder and cannot be disconnected.",
	},
	CodeWorkspaceImageIncompatible: {
		HTTPStatus: http.StatusUnprocessableEntity,
		Detail:     "The workspace image is incompatible with this version of Memoh.",
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
	CodeRegistryUnavailable: {
		HTTPStatus: http.StatusBadGateway,
		Detail:     "The Supermarket is unavailable.",
	},
	CodeRegistryPackageNotFound: {
		HTTPStatus: http.StatusNotFound,
		Detail:     "The Skill package was not found.",
	},
	CodeRegistryPackageInvalid: {
		HTTPStatus: http.StatusBadGateway,
		Detail:     "The Skill package is invalid.",
	},
	CodeRegistryPackageInstallFailed: {
		HTTPStatus: http.StatusInternalServerError,
		Detail:     "The Skill package could not be installed.",
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
	CodeACPTurnReplacementUnsupported: {
		HTTPStatus: http.StatusBadRequest,
		Detail:     "Retry and edit are unavailable for external agent sessions. Send a new message instead.",
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
	},
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
	CodeSessionHistoryInconsistent: {
		HTTPStatus: http.StatusInternalServerError,
		Detail:     "The conversation history could not be reconciled. Refresh and try again.",
	},
	CodeAgentResponseTimeout: {
		HTTPStatus: http.StatusGatewayTimeout,
		Detail:     "The model did not respond in time. Please try again.",
	},
	CodeAgentResponseInterrupted: {
		HTTPStatus: http.StatusBadGateway,
		Detail:     "The model response was interrupted. Please try again.",
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
