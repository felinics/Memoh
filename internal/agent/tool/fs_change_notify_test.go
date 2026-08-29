package tools

import (
	"errors"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
)

func fsNotifyTool(name string, execErr error) sdk.Tool {
	return sdk.Tool{
		Name: name,
		Execute: func(_ *sdk.ToolExecContext, _ any) (any, error) {
			if execErr != nil {
				return nil, execErr
			}
			return "ok", nil
		},
	}
}

func runWrapped(t *testing.T, tool sdk.Tool, input any) {
	t.Helper()
	if _, err := tool.Execute(&sdk.ToolExecContext{}, input); err != nil {
		t.Fatalf("execute: %v", err)
	}
}

func TestWrapFSChangeNotifyReportsWriteAndEditPaths(t *testing.T) {
	var got [][]string
	wrapped := WrapFSChangeNotify(
		[]sdk.Tool{fsNotifyTool("write", nil), fsNotifyTool("edit", nil)},
		func(paths []string) { got = append(got, paths) },
	)

	runWrapped(t, wrapped[0], map[string]any{"path": "/data/a.txt", "content": "x"})
	runWrapped(t, wrapped[1], map[string]any{"path": "/data/b.txt", "old_text": "x", "new_text": "y"})

	if len(got) != 2 {
		t.Fatalf("notify calls = %d, want 2", len(got))
	}
	if len(got[0]) != 1 || got[0][0] != "/data/a.txt" {
		t.Fatalf("write paths = %v", got[0])
	}
	if len(got[1]) != 1 || got[1][0] != "/data/b.txt" {
		t.Fatalf("edit paths = %v", got[1])
	}
}

func TestWrapFSChangeNotifyWildcardsExecAndApplyPatch(t *testing.T) {
	var got [][]string
	wrapped := WrapFSChangeNotify(
		[]sdk.Tool{fsNotifyTool("exec", nil), fsNotifyTool("apply_patch", nil)},
		func(paths []string) { got = append(got, paths) },
	)

	runWrapped(t, wrapped[0], map[string]any{"command": "make build"})
	runWrapped(t, wrapped[1], map[string]any{"patch": "..."})

	if len(got) != 2 || got[0] != nil || got[1] != nil {
		t.Fatalf("notify calls = %v, want two wildcard (nil) calls", got)
	}
}

func TestWrapFSChangeNotifyWildcardsRelativeWritePath(t *testing.T) {
	var got [][]string
	wrapped := WrapFSChangeNotify(
		[]sdk.Tool{fsNotifyTool("write", nil)},
		func(paths []string) { got = append(got, paths) },
	)

	runWrapped(t, wrapped[0], map[string]any{"path": "notes/a.txt", "content": "x"})

	if len(got) != 1 || got[0] != nil {
		t.Fatalf("notify calls = %v, want one wildcard call", got)
	}
}

func TestWrapFSChangeNotifySkipsFailuresAndOtherTools(t *testing.T) {
	var calls int
	wrapped := WrapFSChangeNotify(
		[]sdk.Tool{fsNotifyTool("write", errors.New("boom")), fsNotifyTool("send_message", nil)},
		func([]string) { calls++ },
	)

	if _, err := wrapped[0].Execute(&sdk.ToolExecContext{}, map[string]any{"path": "/data/a.txt"}); err == nil {
		t.Fatal("expected execute error")
	}
	runWrapped(t, wrapped[1], map[string]any{"text": "hi"})

	if calls != 0 {
		t.Fatalf("notify calls = %d, want 0", calls)
	}
}

func TestWrapFSChangeNotifyNilNotifierReturnsInput(t *testing.T) {
	in := []sdk.Tool{fsNotifyTool("write", nil)}
	out := WrapFSChangeNotify(in, nil)
	if len(out) != 1 {
		t.Fatalf("len = %d", len(out))
	}
	runWrapped(t, out[0], map[string]any{"path": "/data/a.txt"})
}
