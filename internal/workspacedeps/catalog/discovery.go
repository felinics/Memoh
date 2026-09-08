package catalog

import (
	"fmt"
	"regexp"
)

// Discovery constructs a read-only command catalog for built-in launchers.
// It carries no downloadable definition, platform promise, or executable script.
// This keeps installed tools discoverable before any registry cache exists.
func Discovery(commands map[string]string) (*Catalog, error) {
	c := Empty()
	commandPattern := regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._+-]*$`)
	for id, command := range commands {
		if !idPattern.MatchString(id) || !commandPattern.MatchString(command) {
			return nil, fmt.Errorf("catalog: invalid discovery binding %q: %q", id, command)
		}
		dep := Dependency{ID: id, Name: id, Source: SourceImage, Category: CategoryTool, Provides: []string{command}, ManifestDigest: "builtin-discovery:" + command}
		item := &entry{dir: id, dep: dep, scripts: make(map[Action]scriptFile)}
		c.entries = append(c.entries, item)
		c.byID[id] = item
	}
	return c, nil
}
