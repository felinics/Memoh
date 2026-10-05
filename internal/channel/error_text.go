package channel

import (
	"strings"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/i18n"
)

// ErrorCodeText is the channel copy for code: errors.<code> from the channel
// locale with args substituted, then the catalog detail. It reports false when
// neither has copy for code.
func ErrorCodeText(t *i18n.Localizer, code apperror.Code, args map[string]string) (string, bool) {
	code = apperror.Code(strings.TrimSpace(string(code)))
	if code == "" {
		return "", false
	}
	key := "errors." + string(code)
	if t != nil {
		params := make(map[string]any, len(args))
		for name, value := range args {
			params[name] = value
		}
		if text := t.T(key, params); strings.TrimSpace(text) != "" && text != key {
			return text, true
		}
	}
	if definition, ok := apperror.Lookup(code); ok {
		return definition.Detail, true
	}
	return "", false
}

// RunFailureEvent is the stream error a channel shows for a failed run. It
// carries the copy for code, or the copy for runtime_run_failed when code has
// none, so the text is always catalog copy and never an error's own text.
func RunFailureEvent(t *i18n.Localizer, code apperror.Code, args map[string]string) StreamEvent {
	if text, ok := ErrorCodeText(t, code, args); ok {
		return StreamEvent{Type: StreamEventError, Error: text, ErrorCode: string(code)}
	}
	text, _ := ErrorCodeText(t, apperror.CodeRuntimeRunFailed, nil)
	return StreamEvent{Type: StreamEventError, Error: text, ErrorCode: string(apperror.CodeRuntimeRunFailed)}
}

// ErrorEvent is the stream error a channel shows for err. An error whose code
// has copy is shown with that copy and carries the code. Any other error is
// shown with the generic copy for its fault, as a Web client shows a Problem
// whose code it does not know: the copy for a bad request for a client fault,
// and the copy for internal otherwise. The error's own text is never shown;
// the unit's result record reports it.
func ErrorEvent(t *i18n.Localizer, err error) StreamEvent {
	if err == nil {
		return StreamEvent{Type: StreamEventError}
	}
	code := apperror.CodeOf(err)
	if text, ok := ErrorCodeText(t, code, apperror.ArgsOf(err)); ok {
		return StreamEvent{Type: StreamEventError, Error: text, ErrorCode: string(code)}
	}
	code = apperror.CodeInternal
	if errs.FaultOf(err) == errs.FaultClient {
		code = apperror.CodeHTTPBadRequest
	}
	text, _ := ErrorCodeText(t, code, nil)
	return StreamEvent{Type: StreamEventError, Error: text, ErrorCode: string(code)}
}
