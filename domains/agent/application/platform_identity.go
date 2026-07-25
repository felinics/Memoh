package application

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const platformIdentitiesIntro = "## Platform Identities\n\nThese XML tags describe your own known account identities across connected platforms.\n"

type identityAttr struct {
	Name  string
	Value string
}

func buildPlatformIdentitiesSection(configs []PlatformIdentity) string {
	xmlBlock := buildPlatformIdentitiesXML(configs)
	if xmlBlock == "" {
		return ""
	}
	return platformIdentitiesIntro + "\n" + xmlBlock
}

func buildPlatformIdentitiesXML(configs []PlatformIdentity) string {
	if len(configs) == 0 {
		return ""
	}
	sorted := make([]PlatformIdentity, len(configs))
	copy(sorted, configs)
	sort.Slice(sorted, func(i, j int) bool {
		left := sorted[i]
		right := sorted[j]
		if cmp := strings.Compare(left.Platform, right.Platform); cmp != 0 {
			return cmp < 0
		}
		if cmp := strings.Compare(left.ExternalIdentity, right.ExternalIdentity); cmp != 0 {
			return cmp < 0
		}
		return strings.Compare(left.ID, right.ID) < 0
	})

	lines := make([]string, 0, len(sorted))
	for _, cfg := range sorted {
		line := buildPlatformIdentityLine(cfg)
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func buildPlatformIdentityLine(cfg PlatformIdentity) string {
	channelName := strings.TrimSpace(cfg.Platform)
	if channelName == "" {
		return ""
	}
	attrs := []identityAttr{{
		Name:  "channel",
		Value: channelName,
	}}

	keys := make([]string, 0, len(cfg.SelfIdentity))
	for key := range cfg.SelfIdentity {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	seen := map[string]struct{}{
		"channel": {},
	}
	for _, key := range keys {
		name, ok := normalizeIdentityAttrName(key)
		if !ok {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		value, ok := stringifyIdentityAttrValue(cfg.SelfIdentity[key])
		if !ok {
			continue
		}
		if name == "username" {
			value = normalizeIdentityUsername(value)
			if strings.TrimSpace(value) == "" {
				continue
			}
		}
		attrs = append(attrs, identityAttr{Name: name, Value: value})
		seen[name] = struct{}{}
	}

	if externalIdentity := strings.TrimSpace(cfg.ExternalIdentity); externalIdentity != "" {
		if _, exists := seen["external_identity"]; !exists {
			attrs = append(attrs, identityAttr{Name: "external_identity", Value: externalIdentity})
		}
	}

	if len(attrs) == 1 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("<identity")
	for _, attr := range attrs {
		sb.WriteByte(' ')
		sb.WriteString(attr.Name)
		sb.WriteString(`="`)
		sb.WriteString(escapeIdentityAttrValue(attr.Value))
		sb.WriteByte('"')
	}
	sb.WriteString("/>")
	return sb.String()
}

func normalizeIdentityAttrName(name string) (string, bool) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", false
	}

	var sb strings.Builder
	for _, r := range trimmed {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '_', r == '-', r == '.':
			sb.WriteRune(r)
		default:
			sb.WriteByte('_')
		}
	}

	normalized := strings.Trim(sb.String(), "_-.")
	if normalized == "" {
		return "", false
	}

	first, _ := utf8.DecodeRuneInString(normalized)
	if !unicode.IsLetter(first) && first != '_' {
		normalized = "attr_" + normalized
	}
	if strings.HasPrefix(strings.ToLower(normalized), "xml") {
		normalized = "attr_" + normalized
	}
	return normalized, true
}

func stringifyIdentityAttrValue(value any) (string, bool) {
	switch v := value.(type) {
	case nil:
		return "", false
	case string:
		s := strings.TrimSpace(v)
		return s, s != ""
	case json.Number:
		s := strings.TrimSpace(v.String())
		return s, s != ""
	case fmt.Stringer:
		s := strings.TrimSpace(v.String())
		return s, s != ""
	case bool:
		return strconv.FormatBool(v), true
	case int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return fmt.Sprint(v), true
	default:
		data, err := json.Marshal(v)
		if err != nil {
			s := strings.TrimSpace(fmt.Sprint(v))
			return s, s != ""
		}
		s := strings.TrimSpace(string(data))
		if s == "" || s == "null" {
			return "", false
		}
		return s, true
	}
}

func normalizeIdentityUsername(username string) string {
	trimmed := strings.TrimSpace(username)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "@") {
		return trimmed
	}
	return "@" + trimmed
}

func escapeIdentityAttrValue(value string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		`"`, "&quot;",
		"<", "&lt;",
		">", "&gt;",
		"'", "&apos;",
	)
	return replacer.Replace(value)
}
