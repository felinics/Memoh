package builtin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	adapters "github.com/felinics/memoh/internal/memory/adapters"
	"github.com/felinics/memoh/internal/memory/migrate"
	storefs "github.com/felinics/memoh/internal/memory/storefs"
)

// graphSync regenerates the agent-facing Markdown derived view for a bot from
// its PG wiki nodes. The PG graph is the source of truth; the Markdown files
// exist purely so the agent (which reads /data/memory/<layer>/<slug>.md via
// real open() syscalls inside its container) sees the same content.
//
// Syncs are serialized per botID so concurrent writes do not race on the same
// memory bundle. Failures are best-effort: PG remains authoritative, so a
// sync error is logged, not returned to the caller (graphRuntime swallows it).
type graphSync struct {
	fs     memoryStore
	logger *slog.Logger

	mu    sync.Mutex             // guards locks map
	locks map[string]*sync.Mutex // per-botID serialization
}

func newGraphSync(fs memoryStore, logger *slog.Logger) *graphSync {
	if logger == nil {
		logger = slog.Default()
	}
	return &graphSync{fs: fs, logger: logger, locks: make(map[string]*sync.Mutex)}
}

func (s *graphSync) botLock(botID string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.locks[botID]
	if !ok {
		m = &sync.Mutex{}
		s.locks[botID] = m
	}
	return m
}

// syncMarkdownFromNodes regenerates /data/memory/<layer>/<slug>.md + MEMORY.md
// for botID from the given node set. It is the DB→file direction only.
//
// The file→DB direction (agent-authored edits under /data/memory) is NOT
// automatic: agents that write markdown directly bypass the wiki store, so
// those files are invisible to search_memory until they are ingested. Use
// graphRuntime.IngestMarkdownFiles (exposed as POST /bots/:bot_id/memory/ingest
// via the MarkdownIngestProvider interface) to import them as DB nodes.
// storefs.RebuildFiles only deletes files it recognizes as DB projections
// (every entry id prefixed "<botID>:"), so un-ingested agent-authored files
// survive a sync rather than being wiped.
//
// The caller passes the authoritative node list (already read from the store);
// this avoids a second ListNodes round-trip and lets the caller hold the most
// recent post-write state.
func (s *graphSync) syncMarkdownFromNodes(ctx context.Context, botID string, nodes []migrate.NodeSpec) error {
	if s.fs == nil {
		return errors.New("graph sync: filesystem store not configured")
	}
	botID = strings.TrimSpace(botID)
	if botID == "" {
		return errors.New("graph sync: bot_id is required")
	}

	// Serialize per-bot so two concurrent writes do not clobber the same file.
	mu := s.botLock(botID)
	mu.Lock()
	defer mu.Unlock()

	items := make([]storefs.MemoryItem, 0, len(nodes))
	for _, n := range nodes {
		items = append(items, nodeSpecToStoreItem(n))
	}
	filters := map[string]any{
		"namespace": sharedMemoryNamespace,
		"scopeId":   botID,
		"bot_id":    botID,
	}
	if err := s.fs.RebuildFiles(ctx, botID, items, filters); err != nil {
		return fmt.Errorf("graph sync: rebuild files: %w", err)
	}
	if err := s.fs.SyncOverview(ctx, botID); err != nil {
		return fmt.Errorf("graph sync: sync overview: %w", err)
	}
	return nil
}

// nodeSpecToStoreItem converts a wiki node into the storefs item shape so the
// canonical Markdown serializer (storefs) renders layer/fact_type/subject/
// confidence/profile_ref/topic into the YAML front matter. Metadata is the
// free-form channel that survives the round-trip through memoryEntryMeta.
func nodeSpecToStoreItem(n migrate.NodeSpec) storefs.MemoryItem {
	meta := buildNodeMetadata(n)
	return storefs.MemoryItem{
		ID:        n.ID,
		Memory:    n.Body,
		Hash:      n.Hash,
		CreatedAt: formatNodeTime(n.CapturedAt),
		UpdatedAt: formatNodeTime(n.CapturedAt),
		Metadata:  meta,
		BotID:     n.BotID,
	}
}

// buildNodeMetadata merges the node's structured fields into its metadata map
// so they round-trip through the Markdown YAML front matter and back.
func buildNodeMetadata(n migrate.NodeSpec) map[string]any {
	meta := map[string]any{}
	for k, v := range n.Metadata {
		meta[k] = v
	}
	if n.Layer != "" {
		meta["layer"] = string(n.Layer)
	}
	if n.FactType != "" {
		meta["fact_type"] = n.FactType
	}
	if n.Subject != "" {
		meta["subject"] = n.Subject
	}
	if n.Confidence != 0 {
		meta["confidence"] = n.Confidence
	}
	if n.ProfileRef != "" {
		meta["profile_ref"] = n.ProfileRef
	}
	if n.Topic != "" {
		meta["topic"] = n.Topic
	}
	return meta
}

func formatNodeTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// nodeSpecToMemoryItem converts a wiki node into the cross-package MemoryItem
// shape used by the Runtime interface and search responses.
func nodeSpecToMemoryItem(n migrate.NodeSpec) adapters.MemoryItem {
	return adapters.MemoryItem{
		ID:               n.ID,
		Memory:           n.Body,
		Hash:             n.Hash,
		CreatedAt:        formatNodeTime(n.CapturedAt),
		UpdatedAt:        formatNodeTime(n.CapturedAt),
		Score:            0,
		Metadata:         buildNodeMetadata(n),
		BotID:            n.BotID,
		SourceMessageIDs: adapters.NormalizeSourceRefs(n.SourceMessageIDs),
	}
}
