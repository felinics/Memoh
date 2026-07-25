package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/agent/chat/compaction"
	"github.com/memohai/memoh/domains/agent/chat/context/fragment"
	"github.com/memohai/memoh/domains/agent/chat/context/history"
)

func historyContextFragsForMessages(messages []agentdomain.ModelMessage, records []history.HistoryRecord) []fragment.ContextFrag {
	if len(messages) == 0 || len(records) == 0 {
		return nil
	}
	frags := make([]fragment.ContextFrag, 0)
	recordStart := 0
	for i, msg := range messages {
		if !looksLikeSummaryMessage(msg) {
			continue
		}
		for j := recordStart; j < len(records); j++ {
			record := records[j]
			if record.Kind != fragment.KindConversationSummary || record.Coverage == nil {
				continue
			}
			if !sameModelMessage(record.ModelMessage, msg) {
				continue
			}
			frag := history.ToFrag(record)
			frag.ID = fmt.Sprintf("message.%03d", i)
			frag.Provenance.Index = i
			frags = append(frags, frag)
			recordStart = j + 1
			break
		}
	}
	return frags
}

func looksLikeSummaryMessage(msg agentdomain.ModelMessage) bool {
	return strings.EqualFold(strings.TrimSpace(msg.Role), "user") &&
		strings.HasPrefix(strings.TrimSpace(msg.TextContent()), "<summary>")
}

func sameModelMessage(a agentdomain.ModelMessage, b agentdomain.ModelMessage) bool {
	return strings.EqualFold(strings.TrimSpace(a.Role), strings.TrimSpace(b.Role)) &&
		string(a.Content) == string(b.Content)
}

func historySourceMessageIDsForMessages(messages []agentdomain.ModelMessage, records []history.HistoryRecord) []string {
	sourceIDs := make([]string, len(messages))
	if len(messages) == 0 || len(records) == 0 {
		return sourceIDs
	}
	positions := make(map[string][]int, len(records))
	for i, record := range records {
		if strings.TrimSpace(record.DBMessageID) == "" {
			continue
		}
		key := modelMessageSourceKey(record.ModelMessage)
		positions[key] = append(positions[key], i)
	}
	cursors := make(map[string]int, len(positions))
	lastMatched := -1
	for i, message := range messages {
		key := modelMessageSourceKey(message)
		candidates := positions[key]
		cursor := cursors[key]
		for cursor < len(candidates) && candidates[cursor] <= lastMatched {
			cursor++
		}
		cursors[key] = cursor
		if cursor >= len(candidates) {
			continue
		}
		recordIndex := candidates[cursor]
		cursors[key] = cursor + 1
		lastMatched = recordIndex
		sourceIDs[i] = strings.TrimSpace(records[recordIndex].DBMessageID)
	}
	return sourceIDs
}

func modelMessageSourceKey(message agentdomain.ModelMessage) string {
	return strings.ToLower(strings.TrimSpace(message.Role)) + "\x00" + string(message.Content)
}

func stripToolMessagesWhenCompactionSummaryIsActive(messages []agentdomain.ModelMessage, records []history.HistoryRecord) []agentdomain.ModelMessage {
	for _, record := range records {
		if record.SourceKind == history.SourceCompactionLog &&
			record.Kind == fragment.KindConversationSummary &&
			record.Lifecycle == history.LifecycleActiveSummary {
			return stripToolMessages(messages)
		}
	}
	return messages
}

type compactionArtifactBoundary struct {
	hasCutoff bool
	cutoffMs  int64
}

