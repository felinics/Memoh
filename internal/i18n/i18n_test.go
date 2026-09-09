package i18n

import (
	"reflect"
	"regexp"
	"sort"
	"testing"
)

func TestResolve(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{"en", "en"},
		{"zh", "zh"},
		{"ja", "ja"},
		{"ZH", "zh"},    // case-insensitive
		{"JA", "ja"},    // case-insensitive
		{" en ", "en"},  // trimmed
		{"auto", "en"},  // auto → default
		{"", "en"},      // empty → default
		{"fr", "en"},    // unsupported → default
		{"zh-CN", "en"}, // not an exact match → default (chat-language values don't leak in)
	}
	for _, tc := range cases {
		if got := Resolve(tc.in); got != tc.want {
			t.Errorf("Resolve(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestLocalizerTLookupAndFallback(t *testing.T) {
	t.Parallel()

	// Direct hit in zh.
	if got := New("zh").T("cmd.settings.title"); got != "⚙️ 机器人设置" {
		t.Errorf("zh title = %q", got)
	}
	// auto resolves to en.
	if got := New("auto").T("cmd.settings.title"); got != "⚙️ Bot Settings" {
		t.Errorf("auto title = %q", got)
	}
	// A key missing in zh would fall back to en; a key missing everywhere
	// returns the key verbatim (visible-but-safe).
	if got := New("zh").T("totally.missing.key"); got != "totally.missing.key" {
		t.Errorf("missing key = %q, want the key itself", got)
	}
}

func TestLocalizerTParams(t *testing.T) {
	t.Parallel()
	got := New("en").T("chrome.currentModel", map[string]any{"model": "Claude Opus"})
	if got != "Current model: Claude Opus" {
		t.Errorf("param substitution = %q", got)
	}
	// Numeric params stringify.
	zh := New("zh").T("cmd.cron.everyNMinutes", map[string]any{"n": 30})
	if zh != "每 30 分钟" {
		t.Errorf("zh cron interval = %q", zh)
	}
}

func TestNilLocalizerIsSafe(t *testing.T) {
	t.Parallel()
	var l *Localizer
	if got := l.T("cmd.settings.title"); got != "⚙️ Bot Settings" {
		t.Errorf("nil localizer should render default locale, got %q", got)
	}
	if got := l.Locale(); got != DefaultLocale {
		t.Errorf("nil localizer Locale() = %q, want %q", got, DefaultLocale)
	}
}

func TestSupportedCatalogsParity(t *testing.T) {
	t.Parallel()
	// Every key present in en must exist in every other supported locale, so a
	// translation is never silently missing (it would fall back to en, which is
	// correct but easy to forget — this catches gaps at test time).
	en := catalogs["en"]
	if len(en) == 0 {
		t.Fatal("en catalog is empty")
	}
	for _, loc := range Supported {
		if loc == "en" {
			continue
		}
		cat := catalogs[loc]
		for key := range en {
			if _, ok := cat[key]; !ok {
				t.Errorf("locale %q missing key %q present in en", loc, key)
			}
		}
		// Reverse direction: a key in another locale but absent from en is also a
		// bug — en is the fallback source, so such a key resolves to the raw key
		// string (never the translation) for any locale that lacks it. The
		// one-directional check above would not catch this.
		for key := range cat {
			if _, ok := en[key]; !ok {
				t.Errorf("locale %q has key %q absent from en (en is the fallback source)", loc, key)
			}
		}
	}
}

func TestIsSupported(t *testing.T) {
	t.Parallel()
	for _, ok := range []string{"en", "zh", "ja", "ZH", "JA", " en "} {
		if !IsSupported(ok) {
			t.Errorf("IsSupported(%q) = false, want true", ok)
		}
	}
	for _, no := range []string{"auto", "", "fr", "zh-CN"} {
		if IsSupported(no) {
			t.Errorf("IsSupported(%q) = true, want false", no)
		}
	}
}

var placeholderRe = regexp.MustCompile(`\{([a-zA-Z0-9_]+)\}`)

func placeholderSet(template string) []string {
	matches := placeholderRe.FindAllStringSubmatch(template, -1)
	seen := make(map[string]bool, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return out
}

// TestSupportedCatalogsPlaceholderParity pins that every key's {placeholder} set
// is identical across locales. TestSupportedCatalogsParity guarantees the keys
// match; this guarantees the *templates* agree on their substitution variables.
// A translator dropping a placeholder (e.g. zh "{minutes}" → "每分钟") would
// otherwise render a literal "{minutes}" to users, or silently lose data — a
// class of bug the key-set parity check cannot see.
func TestSupportedCatalogsPlaceholderParity(t *testing.T) {
	t.Parallel()
	en := catalogs["en"]
	if len(en) == 0 {
		t.Fatal("en catalog is empty")
	}
	for _, loc := range Supported {
		if loc == "en" {
			continue
		}
		cat := catalogs[loc]
		for key, enTmpl := range en {
			locTmpl, ok := cat[key]
			if !ok {
				continue // key-presence gaps are covered by TestSupportedCatalogsParity
			}
			enPH, locPH := placeholderSet(enTmpl), placeholderSet(locTmpl)
			if !reflect.DeepEqual(enPH, locPH) {
				t.Errorf("key %q placeholder mismatch: en=%v %q=%v\n  en:  %q\n  %s: %q",
					key, enPH, loc, locPH, enTmpl, loc, locTmpl)
			}
		}
	}
}
