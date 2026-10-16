package application

import (
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/step"
	"github.com/felinics/memoh/internal/apperror"
)

func TestOutputTruncatedNoticeIsRecordedOnTheCutOffAssistantRow(t *testing.T) {
	t.Parallel()

	messages := []ModelMessage{{Role: "user"}, {Role: "assistant"}}

	cut := withOutputTruncatedNotice(storeRoundOptions{}, &step.Record{Result: sdk.ModelResult{FinishReason: sdk.FinishReasonLength}}, messages)
	notices := event.NoticesFromMetadata(cut.MessageMetadataByIndex[1])
	if len(notices) != 1 || notices[0].Code != string(apperror.CodeRuntimeOutputTruncated) || notices[0].Content == "" {
		t.Fatalf("notices on the assistant row = %+v, want one output_truncated", notices)
	}
	if _, onUser := cut.MessageMetadataByIndex[0]; onUser {
		t.Fatal("the notice belongs on the assistant row only")
	}

	for _, reason := range []sdk.FinishReason{sdk.FinishReasonStop, sdk.FinishReasonToolCalls} {
		got := withOutputTruncatedNotice(storeRoundOptions{}, &step.Record{Result: sdk.ModelResult{FinishReason: reason}}, messages)
		if len(got.MessageMetadataByIndex) != 0 {
			t.Fatalf("finish reason %q recorded metadata %+v", reason, got.MessageMetadataByIndex)
		}
	}
}
