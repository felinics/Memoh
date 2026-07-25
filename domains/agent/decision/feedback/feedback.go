// Package feedback defines stable user-facing Agent Control Protocol errors.
package feedback

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const (
	CodeAgentNotFound         = "acp_agent_not_found"
	CodeAgentNotEnabled       = "acp_agent_not_enabled"
	CodeAgentNotConfigured    = "acp_agent_not_configured"
	CodeCodexOAuthIncomplete  = "codex_oauth_incomplete"
	CodeCodexAuthTokenMissing = "codex_auth_token_missing" //nolint:gosec // G101 false positive: stable error-code identifier, not a credential.
	CodeAgentAuthInvalid      = "acp_agent_auth_invalid"
	CodeNoWorkspaceExec       = "no_workspace_exec"
	CodeRuntimeOwnerMissing   = "acp_runtime_owner_missing"
	CodeDiscussUnsupported    = "acp_discuss_unsupported"
	CodeGroupChatUnsupported  = "group_chat_acp_unsupported"
	CodeProjectModeInvalid    = "acp_project_mode_invalid"
	CodeProjectPathInvalid    = "acp_project_path_invalid"
	CodeDisplayArgsInvalid    = "acp_display_args_invalid"
	CodeRuntimeStartFailed    = "acp_runtime_start_failed"
	CodeRuntimeBusy           = "acp_runtime_busy"
	CodeAttachmentInvalid     = "acp_attachment_invalid"
	CodeAttachmentUnavailable = "acp_attachment_unavailable"
	CodeImageInputUnsupported = "acp_image_input_unsupported"
	CodeInvalidChatRuntime    = "invalid_chat_runtime"
)

type Error struct {
	Code       string            `json:"code"`
	Reason     string            `json:"reason,omitempty"`
	HTTPStatus int               `json:"http_status"`
	I18nKey    string            `json:"i18n_key,omitempty"`
	Args       map[string]string `json:"args,omitempty"`
	Message    string            `json:"message"`
}

func New(code, reason string, httpStatus int, i18nKey, message string, args map[string]string) *Error {
	code = strings.TrimSpace(code)
	if httpStatus == 0 {
		httpStatus = http.StatusBadRequest
	}
	if message == "" {
		message = code
	}
	if args == nil {
		args = map[string]string{}
	}
	return &Error{
		Code:       code,
		Reason:     strings.TrimSpace(reason),
		HTTPStatus: httpStatus,
		I18nKey:    strings.TrimSpace(i18nKey),
		Args:       args,
		Message:    message,
	}
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Reason != "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Reason)
	}
	return e.Code
}

func (e *Error) MarshalJSON() ([]byte, error) {
	type payload Error
	if e == nil {
		return []byte("null"), nil
	}
	return json.Marshal((*payload)(e))
}

func (e *Error) WithArg(key, value string) *Error {
	if e == nil {
		return e
	}
	if e.Args == nil {
		e.Args = map[string]string{}
	}
	e.Args[strings.TrimSpace(key)] = strings.TrimSpace(value)
	return e
}
