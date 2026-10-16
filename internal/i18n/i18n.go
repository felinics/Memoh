// Package i18n provides a tiny, zero-dependency localization catalog for the
// IM command UI (slash commands, inline-keyboard buttons, renderer chrome).
//
// It is deliberately minimal: catalogs are embedded JSON (mirroring the web
// app's nested-key convention but owned by the backend, never importing the
// web tree), flattened to dotted keys at init. Lookups fall back from the
// requested locale to English to the key itself, so a missing translation is
// always visible-but-safe rather than a hard failure.
//
// This layer localizes the command UI only. Its locale comes from
// settings.CommandUILanguage, and "auto" resolves to the
// server default (English) — it never follows per-message content, the IM
// platform's user language, or the agent's reply language.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

//go:embed locales/*.json
var localeFS embed.FS

// DefaultLocale is the server default, used when a bot's command-UI language is
// "auto" or unrecognized.
const DefaultLocale = "en"

// Supported lists the locales that ship with a bundled catalog, in display
// order (used by the /settings language picker).
var Supported = []string{"en", "zh", "ja"}

// catalogs maps locale -> flattened (dotted-key) message table. Populated once
// at init from the embedded JSON; treated as immutable thereafter.
var catalogs = map[string]map[string]string{}

func init() {
	for _, loc := range Supported {
		data, err := localeFS.ReadFile("locales/" + loc + ".json")
		if err != nil {
			panic("i18n: missing embedded locale catalog: " + loc + ": " + err.Error())
		}
		var nested map[string]any
		if err := json.Unmarshal(data, &nested); err != nil {
			panic("i18n: invalid locale catalog json: " + loc + ": " + err.Error())
		}
		flat := map[string]string{}
		flatten("", nested, flat)
		catalogs[loc] = flat
	}
}

// flatten collapses a nested string map into dotted keys
// (e.g. {"cmd":{"settings":{"title":"x"}}} -> {"cmd.settings.title":"x"}).
func flatten(prefix string, in map[string]any, out map[string]string) {
	for k, v := range in {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		switch val := v.(type) {
		case string:
			out[key] = val
		case map[string]any:
			flatten(key, val, out)
		default:
			// Non-string leaves are not expected in a UI catalog; skip them so a
			// stray number/array can never crash startup.
		}
	}
}

// Resolve maps a stored command-UI language setting to a concrete locale.
// Any value not in Supported (including "auto", "", or garbage) resolves to
// DefaultLocale.
func Resolve(setting string) string {
	s := strings.ToLower(strings.TrimSpace(setting))
	for _, loc := range Supported {
		if s == loc {
			return loc
		}
	}
	return DefaultLocale
}

// IsSupported reports whether locale is a bundled locale (case-insensitive).
func IsSupported(locale string) bool {
	s := strings.ToLower(strings.TrimSpace(locale))
	for _, loc := range Supported {
		if s == loc {
			return true
		}
	}
	return false
}

// Localizer renders catalog keys for one resolved locale. The zero value is not
// used; construct via New. A nil *Localizer is safe to call (it behaves as the
// default locale), so un-wired call sites degrade gracefully.
type Localizer struct {
	locale string
}

// New returns a Localizer for the given setting value, resolving "auto"/unknown
// to the default locale.
func New(setting string) *Localizer {
	return &Localizer{locale: Resolve(setting)}
}

// Locale returns the resolved locale string (e.g. "en", "zh").
func (l *Localizer) Locale() string {
	if l == nil || l.locale == "" {
		return DefaultLocale
	}
	return l.locale
}

// T returns the localized string for key, substituting named placeholders
// ("{name}") from the optional params map. Resolution falls back from the
// Localizer's locale to the default locale to the key itself, so a missing
// translation is always visible-but-safe.
func (l *Localizer) T(key string, params ...map[string]any) string {
	locale := DefaultLocale
	if l != nil && l.locale != "" {
		locale = l.locale
	}
	val, ok := lookup(locale, key)
	if !ok && locale != DefaultLocale {
		val, ok = lookup(DefaultLocale, key)
	}
	if !ok {
		val = key
	}
	if len(params) > 0 {
		val = substitute(val, params[0])
	}
	return val
}

func lookup(locale, key string) (string, bool) {
	cat, ok := catalogs[locale]
	if !ok {
		return "", false
	}
	v, ok := cat[key]
	return v, ok
}

// substitute replaces "{name}" placeholders with stringified param values.
// Keys are sorted so replacement is deterministic regardless of map order.
func substitute(s string, params map[string]any) string {
	if len(params) == 0 {
		return s
	}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(params)*2)
	for _, k := range keys {
		pairs = append(pairs, "{"+k+"}", fmt.Sprint(params[k]))
	}
	return strings.NewReplacer(pairs...).Replace(s)
}
