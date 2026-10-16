package native

import (
	"context"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/step"
)

func TestReadMediaOriginFollowsAdmittedPosition(t *testing.T) {
	image := sdk.Message{Role: sdk.MessageRoleUser, Content: []sdk.MessagePart{sdk.ImagePart{Image: "data:image/png;base64,AAAA"}}}
	dynamic := newLoopDynamicInputs(0)
	dynamic.beginBoundary(1)
	dynamic.append(sdk.UserMessage("before"), false, "before", 4)
	dynamic.append(image, true, "", 5)
	dynamic.append(sdk.UserMessage("after"), false, "after", 6)
	refs := dynamic.pendingRefs()
	dynamic.commit(refs)
	indexes := InternalFeedbackIndexes(dynamic.withMessageOrigins(context.Background(), 1))
	if len(indexes) != 1 || indexes[0] != 1 {
		t.Fatalf("feedback origins = %v; want only image at 1, excluding surrounding user inputs", indexes)
	}
	// A dispatch that admitted only the trailing input revokes the image.
	dynamic.commit(refs[2:])
	if got := InternalFeedbackIndexes(dynamic.withMessageOrigins(context.Background(), 1)); len(got) != 0 {
		t.Fatalf("revoked image retained origin: %v", got)
	}
}

func TestReadMediaTerminalOriginMatchesMergedMessage(t *testing.T) {
	image := sdk.Message{Role: sdk.MessageRoleUser, Content: []sdk.MessagePart{sdk.ImagePart{Image: "data:image/png;base64,AAAA"}}}
	dynamic := newLoopDynamicInputs(0)
	dynamic.beginBoundary(1)
	dynamic.append(image, true, "", 1)
	dynamic.commit(dynamic.pendingRefs())
	steps := []step.Record{{Messages: []sdk.Message{sdk.AssistantMessage("looking")}}, {Messages: []sdk.Message{sdk.AssistantMessage("done")}}}
	messages, indexes := dynamic.mergeReadMedia(steps, nil, -1)
	if len(messages) != 3 || len(indexes) != 1 || indexes[0] != 1 || messages[1].Role != sdk.MessageRoleUser {
		t.Fatalf("messages=%v origins=%v", messages, indexes)
	}
	_, indexes = dynamic.mergeReadMedia(steps, nil, 1)
	if len(indexes) != 0 {
		t.Fatalf("interrupted checkpoint feedback duplicated: %v", indexes)
	}
}
