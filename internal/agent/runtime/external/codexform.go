package external

import (
	"errors"

	userinput "github.com/felinics/memoh/internal/agent/decision/input"
)

// CodexFormMarkers reads the markers Codex puts in a form property's _meta:
// the free-text "other" answer of a question, and secret input. They are
// Codex's wire convention, sent alike by its app-server and by codex-acp,
// which runs as a generic ACP agent; the Codex driver and the ACP client both
// read forms with them.
var CodexFormMarkers = userinput.ElicitationExtension{
	CustomAnswer: func(meta map[string]any) (string, bool, error) {
		codex, ok := meta["codex"].(map[string]any)
		if !ok {
			return "", false, nil
		}
		other, exists := codex["isOtherAnswer"]
		if !exists {
			return "", false, nil
		}
		isOther, ok := other.(bool)
		if !ok {
			return "", false, errors.New("codex isOtherAnswer metadata must be a boolean")
		}
		if !isOther {
			return "", false, nil
		}
		target, err := userinput.RequiredMetaString(codex, "questionId", "codex custom-answer metadata")
		return target, true, err
	},
	Secret: func(meta map[string]any) bool {
		codex, _ := meta["codex"].(map[string]any)
		secret, _ := codex["isSecret"].(bool)
		return secret
	},
}
