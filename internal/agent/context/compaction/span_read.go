package compaction

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// maxCompactionScanBytes bounds how much candidate payload one pass reads
// while looking for a span past rows that stay raw. Every window is still
// bounded by compactionReadMaxBytes.
const maxCompactionScanBytes = 32 << 20

// spanRead is the outcome of reading candidates until a span is chosen: the
// window holding it and the span itself, plus what the scan cost.
type spanRead struct {
	rows         []sqlc.ListUncompactedMessagesBySessionRow
	span         []CompactionCandidate
	stats        spanStats
	windows      int
	loadedRows   int
	scannedBytes int64
}

// readCompactionSpan reads candidate windows oldest-first until one holds a
// span worth claiming. A window that only holds rows staying raw — protected,
// unrenderable, already proved ineffective, or too small — moves the cursor
// past them, so a span beyond the first window is still reached. A non-empty
// reason reports why nothing can be claimed now.
func (s *Service) readCompactionSpan(ctx context.Context, sessionUUID pgtype.UUID, cfg TriggerConfig, measure sqlc.MeasureUncompactedMessagesBySessionRow, minSpanTokens int) (spanRead, string, error) {
	readMaxBytes := compactionReadMaxBytes(cfg)
	var read spanRead
	var after pgtype.UUID
	// settledThrough is the last row of the prefix found permanently
	// unclaimable. Recording it lets later passes start after it instead of
	// rescanning, so history behind a prefix larger than the scan budget is
	// still reached. It never passes a fresh claim, whose rows come back as
	// candidates if the claim lapses.
	var settledThrough pgtype.UUID
	var scanEpoch int64
	settling := true
	defer func() {
		if !settledThrough.Valid {
			return
		}
		if err := s.queries.AdvanceCompactionScan(ctx, sqlc.AdvanceCompactionScanParams{
			SessionID:       sessionUUID,
			AfterMessageID:  settledThrough,
			CompactionEpoch: scanEpoch,
		}); err != nil {
			s.logger.WarnContext(ctx, "compaction: record scan position failed",
				slog.String("session_id", cfg.SessionID), slog.Any("error", err))
		}
	}()
	for {
		window, err := s.queries.ListUncompactedMessagesBySessionWithinBytes(ctx, sqlc.ListUncompactedMessagesBySessionWithinBytesParams{
			SessionID:                sessionUUID,
			MaxBytes:                 readMaxBytes,
			AfterMessageID:           after,
			IneffectiveFailureReason: failureReasonIneffectiveSummary,
		})
		if err != nil {
			return spanRead{}, "", err
		}
		if len(window) == 0 {
			s.logger.WarnContext(ctx, "compaction: no candidate fits the database read budget",
				slog.String("session_id", cfg.SessionID),
				slog.Int("window", read.windows),
				slog.Int64("candidate_count", measure.CandidateCount),
				slog.Int64("candidate_bytes", measure.CandidateBytes),
				slog.Int64("largest_candidate_bytes", measure.LargestCandidateBytes),
				slog.Int64("read_max_bytes", readMaxBytes))
			return read, ReasonReadBudgetExceeded, nil
		}
		// Use the count captured by the same SQL statement as the payload rows
		// for the selection decision. The metadata-only measure is
		// observability and oversized-head handling; concurrent claims may
		// legitimately change eligibility between the two statements.
		truncated := window[0].CandidateCount > int64(len(window))
		loadedBytes := window[len(window)-1].CumulativeBytes
		read.windows++
		read.loadedRows += len(window)
		read.scannedBytes += loadedBytes
		s.logger.InfoContext(ctx, "compaction: bounded candidate read",
			slog.String("session_id", cfg.SessionID),
			slog.Int("window", read.windows),
			slog.Int("loaded_messages", len(window)),
			slog.Int64("candidate_count", window[0].CandidateCount),
			slog.Int64("candidate_bytes", window[0].CandidateBytes),
			slog.Int64("loaded_bytes", loadedBytes),
			slog.Int64("read_max_bytes", readMaxBytes),
			slog.Bool("truncated", truncated))

		rows, items, barrierCount := itemsFromWindow(window)
		if barrierCount > 0 {
			s.logger.WarnContext(ctx, "compaction: kept unparseable history rows as span barriers",
				slog.Int("barrier_count", barrierCount),
				slog.String("session_id", cfg.SessionID),
			)
		}
		var toCompact []CompactionCandidate
		switch {
		case truncated:
			// Rows are an oldest-first window with more history behind it.
			// Compact within the window; splitByTarget could noop forever
			// because the unseen newest tail is precisely what should be kept.
			toCompact = items
		case cfg.TargetTokens > 0:
			// Keep the newest messages that fit TargetTokens and compact
			// everything older.
			toCompact = splitByTarget(items, cfg.TargetTokens)
		default:
			toCompact = splitByRatio(items, cfg.TotalInputTokens, cfg.Ratio)
		}
		if len(toCompact) == 0 {
			// Rows passed over — in an earlier window or behind the recorded
			// scan position — are history that cannot shrink, not nothing.
			if read.windows > 1 || measure.CandidateCount > window[0].CandidateCount {
				return read, ReasonNoBeneficialSpan, nil
			}
			return read, ReasonNothingToCompact, nil
		}

		choice := chooseSpan(toCompact, minSpanTokens, truncated)
		read.stats.add(choice.stats)
		if settling {
			settled := 0
			if toCompact[0].ID == items[0].ID {
				settled = choice.settled
			}
			for i := 0; i < settled; i++ {
				if window[i].PendingBefore {
					settled, settling = i, false
				}
			}
			if settled > 0 {
				settledThrough, scanEpoch = rows[settled-1].ID, rows[settled-1].CompactionEpoch
			}
			settling = settling && settled == choice.resume
		}
		if choice.end > choice.start {
			read.rows = rows
			read.span = toCompact[choice.start:choice.end]
			return read, "", nil
		}
		if !truncated {
			return read, ReasonNoBeneficialSpan, nil
		}
		if read.scannedBytes >= maxCompactionScanBytes {
			return read, ReasonReadBudgetExceeded, nil
		}
		after = rows[choice.resume-1].ID
	}
}

func (r spanRead) attrs(cfg TriggerConfig, reason string) []slog.Attr {
	return []slog.Attr{
		slog.String("session_id", cfg.SessionID),
		slog.String("reason", reason),
		slog.Int("windows", r.windows),
		slog.Int("loaded_messages", r.loadedRows),
		slog.Int64("scanned_bytes", r.scannedBytes),
		slog.Int("must_keep_groups", r.stats.MustKeepGroups),
		slog.Int("orphan_result_groups", r.stats.OrphanResultGroups),
		slog.Int("unrendered_groups", r.stats.UnrenderedGroups),
		slog.Int("open_groups", r.stats.OpenGroups),
		slog.Int("gaps", r.stats.Gaps),
		slog.Int("small_spans", r.stats.SmallSpans),
		slog.Int("ineffective_spans", r.stats.IneffectiveSpans),
	}
}
