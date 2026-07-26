package http

import (
	"html/template"
	"strings"
)

func ExecuteHTMLTemplate(tpl *template.Template, data any) string {
	var b strings.Builder
	_ = tpl.Execute(&b, data)
	return b.String()
}

func SessionMetadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

func FirstHeaderValue(raw string) string {
	if raw == "" {
		return ""
	}
	parts := strings.Split(raw, ",")
	return strings.TrimSpace(parts[0])
}
