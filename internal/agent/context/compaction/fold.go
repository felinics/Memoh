package compaction

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/jackc/pgx/v5/pgtype"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/chat/checkpoint"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/models/modelretry"
)

// Source is what one message of a folded request stands for.
type Source struct {
	// Sources are the history rows, inbound messages or summary the message
	// renders. A message with none is runtime context the caller rebuilds
	// for every request: it stays where it is and no summary stands for it.
	Sources []turn.ContextMessageSource
	// Anchor marks the run's input or a steer, which a fold keeps verbatim
	// where it is before anything else it keeps (retain).
	Anchor bool
}

// Summarizer answers a fork request: the run's own model, or the compaction
// model for a request built from rows.
type Summarizer func(context.Context, sdk.Request) (sdk.ModelResult, error)

// Event is one compaction event over a request its caller holds: the run's
// next request, or one built from rows the run cannot show.
type Event struct {
	BotID     string
	SessionID string
	// ModelRecordID names the summarizer's model row in the compaction log.
	ModelRecordID string
	// Request is what the caller sends next and Sources what each of its
	// messages renders. The event decides what of it stays verbatim (retain).
	Request sdk.Request
	Sources []Source
	// Current is the input the caller's next request answers when the
	// request does not hold it: the summarizer reads it to judge what the
	// work needs.
	Current []sdk.Message
	// AnchorPolicy is how a fold keeps the request's anchors.
	AnchorPolicy AnchorPolicy
	// Window is the summarizer's context window and MaxOutputTokens its
	// output cap; Allowance is what the caller's next request may take.
	Window          int
	MaxOutputTokens int
	Allowance       int
	// FixedOutput marks a summarizer that answers within all of
	// MaxOutputTokens: the run's own model, whose output settings a fork of
	// the run's request keeps, since the provider settings the fork shares
	// with the run's requests (a thinking budget) hold for the output those
	// ask for. A pass then reads only what the window holds beside all of it,
	// as the run's own requests do. Otherwise a fork asks for less output
	// when that lets a pass read more.
	FixedOutput bool
	// Pressure is the request size that called for the event, for hooks.
	Pressure int
	// ReplacedTurnID is the turn an edit or retry replaces: the event first
	// moves the session to the checkpoint its replacement keeps.
	ReplacedTurnID string
	Summarizer     Summarizer
	// Apply splices every committed pass into the caller's request as it
	// commits, and returns the request and sources a further pass reads.
	Apply func(Pass) (sdk.Request, []Source)

	// retention is what the event keeps verbatim, decided before its first
	// pass.
	retention
}

// Fold runs one compaction event over a request its caller holds: it decides
// what of the request stays verbatim, forks the request before the newest
// messages it keeps and asks for the summary that replaces everything else, the summaries of the checkpoint the request
// renders included. A pass reads as much of the request as the summarizer's
// window holds; the event passes again only for what that left. It returns
// the passes it committed; a failed pass ends the event with an *EventError
// and leaves the passes before it.
func (s *Service) Fold(ctx context.Context, ev Event) ([]Pass, error) {
	scope := hookScope{BotID: ev.BotID, SessionID: ev.SessionID, ModelID: ev.Request.Model, InputTokens: ev.Pressure, BudgetTokens: ev.Allowance}
	return s.event(ctx, scope, ev.ReplacedTurnID, func(ctx context.Context) ([]Pass, error) {
		return s.passes(ctx, ev)
	})
}

// passes decides what ev keeps verbatim and runs its passes, splicing each
// into the caller's request through ev.Apply, until one reads up to the
// newest messages it keeps or nothing is left to fold.
func (s *Service) passes(ctx context.Context, ev Event) ([]Pass, error) {
	if len(ev.Sources) != len(ev.Request.Messages) {
		return nil, fmt.Errorf("compaction: request of %d messages has %d sources", len(ev.Request.Messages), len(ev.Sources))
	}
	rows, err := s.requestRows(ctx, ev)
	if err != nil {
		return nil, err
	}
	ev.retention = retain(ev, rows.unfoldable(ev))
	s.logger.InfoContext(ctx, "compaction retention",
		slog.String("bot_id", ev.BotID), slog.String("session_id", ev.SessionID),
		slog.Int("verbatim_budget_tokens", verbatimBudget(ev)), slog.Int("kept_groups", ev.keptGroups),
		slog.Int("latest_group_tokens", ev.latestGroupTokens))
	var passes []Pass
	for {
		pass, folded, err := s.foldPass(ctx, ev, rows)
		if err != nil {
			return passes, err
		}
		if !folded {
			return passes, nil
		}
		passes = append(passes, pass)
		ev.Request, ev.Sources = ev.Apply(pass)
		ev.cut = pass.cut
		if !pass.More {
			return passes, nil
		}
	}
}

