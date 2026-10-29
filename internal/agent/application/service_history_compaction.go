package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/agent/context/compaction"
	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/db"
)

func historyContextFragsForMessages(messages []ModelMessage, records []historyfrag.HistoryRecord) []contextfrag.ContextFrag {
	if len(messages) == 0 || len(records) == 0 {
		return nil
	}
	frags := make([]contextfrag.ContextFrag, 0)
	recordStart := 0
	for i, msg := range messages {
		if !looksLikeSummaryMessage(msg) {
			continue
		}
		for j := recordStart; j < len(records); j++ {
			record := records[j]
			if record.Kind != contextfrag.KindConversationSummary || record.Coverage == nil {
				continue
			}
			if !sameModelMessage(record.ModelMessage, msg) {
				continue
			}
			frag := historyfrag.ToFrag(record)
			frag.ID = fmt.Sprintf("message.%03d", i)
			frag.Provenance.Index = i
			frags = append(frags, frag)
			recordStart = j + 1
			break
		}
	}
	return frags
}

func looksLikeSummaryMessage(msg ModelMessage) bool {
	return strings.EqualFold(strings.TrimSpace(msg.Role), "user") &&
		strings.HasPrefix(strings.TrimSpace(msg.TextContent()), "<summary>")
}

func sameModelMessage(a ModelMessage, b ModelMessage) bool {
	return strings.EqualFold(strings.TrimSpace(a.Role), strings.TrimSpace(b.Role)) &&
		string(a.Content) == string(b.Content)
}

func historySourceMessageIDsForMessages(messages []ModelMessage, records []historyfrag.HistoryRecord) []string {
	sourceIDs := make([]string, len(messages))
	for i, index := range matchHistoryRecords(messages, records) {
		if index >= 0 {
			sourceIDs[i] = strings.TrimSpace(records[index].DBMessageID)
		}
	}
	return sourceIDs
}

// contextProvenance names what each message of a request renders, for a run
// that compacts in place: the inbound message or row a message carries as
// its source, or else the artifact a summary renders. sent is messages in
// provider form. A message that renders the request's own input is an anchor.
func contextProvenance(messages []ModelMessage, sent []sdk.Message, records []historyfrag.HistoryRecord, inputs []turn.ContextMessageSource) []native.MessageProvenance {
	matched := matchHistoryRecords(messages, records)
	var out []native.MessageProvenance
	for i, message := range messages {
		var source turn.ContextMessageSource
		switch index := matched[i]; {
		case message.ContextSource != nil:
			source = *message.ContextSource
		case index >= 0:
			source = turn.ContextMessageSource{Kind: "summary", ID: records[index].Ref.ID}
		default:
			continue
		}
		if source.Kind == "history" && source.ContentHash == "" && matched[i] >= 0 {
			source.ContentHash = historyfrag.MessageContentHash(records[matched[i]].ModelMessage)
		}
		anchor := source.Current || slices.ContainsFunc(inputs, func(input turn.ContextMessageSource) bool {
			return input.Kind == source.Kind && input.ID == source.ID
		})
		source.Current = false
		out = append(out, native.MessageProvenance{
			Message: sent[i],
			Sources: []turn.ContextMessageSource{source},
			Anchor:  anchor,
		})
	}
	return out
}

// matchHistoryRecords pairs each message with the index of the stored record
// it was loaded from, or -1. A message loaded from a row names that row as
// its source, however a replay projection rewrote it; a summary names none
// and is matched, in order, to the artifact whose rendering it carries.
func matchHistoryRecords(messages []ModelMessage, records []historyfrag.HistoryRecord) []int {
	rows := make(map[string]int, len(records))
	summaries := make(map[string][]int)
	for i, record := range records {
		if record.SourceKind == historyfrag.SourceCompactionLog {
			key := summaryMatchKey(record.ModelMessage)
			summaries[key] = append(summaries[key], i)
			continue
		}
		if id := strings.TrimSpace(record.DBMessageID); id != "" {
			if _, seen := rows[id]; !seen {
				rows[id] = i
			}
		}
	}
	matched := make([]int, len(messages))
	for i, message := range messages {
		matched[i] = -1
		if source := message.ContextSource; source != nil && source.Kind == "history" {
			if index, ok := rows[strings.TrimSpace(source.ID)]; ok {
				matched[i] = index
			}
			continue
		}
		if len(summaries) == 0 {
			continue
		}
		key := summaryMatchKey(message)
		if candidates := summaries[key]; len(candidates) > 0 {
			matched[i] = candidates[0]
			summaries[key] = candidates[1:]
		}
	}
	return matched
}

