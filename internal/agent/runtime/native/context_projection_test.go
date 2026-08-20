package native

import (
	"testing"

	sdk "github.com/memohai/twilight-ai/sdk"
)

func TestContextProjectionCloneDoesNotShareMessageParts(t *testing.T) {
	t.Parallel()

	projection := ContextProjection{
		System: "system",
		Messages: []sdk.Message{sdk.UserMessage("history", sdk.ImagePart{
			Image:     "data:image/png;base64,abc",
			MediaType: "image/png",
		})},
		Query: "current",
		InlineImages: []sdk.ImagePart{{
			Image:     "https://example.test/image.png",
			MediaType: "image/png",
		}},
	}

	clone := projection.Clone()
	clone.Messages[0].Content[0] = sdk.TextPart{Text: "changed"}
	clone.InlineImages[0].Image = "changed"

	if got, ok := projection.Messages[0].Content[0].(sdk.TextPart); ok && got.Text == "changed" {
		t.Fatal("clone mutation changed the source message parts")
	}
	if projection.InlineImages[0].Image == "changed" {
		t.Fatal("clone mutation changed the source inline images")
	}
}

func TestContextProjectionClonePreservesProviderPayload(t *testing.T) {
	t.Parallel()

	want := ContextProjection{
		System:       "system",
		Messages:     []sdk.Message{sdk.UserMessage("history")},
		Query:        "current",
		InlineImages: []sdk.ImagePart{{Image: "data:image/png;base64,abc", MediaType: "image/png"}},
	}

	got := want.Clone()
	if got.System != want.System || got.Query != want.Query {
		t.Fatalf("clone lost scalar provider payload: got=%#v want=%#v", got, want)
	}
	if len(got.Messages) != 1 || len(got.InlineImages) != 1 {
		t.Fatalf("clone lost provider payload: got=%#v", got)
	}
	if got.Messages[0].Role != sdk.MessageRoleUser || got.InlineImages[0].MediaType != "image/png" {
		t.Fatalf("clone changed provider payload: got=%#v", got)
	}
}