// foldPass forks the request once and commits the summary it gets as the
// session's next checkpoint: one shorter than what it replaces that, when the
// next request carries it, fits the room that request has. A summary longer
// than the instruction asks for is committed too; it brings the next fold
// sooner rather than fail the run. It reports false when nothing before the
// messages the event keeps can be folded.
func (s *Service) foldPass(ctx context.Context, ev Event, rows requestRows) (Pass, bool, error) {
	parent, err := s.checkpoints.Active(ctx, ev.SessionID)
	if err != nil {
		return Pass{}, false, err
	}
	if err := rendersCheckpoint(ev, parent); err != nil {
		return Pass{}, false, err
	}
	plan, err := planFold(ev, rows.unfoldable(ev))
	if err != nil || !plan.folds() {
		return Pass{}, false, err
	}
	fork := ev.Request
	fork.Messages = append(slices.Clip(plan.fork), sdk.UserMessage(plan.instruction))
	if !ev.FixedOutput {
		fork.MaxTokens = &plan.maxTokens
	}

	persistCtx := context.WithoutCancel(ctx)
	logID, err := s.openLog(persistCtx, ev.BotID, ev.SessionID)
	if err != nil {
		return Pass{}, false, err
	}
	fail := func(err error) (Pass, bool, error) {
		_ = s.completeLog(persistCtx, s.queries, logID, "error", "", err.Error(), 0, nil, pgtype.UUID{})
		return Pass{}, false, err
	}
	result, err := s.summarize(ctx, ev, fork, plan)
	if err != nil {
		return fail(err)
	}
	summary := strings.TrimSpace(result.Text)
	replay := summaryReplayTokens(summary)
	switch {
	case replay >= plan.replaced:
		return fail(failed(ReasonIneffective, fmt.Errorf("%w: summary_tokens=%d replaced_tokens=%d", errIneffectiveSummary, replay, plan.replaced)))
	case plan.bounded() && replay > plan.room:
		return fail(failed(ReasonStillOver, fmt.Errorf("%w: replay=%d room=%d", errNoSummaryRoom, replay, plan.room)))
	}

	folds := rows.of(plan)
	commit := checkpoint.Fold{
		SessionID: ev.SessionID,
		Parent:    parent,
		SummaryID: formatUUID(logID),
		Summary:   summary,
		Folded:    folds.folded,
		Read:      folds.read,
	}
	usage, _ := json.Marshal(result.Usage)
	if err := s.inSession(ctx, ev.BotID, ev.SessionID, func(q dbstore.Queries) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := s.checkpoints.Commit(ctx, q, commit); err != nil {
			return err
		}
		return s.completeLog(ctx, q, logID, "ok", summary, "", len(folds.folded), usage, db.ParseUUIDOrEmpty(ev.ModelRecordID))
	}); err != nil {
		return fail(staleSource(err))
	}
	order, cut := plan.order(ev)
	return Pass{
		SummaryID: commit.SummaryID,
		Summary:   summary,
		Order:     order,
		cut:       cut,
		More:      !plan.final(),
	}, true, nil
}

// rendersCheckpoint refuses a request composed on a checkpoint the session
// no longer has: the summaries it renders must be the ones the active
// checkpoint holds, or no fold onto that checkpoint matches it.
func rendersCheckpoint(ev Event, parent checkpoint.Checkpoint) error {
	rendered := map[string]bool{}
	for _, source := range ev.Sources {
		for _, s := range source.Sources {
			if s.Kind == "summary" {
				rendered[s.ID] = true
			}
		}
	}
	held := parent.SummaryIDs()
	matches := len(rendered) == len(held)
	for _, id := range held {
		matches = matches && rendered[id]
	}
	if !matches {
		return failed(ReasonStaleSource, fmt.Errorf("%w: the request renders summaries the active checkpoint %q does not hold", errStaleSource, parent.ID))
	}
	return nil
}

// summarize asks for the summary. A transient failure gets one immediate
// retry, or the wait the provider asks for; a rejected request is not made
// again. A model that answers with a tool call is asked once more, with the
// same prefix and a firmer instruction, which the pass priced in, so the
// second request fits the window with the same output; the answer must
// finish naturally and say something.
func (s *Service) summarize(ctx context.Context, ev Event, fork sdk.Request, plan foldPlan) (sdk.ModelResult, error) {
	ask := func(fork sdk.Request) (sdk.ModelResult, error) {
		return modelretry.Do(models.WithModelSession(ctx, ev.SessionID), s.logger, "agent.compaction",
			modelretry.Config{MaxAttempts: 1, FastAttempts: 1}, modelretry.Retryable,
			func(ctx context.Context) (sdk.ModelResult, error) { return ev.Summarizer(ctx, fork) })
	}
	result, err := ask(fork)
	if err == nil && len(result.ToolCalls) > 0 {
		s.logger.WarnContext(ctx, "compaction: the summarizer called a tool, asking again",
			slog.String("session_id", ev.SessionID), slog.Int("tool_calls", len(result.ToolCalls)))
		fork.Messages = append(slices.Clip(fork.Messages[:len(fork.Messages)-1]), sdk.UserMessage(plan.instruction+noToolsReminder))
		result, err = ask(fork)
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return sdk.ModelResult{}, ctxErr
		}
		return sdk.ModelResult{}, failed(ReasonSummarizerFailed, err)
	}
	switch {
	case len(result.ToolCalls) > 0:
		return sdk.ModelResult{}, failed(ReasonSummarizerFailed, fmt.Errorf("compaction: the summarizer called %d tools", len(result.ToolCalls)))
	case strings.TrimSpace(result.Text) == "":
		return sdk.ModelResult{}, failed(ReasonEmpty, errEmptySummary)
	case result.FinishReason != sdk.FinishReasonStop:
		return sdk.ModelResult{}, failed(ReasonTruncated, fmt.Errorf("%w: finish_reason=%s max_tokens=%d", errIncompleteSummary, result.FinishReason, plan.maxTokens))
	}
	return result, nil
}