func summaryMatchKey(message ModelMessage) string {
	return strings.ToLower(strings.TrimSpace(message.Role)) + "\x00" + string(message.Content)
}

type compactionArtifactBoundary struct {
	hasCutoff bool
	cutoffMs  int64
}

func compactionArtifactBoundaryBeforeMessage(messages []historyfrag.HistoryRecord, messageID string) compactionArtifactBoundary {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return compactionArtifactBoundary{}
	}
	boundary := compactionArtifactBoundary{hasCutoff: true}
	for _, message := range messages {
		if strings.TrimSpace(message.DBMessageID) == messageID && !message.CreatedAt.IsZero() {
			boundary.cutoffMs = message.CreatedAt.UnixMilli()
			break
		}
	}
	return boundary
}

func (s *Service) loadCompactionArtifactBoundary(ctx context.Context, messages []historyfrag.HistoryRecord, sessionID, messageID string) compactionArtifactBoundary {
	boundary := compactionArtifactBoundaryBeforeMessage(messages, messageID)
	if !boundary.hasCutoff || boundary.cutoffMs > 0 || s == nil || s.messageService == nil || strings.TrimSpace(sessionID) == "" {
		return boundary
	}
	message, err := s.messageService.GetByIDBySession(ctx, sessionID, messageID)
	if err == nil && !message.CreatedAt.IsZero() {
		boundary.cutoffMs = message.CreatedAt.UnixMilli()
	}
	return boundary
}

func (b compactionArtifactBoundary) includes(artifact compaction.Artifact) bool {
	if !b.hasCutoff {
		return true
	}
	return b.cutoffMs > 0 &&
		artifact.AnchorStartMs > 0 &&
		artifact.AnchorStartMs <= artifact.AnchorEndMs &&
		artifact.AnchorEndMs < b.cutoffMs
}

func (s *Service) replaceCompactedMessages(ctx context.Context, sessionID string, scope contextfrag.Scope, messages []historyfrag.HistoryRecord, boundary compactionArtifactBoundary) ([]historyfrag.HistoryRecord, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, nil
	}
	if s.queries == nil {
		return messages, nil
	}
	frontier, err := s.loadActiveCompactionFrontier(ctx, scope.BotID, sessionID)
	if err != nil {
		return nil, err
	}
	if len(frontier.Artifacts) == 0 {
		return messages, requireSummariesRendered(messages, func(historyfrag.HistoryRecord) (compaction.Artifact, bool) { return compaction.Artifact{}, false })
	}
	owner := compaction.ArtifactOwner{BotID: scope.BotID, SessionID: sessionID, SessionIDKnown: true}
	catalog := compaction.NewArtifactCatalog()
	catalog.Add(owner, frontier)
	blocked := conflictingArtifactIDs(catalog, messages, scope)
	resolve := func(record historyfrag.HistoryRecord) (compaction.Artifact, bool) {
		artifact, ok := resolveCatalogArtifact(catalog, recordArtifactOwner(record, scope), record)
		if !ok || !boundary.includes(artifact) {
			return compaction.Artifact{}, false
		}
		if _, conflict := blocked[artifact.ID]; conflict {
			return compaction.Artifact{}, false
		}
		return artifact, true
	}
	if err := requireSummariesRendered(messages, resolve); err != nil {
		return nil, err
	}
	messages = replaceCompactedHistoryRecordsWithService(messages, scope, resolve)
	sessionSummaries := summaryRecordsFromArtifacts(missingCompactionArtifacts(messages, frontier.Artifacts, blocked, boundary), scope)
	if len(sessionSummaries) > 0 {
		messages = mergeMissingCompactionSummaries(messages, sessionSummaries)
	}
	return s.refreshCompactedSummaryCoverage(ctx, messages, resolve), nil
}

