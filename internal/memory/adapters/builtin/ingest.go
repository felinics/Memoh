package builtin

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/memory/migrate"
	storefs "github.com/felinics/memoh/internal/memory/storefs"
)

var (
	errGraphBotIDRequired      = errors.New("graph ingest: bot_id is required")
	errGraphStoreNotConfigured = errors.New("graph ingest: wiki store not configured")
)

// IngestResult reports the outcome of a file→DB memory ingest pass.
type IngestResult struct {
	// Ingested is the number of memory nodes written to the wiki store
	// (inserts + updates; re-ingesting an unchanged file counts as an update).
	Ingested int `json:"ingested"`
	// Skipped is the number of source items that parsed to empty content or
	// failed to persist and were therefore not counted as ingested.
	Skipped int `json:"skipped"`
}

// IngestMarkdownFiles imports agent-authored /data/memory/*.md into the wiki
// store as DB nodes, closing the gap left by the DB→file derived-view sync
// (syncMarkdownFromNodes only writes files FROM nodes; it never reads files
// back into the DB).
//
// It is the file→DB direction. Direct agent writes (write_file to /data/memory)
// are otherwise invisible to search_memory (which reads DB nodes); RebuildFiles
// preserves them (it only deletes bot-prefixed projection files), but they stay
// unsearchable until ingested here.
//
// Idempotency: items keep their YAML frontmatter `id` verbatim; items missing
// an id get a deterministic synthesised id (see storefs.ReadAllMemoryFilesForIngest).
// UpsertNode is ON CONFLICT(id) DO UPDATE, so re-ingesting the same files is a
// no-op at the row level. This deliberately does NOT route through
// graphRuntime.Add / runtimeMemoryID, whose nanosecond+sequence ids would
// create a duplicate node on every run.
//
// After upserting nodes the derived edges (same_profile/same_topic/same_day/
// refs) are rebuilt and the Markdown view is regenerated from the now-
// authoritative node set, so files and DB converge to a single source of truth.
func (r *graphRuntime) IngestMarkdownFiles(ctx context.Context, botID string) (IngestResult, error) {
	botID = strings.TrimSpace(botID)
	if botID == "" {
		return IngestResult{}, errGraphBotIDRequired
	}
	if r.store == nil {
		return IngestResult{}, errGraphStoreNotConfigured
	}
	items, err := r.fs.ReadAllMemoryFilesForIngest(ctx, botID)
	if err != nil {
		return IngestResult{}, err
	}
	result := IngestResult{}
	now := time.Now().UTC()
	for _, item := range items {
		if strings.TrimSpace(item.Memory) == "" {
			result.Skipped++
			continue
		}
		// Normalise the id to the canonical "<botID>:<localId>" shape used by
		// runtimeMemoryID, so Update/Delete (which extract the bot id from the
		// memory id) work on ingested nodes regardless of whether the agent wrote
		// a frontmatter id, a synthesised one, or omitted it.
		item.ID = qualifyIngestID(botID, item.ID)
		spec := nodeSpecFromIngestItem(botID, now, item)
		if _, err := r.store.UpsertNode(ctx, spec); err != nil {
			r.logger.Warn("graph ingest: upsert node failed", slog.String("bot_id", botID), slog.String("node_id", spec.ID), slog.Any("err", err))
			result.Skipped++
			continue
		}
		if r.semantic != nil {
			r.semanticUpsertBestEffort(botID, spec) //nolint:contextcheck // async semantic upsert uses its own bounded context
		}
		result.Ingested++
	}
	// Rebuild derived edges + regenerate the derived Markdown view from the
	// authoritative node set, so the file tree and DB converge.
	r.syncAndInvalidate(ctx, botID)
	return result, nil
}

// qualifyIngestID normalises an ingested memory id to the canonical
// "<botID>:<localId>" shape. Agent-authored frontmatter ids and synthesised ids
// may omit the bot prefix; without it, Update/Delete cannot recover the bot id
// from the memory id and reject the operation. Already-qualified ids pass through
// unchanged; the bot prefix is stripped first to avoid double-prefixing.
func qualifyIngestID(botID, id string) string {
	botID = strings.TrimSpace(botID)
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	// Strip any existing "<botID>:" prefix (or any prefix before ":") so we never
	// produce "bot:bot:local".
	if idx := strings.Index(id, ":"); idx >= 0 {
		id = id[idx+1:]
	}
	return botID + ":" + id
}

// nodeSpecFromIngestItem converts a parsed memory item into a wiki NodeSpec via
// the shared migrate.Plan classification (so layer/subject/confidence/topic are
// derived consistently with the chat-extract path), then fills change-detection
// hash and capture time the way runtime.Add does, while preserving the item's
// (possibly synthesised) id so re-ingest is idempotent.
func nodeSpecFromIngestItem(botID string, now time.Time, item storefs.MemoryItem) migrate.NodeSpec {
	nodes, _ := migrate.Plan(botID, []storefs.MemoryItem{item})
	if len(nodes) == 0 {
		return migrate.NodeSpec{ID: item.ID, BotID: botID, Body: strings.TrimSpace(item.Memory), Layer: migrate.LayerNote, CapturedAt: now}
	}
	spec := nodes[0]
	if strings.TrimSpace(spec.Hash) == "" {
		spec.Hash = runtimeHash(spec.Body)
	}
	if spec.CapturedAt.IsZero() {
		spec.CapturedAt = now
	}
	return spec
}
