package builtin

import (
	"context"
	"fmt"
	"strings"
	"testing"

	adapters "github.com/felinics/memoh/internal/memory/adapters"
)

func TestFileRuntimeRejectsEmptyMemoryWithoutHTTPError(t *testing.T) {
	t.Parallel()
	runtime := newFileRuntime(newFakeStore())

	_, err := runtime.Add(context.Background(), adapters.AddRequest{BotID: "bot-1"})
	if err == nil {
		t.Fatal("Add() error = nil, want message validation error")
	}
	if strings.Contains(fmt.Sprintf("%T", err), "echo") {
		t.Fatalf("Add() returned HTTP-layer error type %T", err)
	}
	if !strings.Contains(err.Error(), "message is required") {
		t.Fatalf("Add() error = %q, want message validation", err.Error())
	}
}

func TestFileRuntimeCompactIsDisabled(t *testing.T) {
	t.Parallel()
	runtime := newFileRuntime(newFakeStore())

	if _, err := runtime.Compact(context.Background(), map[string]any{"bot_id": "bot-1"}, 0.5, 0); err == nil {
		t.Fatal("expected file runtime compact to be disabled")
	}
}