// errSummaryNotRendered reports a Summarized row whose summary does not
// render in the request: the window left out the rest of its compact group,
// so the caller loads the window again without folding.
var errSummaryNotRendered = errors.New("history: a folded compact group's summary does not render")

func requireSummariesRendered(messages []historyfrag.HistoryRecord, resolve func(historyfrag.HistoryRecord) (compaction.Artifact, bool)) error {
	for _, record := range messages {
		if !record.Summarized {
			continue
		}
		if artifact, ok := resolve(record); !ok || strings.TrimSpace(artifact.Summary) == "" {
			return fmt.Errorf("%w: compact %s", errSummaryNotRendered, record.CompactID)
		}
	}
	return nil
}

// missingCompactionArtifacts filters the active frontier down to summaries not
// already represented in the loaded records.
func missingCompactionArtifacts(messages []historyfrag.HistoryRecord, artifacts []compaction.Artifact, blocked map[string]struct{}, boundary compactionArtifactBoundary) []compaction.Artifact {
	seen := make(map[string]struct{}, len(messages))
	for _, record := range messages {
		if record.SourceKind == historyfrag.SourceCompactionLog {
			if id := strings.TrimSpace(record.Ref.ID); id != "" {
				seen[id] = struct{}{}
			}
		}
	}
	missing := make([]compaction.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if !boundary.includes(artifact) {
			continue
		}
		if _, conflict := blocked[artifact.ID]; conflict {
			continue
		}
		if _, ok := seen[artifact.ID]; ok {
			continue
		}
		missing = append(missing, artifact)
	}
	return missing
}

func resolveCatalogArtifact(catalog *compaction.ArtifactCatalog, owner compaction.ArtifactOwner, record historyfrag.HistoryRecord) (compaction.Artifact, bool) {
	if compactID := strings.TrimSpace(record.CompactID); compactID != "" {
		if artifact, ok := catalog.Resolve(owner, compactID); ok {
			isSummary := record.SourceKind == historyfrag.SourceCompactionLog && record.Kind == contextfrag.KindConversationSummary
			if isSummary || (!artifact.CoverageMalformed && len(artifact.Coverage) == 0) || artifact.CoversRecord(record) {
				return artifact, true
			}
		}
	}
	artifact, ok := catalog.ResolveCoverageIdentity(owner, record.Ref)
	if !ok || !artifact.CoversRecord(record) {
		return compaction.Artifact{}, false
	}
	return artifact, true
}

func conflictingArtifactIDs(catalog *compaction.ArtifactCatalog, messages []historyfrag.HistoryRecord, fallback contextfrag.Scope) map[string]struct{} {
	blocked := make(map[string]struct{})
	for _, record := range messages {
		if record.SourceKind == historyfrag.SourceCompactionLog {
			continue
		}
		owner := recordArtifactOwner(record, fallback)
		check := func(artifact compaction.Artifact, ok bool) {
			if !ok || len(artifact.Coverage) == 0 || artifact.CoversRecord(record) {
				return
			}
			blocked[artifact.ID] = struct{}{}
		}
		if compactID := strings.TrimSpace(record.CompactID); compactID != "" {
			check(catalog.Resolve(owner, compactID))
		}
		check(catalog.ResolveCoverageIdentity(owner, record.Ref))
	}
	return blocked
}

