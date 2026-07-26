// Package persistence defines the Agent application read ports implemented by
// owner-private adapters, separately from the application service that
// consumes them.
package persistence

import "context"

// LatestSessionModelReader supplies the model used by the latest persisted session round.
type LatestSessionModelReader interface {
	LatestSessionModelID(context.Context, string) (string, error)
}

// CompactionMessageRefReader supplies legacy message coverage for a compaction artifact.
type CompactionMessageRefReader interface {
	ListCompactionMessageRefs(context.Context, string) ([]string, error)
}

// Reads bundles the application read ports one adapter satisfies together, so
// composition roots can hold the bundle instead of the owner-private adapter
// type that implements it.
type Reads interface {
	LatestSessionModelReader
	CompactionMessageRefReader
}
