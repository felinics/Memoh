// Package assembly owns Server-side construction of Model health and other
// cross-cutting Model adapters that must return project-root contracts.
//
// cmd/** must import this package (or other public domains/model leaves) and
// must not reach into domains/model/internal.
package assembly
