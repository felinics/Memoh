//nolint:errorlint // walk visits every node of the chain and each check is about that node alone; errors.As would match a deeper node instead.
package errs

import (
	"log/slog"
	"net/url"
	"regexp"
	"strings"
)

// LogAttrs returns the error fields of a result or event record.
func (r Report) LogAttrs() []slog.Attr {
	out := []slog.Attr{slog.String("fault", string(r.Fault)), slog.String("reason", r.Reason), slog.String("error", r.Text)}
	if r.Source != nil {
		out = append(out, slog.Any("error_source", *r.Source))
	}
	if len(r.Stack) > 0 {
		values := make([]map[string]any, len(r.Stack))
		for i, frame := range r.Stack {
			values[i] = map[string]any{
				"function": frame.Function,
				"file":     frame.File,
				"line":     frame.Line,
			}
		}
		out = append(out, slog.Any("error_stack", values))
	}
	if len(r.Attrs) > 0 {
		values := make([]any, len(r.Attrs))
		for i := range r.Attrs {
			values[i] = r.Attrs[i]
		}
		out = append(out, slog.Group("error_attrs", values...))
	}
	if r.Remote {
		out = append(out, slog.Bool("remote", true))
	}
	if r.RemoteFault != "" {
		out = append(out, slog.String("remote_fault", r.RemoteFault))
	}
	if r.Panic {
		out = append(out, slog.Bool("panic", true))
	}
	return out
}

// urlPattern matches a URL of any scheme: a database DSN
// (postgres://user:pass@…) carries credentials too.
var urlPattern = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s]+`)

// RedactURLs removes the userinfo, query and fragment of every URL in text.
// Rendering a chain applies it; text from outside the process (an upstream
// error message) must go through it before it becomes an attribute. A URL
// that does not parse keeps only its scheme, since the rest may hold
// credentials.
func RedactURLs(text string) string {
	return urlPattern.ReplaceAllStringFunc(text, func(raw string) string {
		leading, trailing := "", ""
		for raw != "" && strings.ContainsRune("([{\"'", rune(raw[0])) {
			leading, raw = leading+raw[:1], raw[1:]
		}
		for raw != "" && strings.ContainsRune(".,;:!?)]}\"'", rune(raw[len(raw)-1])) {
			trailing, raw = raw[len(raw)-1:]+trailing, raw[:len(raw)-1]
		}
		u, err := url.Parse(raw)
		if err != nil {
			return leading + raw[:strings.Index(raw, "://")+3] + "[redacted]" + trailing
		}
		u.User, u.RawQuery, u.Fragment = nil, "", ""
		return leading + u.String() + trailing
	})
}

// Text returns the redacted text of the whole chain.
func Text(err error) string {
	return render(err)
}

func render(err error) string {
	if err == nil {
		return ""
	}
	switch e := err.(type) {
	case *faultError:
		switch {
		case e.cause == nil:
			return RedactURLs(e.msg)
		case e.msg == "":
			return render(e.cause)
		}
		return RedactURLs(e.msg) + ": " + render(e.cause)
	case *marker:
		return render(e.err)
	case interface{ Unwrap() []error }:
		parts := make([]string, 0)
		for _, child := range e.Unwrap() {
			if text := render(child); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "; ")
	}
	text := err.Error()
	child := single(err)
	if child == nil {
		return RedactURLs(text)
	}
	if childText := child.Error(); strings.HasSuffix(text, childText) {
		return RedactURLs(strings.TrimSuffix(text, childText)) + render(child)
	}
	if _, unwraps := err.(interface{ Unwrap() error }); !unwraps {
		// A Cause()-only error keeps its cause out of Error() on purpose, as
		// apperror does: Error() is the code alone. The diagnostic text still
		// needs the cause.
		if childText := render(child); childText != "" {
			return RedactURLs(text) + ": " + childText
		}
	}
	return RedactURLs(text)
}