func compactionArtifactBoundaryBeforeMessage(messages []history.HistoryRecord, messageID string) compactionArtifactBoundary {
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

func (s *Service) loadCompactionArtifactBoundary(ctx context.Context, messages []history.HistoryRecord, sessionID, messageID string) compactionArtifactBoundary {
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

func (s *Service) replaceCompactedMessages(ctx context.Context, sessionID string, scope fragment.Scope, messages []history.HistoryRecord, boundary compactionArtifactBoundary) ([]history.HistoryRecord, error) {
	if s.compactionArtifacts == nil {
		return messages, nil
	}
	if strings.TrimSpace(sessionID) == "" {
		// Sessionless (chat-scoped) loads have no session log list to draw from;
		// resolve each in-window compact group individually.
		return s.replaceRecentCompactedMessages(ctx, scope, messages, boundary)
	}
	frontier, err := s.loadActiveCompactionFrontier(ctx, scope.BotID, sessionID)
	if err != nil {
		return nil, err
	}
	if len(frontier.Artifacts) == 0 {
		return messages, nil
	}
	owner := compaction.ArtifactOwner{BotID: scope.BotID, SessionID: sessionID, SessionIDKnown: true}
	catalog := compaction.NewArtifactCatalog()
	catalog.Add(owner, frontier)
	blocked := conflictingArtifactIDs(catalog, messages, scope)
	resolve := func(record history.HistoryRecord) (compaction.Artifact, bool) {
		artifact, ok := resolveCatalogArtifact(catalog, recordArtifactOwner(record, scope), record)
		if !ok || !boundary.includes(artifact) {
			return compaction.Artifact{}, false
		}
		if _, conflict := blocked[artifact.ID]; conflict {
			return compaction.Artifact{}, false
		}
		return artifact, true
	}
	messages = replaceCompactedHistoryRecordsWithService(messages, scope, resolve)
	sessionSummaries := summaryRecordsFromArtifacts(missingCompactionArtifacts(messages, frontier.Artifacts, blocked, boundary), scope)
	if len(sessionSummaries) > 0 {
		messages = mergeMissingCompactionSummaries(messages, sessionSummaries)
	}
	return s.refreshCompactedSummaryCoverage(ctx, messages, resolve), nil
}

// missingCompactionArtifacts filters the active frontier down to summaries not
// already represented in the loaded records.
func missingCompactionArtifacts(messages []history.HistoryRecord, artifacts []compaction.Artifact, blocked map[string]struct{}, boundary compactionArtifactBoundary) []compaction.Artifact {
	seen := make(map[string]struct{}, len(messages))
	for _, record := range messages {
		if record.SourceKind == history.SourceCompactionLog {
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

func (s *Service) replaceRecentCompactedMessages(ctx context.Context, scope fragment.Scope, messages []history.HistoryRecord, boundary compactionArtifactBoundary) ([]history.HistoryRecord, error) {
	if s.compactionArtifacts == nil {
		return messages, nil
	}

	compactGroups := make(map[string][]int) // compact_id -> indices
	for i, m := range messages {
		if m.CompactID != "" {
			compactGroups[m.CompactID] = append(compactGroups[m.CompactID], i)
		}
	}
	catalog := compaction.NewArtifactCatalog()
	loadedFrontiers := make(map[compaction.ArtifactOwner]struct{})
	projection := compaction.NewArtifactProjection(s.compactionArtifacts)
	for _, record := range messages {
		owner := recordArtifactOwner(record, scope)
		if owner.SessionID == "" {
			continue
		}
		if _, loaded := loadedFrontiers[owner]; loaded {
			continue
		}
		frontier, err := s.loadActiveCompactionFrontier(ctx, owner.BotID, owner.SessionID)
		if err != nil {
			return nil, err
		}
		catalog.Add(owner, frontier)
		loadedFrontiers[owner] = struct{}{}
	}
	for compactID := range compactGroups {
		owner, consistent := compactGroupOwner(messages, compactGroups[compactID], scope)
		if !consistent {
			if s.logger != nil {
				s.logger.WarnContext(ctx, "replaceCompactedMessages: compact group spans owners", slog.String("compact_id", compactID))
			}
			continue
		}
		if _, loaded := loadedFrontiers[owner]; loaded {
			continue
		}
		artifact, err := projection.LoadActiveByID(ctx, compactID, owner)
		if err != nil {
			var lineageErr *compaction.LineageError
			if !errors.As(err, &lineageErr) {
				return nil, err
			}
			if s.logger != nil {
				s.logger.WarnContext(ctx, "replaceCompactedMessages: failed to load compaction artifact", slog.String("compact_id", compactID), slog.Any("error", err))
			}
			continue
		}
		merged := catalog.Add(owner, compaction.NewArtifactAliasFrontier(compactID, artifact))
		for _, issue := range merged.Issues {
			if s.logger != nil && (issue.Kind == compaction.LineageIssueCoverageOverlap || issue.Kind == compaction.LineageIssueAliasConflict) {
				s.logger.WarnContext(ctx, "replaceCompactedMessages: owner frontier conflict", slog.String("issue", issue.Error()))
			}
		}
	}
	blocked := conflictingArtifactIDs(catalog, messages, scope)
	resolve := func(record history.HistoryRecord) (compaction.Artifact, bool) {
		artifact, ok := resolveCatalogArtifact(catalog, recordArtifactOwner(record, scope), record)
		if !ok || !boundary.includes(artifact) {
			return compaction.Artifact{}, false
		}
		if _, conflict := blocked[artifact.ID]; conflict {
			return compaction.Artifact{}, false
		}
		return artifact, true
	}

	replaced := replaceCompactedHistoryRecordsWithService(messages, scope, resolve)
	return s.refreshCompactedSummaryCoverage(ctx, replaced, resolve), nil
}

func resolveCatalogArtifact(catalog *compaction.ArtifactCatalog, owner compaction.ArtifactOwner, record history.HistoryRecord) (compaction.Artifact, bool) {
	if compactID := strings.TrimSpace(record.CompactID); compactID != "" {
		if artifact, ok := catalog.Resolve(owner, compactID); ok {
			isSummary := record.SourceKind == history.SourceCompactionLog && record.Kind == fragment.KindConversationSummary
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

func conflictingArtifactIDs(catalog *compaction.ArtifactCatalog, messages []history.HistoryRecord, fallback fragment.Scope) map[string]struct{} {
	blocked := make(map[string]struct{})
	for _, record := range messages {
		if record.SourceKind == history.SourceCompactionLog {
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
	frontier, err := compaction.NewArtifactProjection(s.compactionArtifacts).LoadActiveSession(ctx, compaction.ArtifactOwner{BotID: botID, SessionID: sessionID, SessionIDKnown: true})
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

func recordArtifactOwner(record history.HistoryRecord, fallback fragment.Scope) compaction.ArtifactOwner {
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

func compactGroupOwner(messages []history.HistoryRecord, indices []int, fallback fragment.Scope) (compaction.ArtifactOwner, bool) {
	owner := compaction.ArtifactOwner{
		BotID:          strings.TrimSpace(fallback.BotID),
		SessionID:      strings.TrimSpace(fallback.SessionID),
		SessionIDKnown: strings.TrimSpace(fallback.SessionID) != "",
	}
	for _, index := range indices {
		recordOwner := recordArtifactOwner(messages[index], fragment.Scope{})
		if recordOwner.BotID != "" {
			if owner.BotID != "" && owner.BotID != recordOwner.BotID {
				return compaction.ArtifactOwner{}, false
			}
			owner.BotID = recordOwner.BotID
		}
		if recordOwner.SessionIDKnown {
			if owner.SessionIDKnown && owner.SessionID != recordOwner.SessionID {
				return compaction.ArtifactOwner{}, false
			}
			owner.SessionID = recordOwner.SessionID
			owner.SessionIDKnown = true
		}
	}
	return owner, true
}

func summaryRecordsFromArtifacts(artifacts []compaction.Artifact, scope fragment.Scope) []history.HistoryRecord {
	if len(artifacts) == 0 {
		return nil
	}
	records := make([]history.HistoryRecord, 0, len(artifacts))
	for _, artifact := range artifacts {
		records = append(records, artifact.HistoryRecord(scope))
	}
	return records
}

// refreshCompactedSummaryCoverage backfills legacy summaries that predate
// persisted artifact coverage. New artifacts already carry immutable refs and
// avoid this per-summary query.
func (s *Service) refreshCompactedSummaryCoverage(ctx context.Context, messages []history.HistoryRecord, resolve func(history.HistoryRecord) (compaction.Artifact, bool)) []history.HistoryRecord {
	if s.compactionMessageRefs == nil {
		return messages
	}
	for i := range messages {
		record := &messages[i]
		if record.SourceKind != history.SourceCompactionLog || record.Kind != fragment.KindConversationSummary {
			continue
		}
		if artifact, ok := resolve(*record); ok && len(artifact.Coverage) > 0 {
			continue
		}
		compactID, err := uuid.Parse(strings.TrimSpace(record.CompactID))
		if err != nil {
			continue
		}
		coverage := fragment.NewSummaryCoverage(record.Ref, s.coveredRefsForCompact(ctx, compactID.String()))
		record.Coverage = &coverage
	}
	return messages
}

func (s *Service) coveredRefsForCompact(ctx context.Context, compactID string) []fragment.ContextRef {
	if s.compactionMessageRefs == nil {
		return nil
	}
	parsed, err := uuid.Parse(strings.TrimSpace(compactID))
	if err != nil {
		return nil
	}
	compactID = parsed.String()
	messageIDs, err := s.compactionMessageRefs.ListCompactionMessageRefs(ctx, compactID)
	if err != nil {
		if s.logger != nil {
			s.logger.WarnContext(ctx, "coveredRefsForCompact: failed to load compacted message refs", slog.String("compact_id", compactID), slog.Any("error", err))
		}
		return nil
	}
	refs := make([]fragment.ContextRef, 0, len(messageIDs))
	for _, messageID := range messageIDs {
		ref, err := history.DBMessageIdentityRef(messageID)
		if err != nil {
			if s.logger != nil {
				s.logger.WarnContext(ctx, "coveredRefsForCompact: skipped compacted message ref", slog.String("compact_id", compactID), slog.Any("error", err))
			}
			continue
		}
		refs = append(refs, ref)
	}
	return refs
}
