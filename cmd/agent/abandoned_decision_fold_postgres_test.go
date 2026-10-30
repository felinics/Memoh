package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// toolMessagesWithoutCall lists the tool messages of one OpenAI chat request
// that answer a call no earlier assistant message made.
func toolMessagesWithoutCall(t *testing.T, body string) []string {
	t.Helper()
	var request struct {
		Messages []struct {
			Role       string `json:"role"`
			ToolCallID string `json:"tool_call_id"`
			ToolCalls  []struct {
				ID string `json:"id"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &request); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	called := map[string]bool{}
	var orphans []string
	for _, message := range request.Messages {
		for _, call := range message.ToolCalls {
			called[call.ID] = true
		}
		if message.Role == "tool" && !called[message.ToolCallID] {
			orphans = append(orphans, message.ToolCallID)
		}
	}
	return orphans
}

// A decision whose run was aborted leaves its call without a result row, and
// every later request closes the call with a result the read path writes. A
// run that folds the call in place takes that closure with it: no request it
// sends answers a call it no longer carries, and the next turn renders
// neither the call nor its closure.
func TestPostgresInRunFoldTakesAnAbandonedDecisionWithItsCall(t *testing.T) {
	for _, entry := range []string{"approval", "ask-user"} {
		t.Run(entry, func(t *testing.T) {
			s := newEffectLoop(t, runEntryNamed(entry))
			f := s.f
			const asked = "DECIDE_FIRST before the loop"
			s.mu.Lock()
			s.marker, s.steps, s.pendingDecision = asked, 1, true
			s.mu.Unlock()
			if code := s.chat(asked); code != "" {
				t.Fatalf("the deciding turn failed with %s", code)
			}
			if err := f.service.AbortSessionRuns(f.ctx, f.botID, f.sessionID); err != nil {
				t.Fatal(err)
			}
			var results int
			if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM bot_history_messages WHERE session_id=$1 AND role='tool'`, f.sessionID).Scan(&results); err != nil {
				t.Fatal(err)
			}
			if results != 0 {
				t.Fatalf("the aborted decision left %d result rows, want its call open", results)
			}

			web := runEntryNamed("web")
			code, requests := s.run(web, loopInput, 14)
			if code != "" || len(s.effects) != 14 {
				t.Fatalf("loop: code=%q effects=%v, want every effect run", code, s.effects)
			}
			var folded int
			if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM bot_history_messages h JOIN bot_history_message_compacts c ON c.id=h.compact_id AND c.status='ok' WHERE h.session_id=$1 AND h.role='assistant' AND h.content::text LIKE '%call_001%'`, f.sessionID).Scan(&folded); err != nil {
				t.Fatal(err)
			}
			if folded != 1 {
				t.Fatalf("the abandoned call row was folded %d times, want once", folded)
			}
			for i, request := range requests {
				if orphans := toolMessagesWithoutCall(t, request); len(orphans) > 0 {
					t.Fatalf("request %d of %d answers calls it does not carry: %v", i+1, len(requests), orphans)
				}
			}

			code, next := s.run(web, "NEXT_TURN_AFTER_THE_FOLD", 0)
			if code != "" || len(next) != 1 || strings.Contains(next[0], "call_001") {
				t.Fatalf("next turn: code=%q provider_calls=%d, want one call that renders neither the abandoned call nor its closure", code, len(next))
			}
		})
	}
}
