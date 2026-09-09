package toolset

import "github.com/felinics/memoh/internal/agent/tool/internal/toolname"

// Available is the set of built-in tool names registered for a session.
// The backing set lives outside the public tools package so Usage code cannot
// forge availability by constructing or mutating the map directly.
type Available struct {
	names map[toolname.Name]struct{}
}

func New(names []toolname.Name) Available {
	available := Available{names: make(map[toolname.Name]struct{}, len(names))}
	for _, name := range names {
		if name.IsZero() {
			continue
		}
		available.names[name] = struct{}{}
	}
	return available
}

func (a Available) Has(name toolname.Name) bool {
	if a.names == nil || name.IsZero() {
		return false
	}
	_, ok := a.names[name]
	return ok
}

func (a Available) Ref(name toolname.Name) (string, bool) {
	if !a.Has(name) {
		return "", false
	}
	return "`" + name.String() + "`", true
}

func (a Available) Refs(names ...toolname.Name) []string {
	refs := make([]string, 0, len(names))
	for _, name := range names {
		if ref, ok := a.Ref(name); ok {
			refs = append(refs, ref)
		}
	}
	return refs
}
