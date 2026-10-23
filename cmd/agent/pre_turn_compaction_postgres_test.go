package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

// History that does not fit the model window is summarized before the turn,
// from the oldest row no summary replaces however old it is, into one active
// summary that the next compaction folds again. A summary as long as the prompt allows is
// answered without another compaction. When the summarizer fails, the turn
// fails before any provider call and leaves history whole; once the
// summarizer is back, the next turn runs on a summary that also covers the
// failed input a bounded chat view no longer loads.
func TestPostgresPreTurnCompaction(t *testing.T) {
	for _, entry := range []string{"chat", "discuss"} {
		t.Run(entry, func(t *testing.T) {
			f := newBacklogFixture(t)
			send := f.sendDiscuss
			if entry == "chat" {
				send = f.sendChat
				if _, err := f.pool.Exec(f.ctx, `UPDATE bot_sessions SET type='chat', channel_type='local' WHERE id=$1`, f.sessionID); err != nil {
					t.Fatal(err)
				}
			}
			f.persistHistory("old-", 4, 400, time.Now().Add(-48*time.Hour))
			for round := range 2 {
				prefix := fmt.Sprintf("round%d-", round)
				// The second summary is as long as the prompt allows: it must
				// pass the recheck without another compaction.
				f.longSummary.Store(round == 1)
				f.appendRound(entry, prefix)
				summaries, calls := f.summaries.Load(), f.modelCalls.Load()
				current := prefix + "current"
				if code := send(current, current+" message"); code != "" || f.modelCalls.Load() != calls+1 || f.summaries.Load() == summaries {
					t.Fatalf("%s: code=%q provider_calls=%d summaries=%d, want one answered call after a summary", prefix, code, f.modelCalls.Load()-calls, f.summaries.Load()-summaries)
				}
				request, _ := f.lastRequest.Load().(string)
				if strings.Count(request, "Earlier backlog summarized.") != 1 || strings.Count(request, current+" message") != 1 || f.activeSummaries() != 1 {
					t.Fatalf("%s: summaries in request=%d current=%d active=%d, want one of each", prefix, strings.Count(request, "Earlier backlog summarized."), strings.Count(request, current+" message"), f.activeSummaries())
				}
				if long := strings.Count(request, " kept"); round == 1 && long < 100 {
					t.Fatalf("%s: the long summary reached the provider with %d filler words", prefix, long)
				}
			}
			f.longSummary.Store(false)
			if raw, compacted := f.backlogRowsPrefixed("old-"); raw != 0 || compacted != 4 {
				t.Fatalf("history older than a day: raw=%d compacted=%d, want it summarized", raw, compacted)
			}
			f.failSummary.Store(true)
			f.appendRound(entry, "failed-")
			summaries, calls := f.summaries.Load(), f.modelCalls.Load()
			if code := send("failed-current", "failed-current message"); code != "context.budget_unsatisfied" || f.modelCalls.Load() != calls || f.summaries.Load() == summaries {
				t.Fatalf("summarizer down: code=%q provider_calls=%d summary_attempts=%d, want budget_unsatisfied before any provider call", code, f.modelCalls.Load()-calls, f.summaries.Load()-summaries)
			}
			if raw, compacted := f.backlogRowsPrefixed("failed-"); raw < 11 || compacted != 0 || f.activeSummaries() != 1 {
				t.Fatalf("a failed summary changed history: raw=%d compacted=%d active=%d", raw, compacted, f.activeSummaries())
			}
			f.appendRound(entry, "failedb-")
			if code := send("failedb-current", "failedb-current message"); code != "context.budget_unsatisfied" {
				t.Fatalf("summarizer still down: code=%q", code)
			}
			f.failSummary.Store(false)
			calls = f.modelCalls.Load()
			if code := send("next-current", "next-current message"); code != "" || f.modelCalls.Load() != calls+1 {
				t.Fatalf("summarizer back: code=%q provider_calls=%d", code, f.modelCalls.Load()-calls)
			}
			request, _ := f.lastRequest.Load().(string)
			if raw, compacted := f.backlogRowsPrefixed("failed-"); compacted == 0 || entry == "chat" && raw != 0 || strings.Count(request, "next-current message") != 1 || f.activeSummaries() != 1 {
				t.Fatalf("summarizer back: raw=%d compacted=%d current=%d active=%d", raw, compacted, strings.Count(request, "next-current message"), f.activeSummaries())
			}
		})
	}
}

// persistHistory writes answered history the turn did not take part in:
// user/assistant pairs for chat, created around at.
func (f *backlogFixture) persistHistory(prefix string, count, size int, at time.Time) {
	f.t.Helper()
	for i := range count {
		id := fmt.Sprintf("%s%d", prefix, i)
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		content, _ := json.Marshal(id + " " + strings.Repeat("x", size))
		msg, err := f.messages.Persist(f.ctx, messagepkg.PersistInput{BotID: f.botID, SessionID: f.sessionID, ExternalMessageID: id, Role: role, Content: content})
		if err != nil {
			f.t.Fatal(err)
		}
		if _, err := f.pool.Exec(f.ctx, `UPDATE bot_history_messages SET created_at=$2 WHERE id=$1`, msg.ID, at.Add(time.Duration(i)*time.Second)); err != nil {
			f.t.Fatal(err)
		}
	}
}

// appendRound adds history over the model budget that the next turn loads:
// answered pairs for chat, unconsumed input for discuss.
func (f *backlogFixture) appendRound(entry, prefix string) {
	if entry == "chat" {
		f.persistHistory(prefix, 12, 2800, time.Now().Add(-time.Hour))
		return
	}
	for i := range 11 {
		f.appendMessage(fmt.Sprintf("%s%d", prefix, i), fmt.Sprintf("%s%d ", prefix, i)+strings.Repeat("x", 2800))
	}
}

func (f *backlogFixture) activeSummaries() int {
	f.t.Helper()
	var count int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM bot_history_message_compacts WHERE session_id=$1 AND status='ok' AND superseded_by IS NULL`, f.sessionID).Scan(&count); err != nil {
		f.t.Fatal(err)
	}
	return count
}

func (f *backlogFixture) sendDiscuss(id, text string) string {
	f.appendMessage(id, text)
	return f.trigger()
}

func (f *backlogFixture) sendChat(_, text string) string {
	f.t.Helper()
	handle, err := f.service.StartTurn(f.ctx, turn.StartTurnCommand{TeamID: f.cfg.TeamID, Mode: turn.ModeChat, BotID: f.botID, ChatID: f.botID, ThreadID: f.sessionID, Query: text, CurrentChannel: "local"})
	if err != nil {
		f.t.Fatal(err)
	}
	for range handle.Events() {
	}
	if err := <-handle.Errs(); err != nil {
		return string(apperror.CodeOf(err))
	}
	return ""
}
