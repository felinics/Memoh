package workspacedeps

import (
	"path"

	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

// removalScript cleans only the managed home under the existing lock and
// completion receipt. Image baselines and user files remain untouched. A
// recipe exit must not skip cleanup; failure must stop it.
func removalScript(_ catalog.Dependency, script, _ string) string {
	return "(\n" + script + "\n)\nrm -rf -- \"$MEMOH_DEP_HOME\"\n"
}

func actionScript(dep catalog.Dependency, action catalog.Action, script string) string {
	if action == catalog.ActionRemove {
		return removalScript(dep, script, path.Dir(toolkitBinDir))
	}
	return script
}
