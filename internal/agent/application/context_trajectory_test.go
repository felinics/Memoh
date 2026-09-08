package application

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	"github.com/felinics/memoh/internal/agent/context/trajectory"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/chat/timeline"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

type applicationTrajectorySink struct {
	events   []trajectory.Event
	contents map[string]string
}

func (s *applicationTrajectorySink) Append(_ context.Context, event trajectory.Event, contents []trajectory.Content) error {
	if s.contents == nil {
		s.contents = make(map[string]string)
	}
	for _, content := range contents {
		s.contents[content.Hash] = string(content.Data)
	}
	s.events = append(s.events, event)
	return nil
}

func (s *applicationTrajectorySink) stageText(stage string) string {
	var text strings.Builder
	for _, event := range s.events {
		if event.Stage != stage {
			continue
		}
		for _, block := range event.Blocks {
			for _, hash := range block.Chunks {
				text.WriteString(s.contents[hash])
			}
		}
	}
	return text.String()
}

func TestPipelineTrajectoryPreservesHistoryBeforeSummaryReplacement(t *testing.T) {
	sink := &applicationTrajectorySink{}
	recorder := trajectory.NewRecorder(sink)
	recorder.Bind("run", pipelineTestSessionID)
	pipeline := timeline.NewPipeline(timeline.RenderParams{})
	pipeline.PushEvent(pipelineTestSessionID, pipelineTextEvent("m1", 1000, "ORIGINAL_REPLACED_HISTORY"))
	pipeline.PushEvent(pipelineTestSessionID, pipelineTextEvent("m2", 2000, "CURRENT_TRIGGER"))
	svc := &Service{
		pipeline: pipeline,
		queries:  fakeArtifactLineageQueries{rows: []sqlc.BotHistoryMessageCompact{compactionLogRow(t, "COMPACTED_SUMMARY", "m1", 1000)}},
		logger:   slog.New(slog.DiscardHandler),
	}
	messages, _ := svc.buildMessagesFromPipeline(trajectory.WithRecorder(t.Context(), recorder), ChatRequest{BotID: pipelineTestBotID, ThreadID: pipelineTestSessionID}, 0)
	if strings.Contains(messagesDebug(messages), "ORIGINAL_REPLACED_HISTORY") {
		t.Fatal("fixture did not replace history")
	}
	if !strings.Contains(sink.stageText("history_loaded"), "ORIGINAL_REPLACED_HISTORY") {
		t.Fatal("trajectory lost history that compaction replaced")
	}
	after := sink.stageText("history_composed")
	if !strings.Contains(after, "COMPACTED_SUMMARY") || !strings.Contains(after, "CURRENT_TRIGGER") || strings.Contains(after, "ORIGINAL_REPLACED_HISTORY") {
		t.Fatal("trajectory does not show the actual composed history")
	}
}

func TestPromptTrajectoryFollowsMaterializationThroughProvider(t *testing.T) {
	provider := &triggerCaptureProvider{}
	cfg := triggerResolvedRunConfig(provider, "ORIGINAL_TRIGGER", time.Unix(100, 0), "chat")
	cfg.RunID, cfg.Identity.SessionID = "run", "session"
	sink := &applicationTrajectorySink{}
	cfg.ContextLifecycle.SetTrajectoryRecorder(trajectory.NewRecorder(sink))
	_, params := runTriggerPromptPipeline(t, cfg, provider)
	if len(params.Messages) != 1 {
		t.Fatal("unexpected provider fixture")
	}
	for _, stage := range []string{"prompt_input", "prompt_built", "context_collected", "provider_request"} {
		if !strings.Contains(sink.stageText(stage), "ORIGINAL_TRIGGER") {
			t.Fatalf("%s lost original trigger", stage)
		}
	}
}

func TestContinuationTrajectoryPreservesDiscardedPriorInput(t *testing.T) {
	sink := &applicationTrajectorySink{}
	holder := contextfrag.NewLifecycleHolder()
	holder.SetTrajectoryRecorder(trajectory.NewRecorder(sink))
	base := native.RunConfig{RunID: "run", Query: "STALE_CONTINUATION_QUERY", ContextLifecycle: holder,
		Identity: native.SessionContext{SessionID: "session"}}
	_, err := (&Service{}).prepareContinuationRunConfig(t.Context(), base, historyfrag.ScopeFallback{}, contextfrag.Scope{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sink.stageText("continuation_input"), "STALE_CONTINUATION_QUERY") {
		t.Fatal("continuation lost input before history reconstruction")
	}
	if strings.Contains(sink.stageText("prompt_built"), "STALE_CONTINUATION_QUERY") {
		t.Fatal("continuation records stale query as part of rebuilt prompt")
	}
}
