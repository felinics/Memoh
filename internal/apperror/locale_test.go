package apperror

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

const localeDir = "../../apps/web/src/i18n/locales"

var localeFiles = []string{"en.json", "zh.json", "ja.json"}

// TestLocalesDescribeExactlyTheContract is the codesync(error-catalog) gate.
// The catalog's English Detail is only the no-locale fallback; the strings a
// user actually reads live in the frontend, so the two sides drifting apart
// silently degrades every localized client.
//
// It also covers the errors.kind.* keys, which is what makes an error with no
// catalog code localizable instead of falling back to English.
func TestLocalesDescribeExactlyTheContract(t *testing.T) {
	// upstreamKeys frame a third party's untranslated message as a quotation.
	// They are the one part of errors.* that is not derived from the contract,
	// because the sentence inside them is not ours.
	upstreamKeys := []string{"upstream.attributed", "upstream.anonymous"}

	want := make([]string, 0, len(catalog)+len(allKinds)+len(fieldCodes)+len(upstreamKeys))
	for _, code := range Codes() {
		want = append(want, string(code))
	}
	for _, kind := range allKinds {
		want = append(want, "kind."+kind.String())
	}
	for code := range fieldCodes {
		want = append(want, "field."+string(code))
	}
	want = append(want, upstreamKeys...)
	sort.Strings(want)

	for _, name := range localeFiles {
		t.Run(name, func(t *testing.T) {
			got := errorKeys(t, filepath.Join(localeDir, name))

			for _, key := range want {
				if !slices.Contains(got, key) {
					t.Errorf("locale is missing errors.%s", key)
				}
			}
			for _, key := range got {
				if !slices.Contains(want, key) {
					t.Errorf("locale declares errors.%s, which no code or kind defines", key)
				}
			}
		})
	}
}

// errorKeys flattens the locale's errors object into dotted leaf paths, which
// is the same shape a code ("bot.name_taken") already has.
func errorKeys(t *testing.T, path string) []string {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read locale: %v", err)
	}
	var document struct {
		Errors map[string]any `json:"errors"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("parse locale: %v", err)
	}

	var keys []string
	var walk func(prefix string, node map[string]any)
	walk = func(prefix string, node map[string]any) {
		for key, value := range node {
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			if nested, ok := value.(map[string]any); ok {
				walk(path, nested)
				continue
			}
			keys = append(keys, path)
		}
	}
	walk("", document.Errors)

	sort.Strings(keys)
	return keys
}

// TestCodeNamingFollowsTheContract keeps the catalog from accumulating the
// naming variants this project already paid for once: acp_agent_not_found and
// acp.runtime_not_found were two live conventions for the same domain.
func TestCodeNamingFollowsTheContract(t *testing.T) {
	for _, code := range Codes() {
		segments := strings.Split(string(code), ".")
		if len(segments) != 2 {
			t.Errorf("code %q must be <domain>.<condition>", code)
			continue
		}
		for _, segment := range segments {
			if segment == "" || segment != strings.ToLower(segment) || strings.ContainsAny(segment, "-. ") {
				t.Errorf("code %q must use lowercase snake_case segments", code)
			}
		}
	}
}
