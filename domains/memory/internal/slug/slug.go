package slug

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
	if slug := Slugify(subject); slug != "" {
		return slug
	}
	if slug := Slugify(topic); slug != "" {
		return slug
	}
	if idx := strings.LastIndex(id, ":"); idx >= 0 && idx+1 < len(id) {
		return Slugify(id[idx+1:])
	}
	return Slugify(id)
}

// Slugify normalizes a user/LLM-facing memory label for [[slug]] links.
func Slugify(s string) string {
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

// ParseMemoryLinks extracts referenced slugs from a memory body. It supports
// [[slug]] and markdown links. HTTP(S) links are ignored.
func ParseMemoryLinks(body string) []string {
	var slugs []string
	seen := map[string]bool{}
	collect := func(raw string) {
		slug := Slugify(linkTargetSlug(raw))
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

// RenderWikiLinks converts [[label]] links using resolve. Returning ok=false
// keeps the original wiki link.
func RenderWikiLinks(body string, resolve func(label, normalizedSlug string) (href string, ok bool)) string {
	if strings.TrimSpace(body) == "" || resolve == nil {
		return body
	}
	return wikiLinkRe.ReplaceAllStringFunc(body, func(match string) string {
		parts := wikiLinkRe.FindStringSubmatch(match)
		if len(parts) < 2 {
			return match
		}
		label := strings.TrimSpace(parts[1])
		normalized := Slugify(label)
		href, ok := resolve(label, normalized)
		if !ok || strings.TrimSpace(href) == "" {
			return match
		}
		return "[" + label + "](" + strings.TrimSpace(href) + ")"
	})
}

// RestoreMarkdownFileLinks converts relative markdown links to .md files back
// into [[slug]] links so repeated syncs remain stable.
func RestoreMarkdownFileLinks(body string) string {
	if strings.TrimSpace(body) == "" {
		return body
	}
	return mdLinkRe.ReplaceAllStringFunc(body, func(match string) string {
		parts := mdLinkRe.FindStringSubmatch(match)
		if len(parts) < 3 {
			return match
		}
		href := strings.TrimSpace(parts[2])
		lower := strings.ToLower(href)
		if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
			return match
		}
		if !strings.HasSuffix(lower, ".md") {
			return match
		}
		slug := Slugify(strings.TrimSuffix(path.Base(href), path.Ext(href)))
		if slug == "" {
			return match
		}
		return "[[" + slug + "]]"
	})
}

func linkTargetSlug(href string) string {
	href = strings.TrimSpace(href)
	lower := strings.ToLower(href)
	if strings.HasSuffix(lower, ".md") {
		return strings.TrimSuffix(path.Base(href), path.Ext(href))
	}
	return href
}
