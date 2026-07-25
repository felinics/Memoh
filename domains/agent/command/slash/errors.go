package slash

const (
	CodeUnknownSlash                   = "unknown_slash"
	CodeUnsupportedWebCommand          = "unsupported_web_command"
	CodeInvalidSkillSlashSyntax        = "invalid_skill_slash_syntax"
	CodeRequestedSkillNotFound         = "requested_skill_not_found"
	CodeRequestedSkillAmbiguous        = "requested_skill_ambiguous"
	CodeRequestedSkillDisabled         = "requested_skill_disabled"
	CodeRequestedSkillNotRuntimeUsable = "requested_skill_not_runtime_usable"
	CodeTooManyRequestedSkills         = "too_many_requested_skills"
	CodeRequestedSkillContextTooLarge  = "requested_skill_context_too_large"
	CodeSlashAttachmentsUnsupported    = "slash_attachments_unsupported"
	CodeUnsupportedSkillSlashContext   = "unsupported_skill_slash_context"
	CodeUnsupportedLegacyEndpoint      = "unsupported_legacy_endpoint"
	CodePermissionDenied               = "permission_denied"
	CodeReservedSkillMetadata          = "reserved_skill_metadata"
	CodeInvalidQuickActionScope        = "invalid_quick_action_scope"
)

type Error struct {
	Code string
	Msg  string
}

func (e Error) Error() string {
	if e.Msg != "" {
		return e.Msg
	}
	return e.Code
}

func NewError(code string) Error {
	return Error{Code: code, Msg: code}
}
