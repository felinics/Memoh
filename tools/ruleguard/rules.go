//go:build ruleguard

// Package ruleguard holds the go-critic ruleguard rules run by golangci-lint.
// See "Result records" in docs/errors.md for the rules they enforce.
//
// golangci-lint does not invalidate its result cache when this file changes.
// After editing a rule, run `golangci-lint cache clean` (mise run lint:clean)
// before linting locally, or the old results are reported again.
package ruleguard

import "github.com/quasilyte/go-ruleguard/dsl"

// errorReturnedAfterLog reports a log record of an error that is then
// returned. The caller's result record already records the failure, so the
// log is a second record of it. A healthcheck that turns the error into its
// result is a false positive and carries a nolint with the reason.
func errorReturnedAfterLog(m dsl.Matcher) {
	// The log is the last statement of the if block that returns.
	m.Match(
		`if $*_ { $*_; $log.$method($*_, slog.Any($_, $err), $*_); $*_; return $*ret }`,
		`if $*_ { $*_; $log.$method($*_, slog.String($_, $err.Error()), $*_); $*_; return $*ret }`,
		`if $*_ { $*_; if $_ != nil { $log.$method($*_, slog.Any($_, $err), $*_) }; $*_; return $*ret }`,
		`if $*_ { $*_; if $_ != nil { $log.$method($*_, slog.String($_, $err.Error()), $*_) }; $*_; return $*ret }`,
	).
		Where(m["method"].Text.Matches(`^((Debug|Info|Warn|Error)(Context)?|Log|LogAttrs)$`) &&
			m["err"].Type.Implements("error") &&
			m["ret"].Contains(`$err`) &&
			!m.File().Name.Matches(`_test\.go$`) &&
			!m.File().PkgPath.Matches(`/internal/(agent|errlog|errs)(/|$)|/cmd/bridge(/|$)`)).
		Report("RG-RETURN: $err is logged and then returned; return it and let the caller's result record it (docs/errors.md, Result records)")

	// The if block logs and falls through to a return right after it.
	m.Match(
		`if $*_ { $*_; $log.$method($*_, slog.Any($_, $err), $*_); $*rest }; return $*ret`,
		`if $*_ { $*_; $log.$method($*_, slog.String($_, $err.Error()), $*_); $*rest }; return $*ret`,
		`if $*_ { $*_; if $_ != nil { $log.$method($*_, slog.Any($_, $err), $*_) }; $*rest }; return $*ret`,
		`if $*_ { $*_; if $_ != nil { $log.$method($*_, slog.String($_, $err.Error()), $*_) }; $*rest }; return $*ret`,
	).
		Where(m["method"].Text.Matches(`^((Debug|Info|Warn|Error)(Context)?|Log|LogAttrs)$`) &&
			m["err"].Type.Implements("error") &&
			m["ret"].Contains(`$err`) &&
			!m["rest"].Contains(`return $*_`) &&
			!m["rest"].Contains(`continue`) &&
			!m.File().Name.Matches(`_test\.go$`) &&
			!m.File().PkgPath.Matches(`/internal/(agent|errlog|errs)(/|$)|/cmd/bridge(/|$)`)).
		Report("RG-RETURN: $err is logged and then returned; return it and let the caller's result record it (docs/errors.md, Result records)")
}

// errorAsLogAttr reports an error passed to a log record as a plain slog
// attribute. An error reaches a log only through errlog.Finish or
// errlog.Event, which attribute and redact it.
func errorAsLogAttr(m dsl.Matcher) {
	m.Match(`slog.Any($_, $err)`, `slog.String($_, $err.Error())`).
		Where(m["err"].Type.Implements("error") &&
			!m.File().Name.Matches(`_test\.go$`) &&
			!m.File().PkgPath.Matches(`/internal/(errlog|errs)(/|$)|/cmd/bridge(/|$)`)).
		Report("RG-ATTR: error $err as a slog attribute; record it with errlog.Event, or return it to the unit's result record (docs/errors.md, Result records)")
}
