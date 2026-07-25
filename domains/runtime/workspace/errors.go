package workspace

import "errors"

// ErrWorkspaceTemplateBootstrapFailed identifies a workspace that started but
// failed template bootstrap / identity migration.
var ErrWorkspaceTemplateBootstrapFailed = errors.New("workspace template bootstrap failed")

// ErrTransactionsRequired reports that an atomic Workspace persistence
// operation was configured without a transaction beginner.
var ErrTransactionsRequired = errors.New("workspace persistence requires transactions")

// ErrRecordNotFound is returned by Workspace persistence adapters when an
// owner record does not exist. Consumers translate it into use-case errors.
var ErrRecordNotFound = errors.New("workspace record not found")
