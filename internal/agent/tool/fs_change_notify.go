package tools

import (
	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/fsevent"
)

// FSChangeNotifier receives the workspace paths touched by a successful
// fs-mutating tool execution. nil means unknown scope (exec, apply_patch, or
// a path that cannot be resolved host-side).
type FSChangeNotifier func(paths []string)

// WrapFSChangeNotify wraps the fs-mutating native tools so every successful
// execution reports its touched paths, regardless of which surface (web,
// channel, schedule, background) ran the turn. Classification lives in
// fsevent.ToolChange, shared with the ACP event path.
func WrapFSChangeNotify(sdkTools []sdk.Tool, notify FSChangeNotifier) []sdk.Tool {
	if notify == nil || len(sdkTools) == 0 {
		return sdkTools
	}
	wrapped := make([]sdk.Tool, len(sdkTools))
	copy(wrapped, sdkTools)
	for i := range wrapped {
		execute := wrapped[i].Execute
		if execute == nil {
			continue
		}
		name := wrapped[i].Name
		if _, mutating := fsevent.ToolChange(name, nil); !mutating {
			continue
		}
		wrapped[i].Execute = func(ctx *sdk.ToolExecContext, input any) (any, error) {
			output, err := execute(ctx, input)
			if err != nil {
				return output, err
			}
			paths, _ := fsevent.ToolChange(name, input)
			notify(paths)
			return output, nil
		}
	}
	return wrapped
}
