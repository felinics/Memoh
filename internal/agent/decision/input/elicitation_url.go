package input

import (
	"errors"
	"net/url"
	"strings"
)

type elicitationURLInput map[string]any

// ElicitationURLInput adapts a browser step to the shared user-input surface.
// Keep server wording and option identities intact until a surface renders them.
// Question text is plain on every surface, so the address stands on its own
// line: the web form links it, channels show it verbatim.
func ElicitationURLInput(message, address string) (any, error) {
	parsed, err := url.Parse(strings.TrimSpace(address))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return nil, errors.New("unsupported elicitation URL")
	}
	return elicitationURLInput{"questions": []any{map[string]any{
		"text": strings.TrimSpace(message + "\n" + parsed.String()), "kind": QuestionKindSingleSelect,
		"options": []any{map[string]any{"label": "Done"}, map[string]any{"label": "Cancel"}},
	}}}, nil
}
