package channel

import (
	"context"
	"strings"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/i18n"
)

// ErrorText is the channel copy for a public error: errors.<code> from the
// channel locale with the error's args substituted, then the catalog detail.
// public is an answer of errs.Answer, so its code is in the catalog.
func ErrorText(t *i18n.Localizer, public *apperror.Error) string {
	code := apperror.CodeOf(public)
	if t != nil {
		key := "errors." + string(code)
		args := apperror.ArgsOf(public)
		params := make(map[string]any, len(args))
		for name, value := range args {
			params[name] = value
		}
		if text := t.T(key, params); strings.TrimSpace(text) != "" && text != key {
			return text
		}
	}
	definition, _ := apperror.Lookup(code)
	return definition.Detail
}

// CodeEvent is the stream error for a code that was recorded rather than
// returned: the code a failed run stored, or the code of a history event. It
// carries the copy for code, or the copy for runtime_run_failed when code is
// not in the catalog, so the text is always catalog copy and never an error's
// own text.
func CodeEvent(t *i18n.Localizer, code apperror.Code, args map[string]string) StreamEvent {
	code = apperror.Code(strings.TrimSpace(string(code)))
	if _, ok := apperror.Lookup(code); !ok {
		code, args = apperror.CodeRuntimeRunFailed, nil
	}
	return StreamEvent{Type: StreamEventError, Error: ErrorText(t, apperror.New(code, args)), ErrorCode: string(code)}
}

// ErrorEvent is the stream error a channel shows for err, answered by
// errs.Answer as an HTTP response would be: the copy for the public error,
// carrying its code. The error's own text is never shown; the unit's result
// record reports it. It reports false when the caller has canceled, which is
// not answered.
func ErrorEvent(ctx context.Context, t *i18n.Localizer, err error) (StreamEvent, bool) {
	public, fault := errs.Answer(ctx, err)
	if public == nil || fault == apperror.FaultCanceled {
		return StreamEvent{}, false
	}
	return StreamEvent{Type: StreamEventError, Error: ErrorText(t, public), ErrorCode: string(apperror.CodeOf(public))}, true
}

// ReplyText is the reply a flow sends for err. A specific public error is
// shown with its copy. An error errs.Answer answers with a generic code,
// internal or http.bad_request, is shown with fallback, the flow's own
// failure copy, or with the generic copy when fallback is empty. It is empty
// when the caller has canceled, and the flow then sends no reply.
func ReplyText(ctx context.Context, t *i18n.Localizer, err error, fallback string) string {
	public, fault := errs.Answer(ctx, err)
	if public == nil || fault == apperror.FaultCanceled {
		return ""
	}
	if code := apperror.CodeOf(public); fallback != "" && (code == apperror.CodeInternal || code == apperror.CodeHTTPBadRequest) {
		return fallback
	}
	return ErrorText(t, public)
}