// requestRows is where the sources a request renders stand in the history: the context rows that hold each one, by the source as the
// request names it. A history source names its row; an inbound or a
// self-sent message names the rows it was written to.
type requestRows map[turn.ContextMessageSource][]checkpoint.Row

func (s *Service) requestRows(ctx context.Context, ev Event) (requestRows, error) {
	sessionUUID, err := db.ParseUUID(ev.SessionID)
	if err != nil {
		return nil, err
	}
	var ids []pgtype.UUID
	var external []string
	for _, source := range ev.Sources {
		for _, s := range source.Sources {
			switch s.Kind {
			case "history":
				if id, err := db.ParseUUID(s.ID); err == nil {
					ids = append(ids, id)
				}
			case "external", "self":
				if s.ID != "" {
					external = append(external, s.ID)
				}
			}
		}
	}
	if len(ids) == 0 && len(external) == 0 {
		return nil, nil
	}
	found, err := s.queries.ListHistoryRowsBySources(ctx, sqlc.ListHistoryRowsBySourcesParams{SessionID: sessionUUID, Ids: ids, ExternalMessageIds: external})
	if err != nil {
		return nil, fmt.Errorf("load the rows the request renders: %w", err)
	}
	rows := requestRows{}
	for _, row := range found {
		id := formatUUID(row.ID)
		held := checkpoint.Row{ID: id, Position: messagepkg.HistoryPosition{Turn: row.TurnPosition.Int64, Seq: row.TurnMessageSeq.Int64}}
		key := turn.ContextMessageSource{Kind: "history", ID: id}
		rows[key] = append(rows[key], held)
		if message := strings.TrimSpace(row.SourceMessageID.String); message != "" {
			for _, kind := range []string{"external", "self"} {
				key := turn.ContextMessageSource{Kind: kind, ID: message}
				rows[key] = append(rows[key], held)
			}
		}
	}
	return rows, nil
}

// unfoldable marks the messages of ev a fold cannot stand for: one that
// renders a source no context row holds stays where it is, as the checkpoint
// has nothing to fold for it.
func (r requestRows) unfoldable(ev Event) map[int]bool {
	out := map[int]bool{}
	for i, source := range ev.Sources {
		if isSummarySource(source) {
			continue
		}
		for _, s := range source.Sources {
			if len(r[turn.ContextMessageSource{Kind: s.Kind, ID: s.ID}]) == 0 {
				out[i] = true
				break
			}
		}
	}
	return out
}

// foldRows is what a pass read and folds as its checkpoint names them: the
// rows it folds and the rows it read.
type foldRows struct {
	folded []checkpoint.Row
	read   []string
}

func (r requestRows) of(plan foldPlan) foldRows {
	var out foldRows
	read, folded := map[string]bool{}, map[string]bool{}
	for _, source := range plan.read {
		for _, row := range r[turn.ContextMessageSource{Kind: source.Kind, ID: source.ID}] {
			if !read[row.ID] {
				read[row.ID] = true
				out.read = append(out.read, row.ID)
			}
		}
	}
	for _, source := range plan.folded {
		for _, row := range r[turn.ContextMessageSource{Kind: source.Kind, ID: source.ID}] {
			if !folded[row.ID] {
				folded[row.ID] = true
				out.folded = append(out.folded, row)
			}
		}
	}
	return out
}

// noToolsReminder is what a second fork request adds to the instruction
// after a tool call.
const noToolsReminder = "\n\nYour previous answer called a tool. Do not call any tool: answer with the summary text alone."

// hookScope is what the compaction hooks are told about an event.
type hookScope struct {
	BotID        string
	SessionID    string
	ModelID      string
	InputTokens  int
	BudgetTokens int
}

// forkTokens prices a fork request as its provider receives it.
func forkTokens(req sdk.Request) int {
	return contextfrag.ProviderEnvelopeTokens(req.System, req.Messages, req.Tools)
}
