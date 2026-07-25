package memory

import (
	"path"
	"regexp"
	"strings"
	"unicode"
)

var (
	wikiLinkRe = regexp.MustCompile(`\[\[([^\]]+)\]\]`)
	mdLinkRe   = regexp.MustCompile(`\[([^\]]*)\]\(([^)]+)\)`)
)

// NodeSlug returns the human/LLM-friendly slug used in wiki cross-references.
func NodeSlug(id, subject, topic string) string {
	if slug := slugify(subject); slug != "" {
		return slug
	}
	if slug := slugify(topic); slug != "" {
		return slug
	}
	if idx := strings.LastIndex(id, ":"); idx >= 0 && idx+1 < len(id) {
		return slugify(id[idx+1:])
	}
	return slugify(id)
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func parseMemoryLinks(body string) []string {
	var slugs []string
	seen := map[string]bool{}
	collect := func(raw string) {
		slug := slugify(linkTargetSlug(raw))
		if slug == "" || seen[slug] {
			return
		}
		seen[slug] = true
		slugs = append(slugs, slug)
	}
	for _, m := range wikiLinkRe.FindAllStringSubmatch(body, -1) {
		if len(m) > 1 {
			collect(m[1])
		}
	}
	for _, m := range mdLinkRe.FindAllStringSubmatch(body, -1) {
		if len(m) <= 2 {
			continue
		}
		href := strings.TrimSpace(m[2])
		lower := strings.ToLower(href)
		if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
			continue
		}
		collect(href)
	}
	return slugs
}

func linkTargetSlug(href string) string {
	href = strings.TrimSpace(href)
	lower := strings.ToLower(href)
	if strings.HasSuffix(lower, ".md") {
		return strings.TrimSuffix(path.Base(href), path.Ext(href))
	}
	return href
}
