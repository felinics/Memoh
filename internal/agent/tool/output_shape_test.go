package tools

import "testing"

// mapSlice reads a list of objects from a tool output. A handler invoked
// directly returns []map[string]any; one invoked through the executor returns
// the JSON decoding, []any of map[string]any. Both are the same list.
func mapSlice(value any) ([]map[string]any, bool) {
	switch list := value.(type) {
	case []map[string]any:
		return list, true
	case []any:
		out := make([]map[string]any, 0, len(list))
		for _, item := range list {
			entry, ok := item.(map[string]any)
			if !ok {
				return nil, false
			}
			out = append(out, entry)
		}
		return out, true
	}
	return nil, false
}

func asMapSlice(t *testing.T, value any) []map[string]any {
	t.Helper()
	list, ok := mapSlice(value)
	if !ok {
		t.Fatalf("expected list of maps, got %T", value)
	}
	return list
}
