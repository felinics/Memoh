// Package errs wraps internal errors and analyzes them at a boundary.
//
// It is the diagnostic layer only. Public errors are apperror values, which
// carry the catalog code clients branch on; package errors are sentinels that
// callers compare with errors.Is. errs adds what a boundary needs to write one
// useful record per unit of work: where the error was produced (a captured
// stack), who is at fault, structured attributes, and a redacted rendering of
// the whole chain.
//
// Traversal follows Unwrap() error, Unwrap() []error and, for errors that
// implement only Cause() error, Cause(). apperror keeps its cause out of
// Unwrap on purpose, so errors.Is and errors.As do not cross a public error;
// Cause() lets diagnostics cross it without changing that.
package errs
