package slash

import "github.com/felinics/memoh/internal/apperror"

// Refusals of a slash request. Each is the catalog code the refusal is
// answered with, on the Web composer and in IM channels alike.
const (
	CodeUnknownSlash                   = apperror.CodeSlashUnknownCommand
	CodeUnsupportedWebCommand          = apperror.CodeSlashUnsupportedInWeb
	CodeInvalidSkillSlashSyntax        = apperror.CodeSlashSkillSyntaxInvalid
	CodeRequestedSkillNotFound         = apperror.CodeSlashSkillNotFound
	CodeRequestedSkillAmbiguous        = apperror.CodeSlashSkillAmbiguous
	CodeRequestedSkillDisabled         = apperror.CodeSlashSkillDisabled
	CodeRequestedSkillNotRuntimeUsable = apperror.CodeSlashSkillNotUsable
	CodeTooManyRequestedSkills         = apperror.CodeSlashTooManySkills
	CodeRequestedSkillContextTooLarge  = apperror.CodeSlashSkillContextTooLarge
	CodeSlashAttachmentsUnsupported    = apperror.CodeSlashAttachmentsUnsupported
	CodeUnsupportedSkillSlashContext   = apperror.CodeSlashSkillActivationUnsupported
	CodeUnsupportedLegacyEndpoint      = apperror.CodeSlashRequiresWebSocket
	CodePermissionDenied               = apperror.CodeSlashPermissionDenied
	CodeReservedSkillMetadata          = apperror.CodeSlashReservedMetadata
)

// Error is a slash request refused for Code. Callers in the process match it
// with errors.As; it unwraps to the catalog error for Code, so every boundary
// answers it with that code, also when it crosses an RPC.
type Error struct {
	Code apperror.Code
}

func NewError(code apperror.Code) Error {
	return Error{Code: code}
}

func (e Error) Error() string {
	return string(e.Code)
}

func (e Error) Unwrap() error {
	return apperror.New(e.Code, nil)
}