func (s *Service) loadActiveCompactionFrontier(ctx context.Context, botID, sessionID string) (compaction.ArtifactFrontier, error) {
	frontier, err := compaction.NewArtifactProjection(s.queries).LoadActiveSession(ctx, compaction.ArtifactOwner{BotID: botID, SessionID: sessionID, SessionIDKnown: true})
	if err != nil {
		return compaction.ArtifactFrontier{}, err
	}
	for _, issue := range frontier.Issues {
		if s.logger != nil {
			s.logger.WarnContext(ctx, "loadActiveCompactionFrontier: ignored invalid lineage", slog.String("issue", issue.Error()))
		}
	}
	for _, artifact := range frontier.Artifacts {
		if artifact.CoverageMalformed && s.logger != nil {
			s.logger.WarnContext(ctx, "loadActiveCompactionFrontier: malformed coverage requires legacy backfill", slog.String("compact_id", artifact.ID))
		}
	}
	return frontier, nil
}

func recordArtifactOwner(record historyfrag.HistoryRecord, fallback contextfrag.Scope) compaction.ArtifactOwner {
	sessionID := strings.TrimSpace(record.SessionID)
	sessionIDKnown := record.SessionIDKnown || sessionID != ""
	if !sessionIDKnown {
		sessionID = strings.TrimSpace(fallback.SessionID)
		sessionIDKnown = sessionID != ""
	}
	return compaction.ArtifactOwner{
		BotID:          firstNonEmpty(strings.TrimSpace(record.BotID), strings.TrimSpace(fallback.BotID)),
		SessionID:      sessionID,
		SessionIDKnown: sessionIDKnown,
	}
}

func summaryRecordsFromArtifacts(artifacts []compaction.Artifact, scope contextfrag.Scope) []historyfrag.HistoryRecord {
	if len(artifacts) == 0 {
		return nil
	}
	records := make([]historyfrag.HistoryRecord, 0, len(artifacts))
	for _, artifact := range artifacts {
		records = append(records, artifact.HistoryRecord(scope))
	}
	return records
}

// refreshCompactedSummaryCoverage backfills legacy summaries that predate
// persisted artifact coverage. New artifacts already carry immutable refs and
// avoid this per-summary query.
func (s *Service) refreshCompactedSummaryCoverage(ctx context.Context, messages []historyfrag.HistoryRecord, resolve func(historyfrag.HistoryRecord) (compaction.Artifact, bool)) []historyfrag.HistoryRecord {
	if s.queries == nil {
		return messages
	}
	for i := range messages {
		record := &messages[i]
		if record.SourceKind != historyfrag.SourceCompactionLog || record.Kind != contextfrag.KindConversationSummary {
			continue
		}
		if artifact, ok := resolve(*record); ok && len(artifact.Coverage) > 0 {
			continue
		}
		compactID, err := db.ParseUUID(record.CompactID)
		if err != nil {
			continue
		}
		coverage := contextfrag.NewSummaryCoverage(record.Ref, s.coveredRefsForCompact(ctx, compactID))
		record.Coverage = &coverage
	}
	return messages
}

func (s *Service) coveredRefsForCompact(ctx context.Context, compactID pgtype.UUID) []contextfrag.ContextRef {
	if s.queries == nil || !compactID.Valid {
		return nil
	}
	rows, err := s.queries.ListMessageRefsByCompactID(ctx, compactID)
	if err != nil {
		if s.logger != nil {
			s.logger.WarnContext(ctx, "coveredRefsForCompact: failed to load compacted message refs", slog.String("compact_id", pgUUIDString(compactID)), slog.Any("error", err))
		}
		return nil
	}
	refs := make([]contextfrag.ContextRef, 0, len(rows))
	for _, row := range rows {
		ref, err := historyfrag.DBMessageIdentityRef(pgUUIDString(row.ID))
		if err != nil {
			if s.logger != nil {
				s.logger.WarnContext(ctx, "coveredRefsForCompact: skipped compacted message ref", slog.String("compact_id", pgUUIDString(compactID)), slog.Any("error", err))
			}
			continue
		}
		refs = append(refs, ref)
	}
	return refs
}
