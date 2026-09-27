// Package errlog finishes a unit of work that failed: it decides the level of
// the result record, marks the span and counts errors without a recorded
// origin, from one errs.Report.
//
// It does not write the record. The boundary logs it with its own logger and
// a context-taking call, so the correlation handler in internal/logger adds
// request_id, trace_id and span_id:
//
//	result := errlog.Finish(ctx, "http.request", err, errlog.Options{})
//	logger.LogAttrs(ctx, result.Level, "request failed", result.Attrs()...)
package errlog
