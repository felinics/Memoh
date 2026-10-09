package compaction

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// maxCompactionScanBytes and maxCompactionScanWindows bound how much one
// pass reads while looking for a span past rows that stay raw. Every window is
// still bounded by compactionReadMaxBytes, and the next pass continues from
// the recorded scan position.
const (
	maxCompactionScanBytes   = 32 << 20
	maxCompactionScanWindows = 64
)

// spanRead is the outcome of reading candidates until a span is chosen: the
// window holding it and the span itself, plus what the scan cost.
type spanRead struct {
	rows         []sqlc.ListUncompactedMessagesBySessionRow
	span         []CompactionCandidate
	stats        spanStats
	windows      int
	loadedRows   int
	oversized    int
	scannedBytes int64
}

// readCompactionSpan reads candidate windows oldest-first until one holds a
// span worth claiming. A window that only holds rows staying raw — protected,
// unrenderable, already proved ineffective, too small, or too large to read —
// moves the cursor past them, so a span beyond the first window is still
// reached. A non-empty reason reports why nothing can be claimed now.
func (s *Service) readCompactionSpan(ctx context.Context, sessionUUID pgtype.UUID, cfg TriggerConfig, measure sqlc.MeasureUncompactedMessagesBySessionRow, minSpanTokens, minBudget int) (spanRead, string, error) {
	readMaxBytes := compactionReadMaxBytes(cfg)
	var read spanRead
	var after pgtype.UUID
	// settledThrough is the last row of the prefix found permanently
	// unclaimable. Recording it lets later passes start after it instead of
	// rescanning, so history behind a prefix larger than the scan budget is
	// still reached. It never passes a fresh claim, whose rows come back as
	// candidates if the claim lapses, nor the current task.
	var settledThrough pgtype.UUID
	var scanEpoch int64
	settling := true
	settle := func(row sqlc.ListUncompactedMessagesBySessionRow) {
		settledThrough, scanEpoch = row.ID, row.CompactionEpoch
	}
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
		if read.windows >= maxCompactionScanWindows || read.scannedBytes >= maxCompactionScanBytes {
			return read, ReasonReadBudgetExceeded, nil
		}
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
			if read.windows > 0 || measure.CandidateCount > 0 {
				return read, ReasonNoBeneficialSpan, nil
			}
			return read, ReasonNothingToCompact, nil
		}
		read.windows++
		truncated := window[0].CandidateCount > int64(len(window))
		if window[0].Oversized {
			// Larger than a whole window: it stays raw, and the cursor moves
			// past it so the history behind it is still reached.
			read.oversized++
			s.logger.WarnContext(ctx, "compaction: candidate larger than the read window stays raw",
				slog.String("session_id", cfg.SessionID),
				slog.String("message_id", formatUUID(window[0].ID)),
				slog.Int64("read_max_bytes", readMaxBytes))
			row := uncompactedRowsFromBounded(window)[0]
			if window[0].PendingBefore || window[0].LatestUser {
				settling = false
			} else if settling {
				settle(row)
			}
			if !truncated {
				return read, ReasonReadBudgetExceeded, nil
			}
			after = row.ID
			continue
		}
		// Use the count captured by the same SQL statement as the payload rows
		// for the selection decision. The metadata-only measure is
		// observability; concurrent claims may legitimately change eligibility
		// between the two statements.
		loadedBytes := window[len(window)-1].CumulativeBytes
		read.loadedRows += len(window)
		read.scannedBytes += loadedBytes
		level := slog.LevelInfo
		if read.windows > 1 {
			level = slog.LevelDebug
		}
		s.logger.Log(ctx, level, "compaction: bounded candidate read",
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
			// An oldest-first window with more history behind it: the recent
			// tail lies beyond it, so only the current task is held back.
			// splitByTarget could noop forever because the unseen newest tail
			// is precisely what should be kept.
			for i := range items {
				items[i].Policies = withoutPolicy(items[i].Policies, CompactPolicyPreserveRecent)
				if window[i].LatestUser {
					items[i].Policies = appendPolicy(items[i].Policies, CompactPolicyPreserveRecent)
				}
			}
			toCompact = items
		default:
			toCompact = splitRecent(items, cfg)
		}

		choice := chooseSpan(toCompact, minSpanTokens, minBudget, truncated)
		read.stats.add(choice.stats)
		if settling {
			settled := 0
			if len(toCompact) > 0 && toCompact[0].ID == items[0].ID {
				settled = choice.settled
			}
			for i := 0; i < settled; i++ {
				if window[i].PendingBefore {
					settled, settling = i, false
				}
			}
			if settled > 0 {
				settle(rows[settled-1])
			}
			settling = settling && settled == choice.resume
		}
		if choice.end > choice.start {
			read.rows = rows
			read.span = toCompact[choice.start:choice.end]
			return read, "", nil
		}
		if !truncated {
			// Nothing older than the current turn can be claimed: the turn's
			// own older steps may still be, behind its task message.
			if turn := currentTurnItems(items); len(turn) > 0 {
				steps := splitRecent(turn, cfg)
				if choice := chooseSpan(steps, minSpanTokens, minBudget, false); choice.end > choice.start {
					read.rows = rows
					read.span = steps[choice.start:choice.end]
					return read, "", nil
				}
			}
			// Rows passed over — in an earlier window or behind the recorded
			// scan position — are history that cannot shrink, not nothing.
			switch {
			case read.oversized > 0:
				return read, ReasonReadBudgetExceeded, nil
			case len(toCompact) == 0 && read.windows == 1 && measure.CandidateCount <= window[0].CandidateCount:
				return read, ReasonNothingToCompact, nil
			}
			return read, ReasonNoBeneficialSpan, nil
		}
		after = rows[choice.resume-1].ID
	}
}

// splitRecent keeps the newest candidates that fit the trigger's target (or
// ratio) and returns the older ones a claim may come from.
func splitRecent(items []CompactionCandidate, cfg TriggerConfig) []CompactionCandidate {
	if cfg.TargetTokens > 0 {
		return splitByTarget(items, cfg.TargetTokens)
	}
	return splitByRatio(items, cfg.TotalInputTokens, cfg.Ratio)
}

// currentTurnItems returns the current turn — its task message and every
// step after it — with selection policies derived as if the turn were all
// that is left, so its task message and latest step stay raw while its older
// steps become claimable. Nil when the turn already starts the window.
func currentTurnItems(items []CompactionCandidate) []CompactionCandidate {
	latest := latestUserIndex(items)
	if latest <= 0 {
		return nil
	}
	turn := make([]CompactionCandidate, len(items)-latest)
	for i, item := range items[latest:] {
		item.Policies = withoutPolicy(item.Policies, CompactPolicyPreserveRecent)
		turn[i] = item
	}
	markSelectionPolicies(turn)
	return turn
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
		slog.Int("oversized_rows", r.oversized),
	}
}
