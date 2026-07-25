package secret

import (
	"strings"
	"testing"
)

func TestNewKeyFormatAndEntropy(t *testing.T) {
	t.Parallel()
	a, err := NewKey()
	if err != nil {
		t.Fatalf("NewKey() error = %v", err)
	}
	b, err := NewKey()
	if err != nil {
		t.Fatalf("NewKey() error = %v", err)
	}
	if err := ValidateKeyFormat(a); err != nil {
		t.Fatalf("ValidateKeyFormat(a) = %v", err)
	}
	if a == b {
		t.Fatal("NewKey() returned identical keys")
	}
	if !strings.HasPrefix(a, keyPrefix) {
		t.Fatalf("key prefix = %q, want %q", a[:len(keyPrefix)], keyPrefix)
	}
}

func TestValidateKeyFormatRejectsMalformed(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"", "mrk_", "mrk_zz", "tok_" + strings.Repeat("ab", 32), keyPrefix + strings.Repeat("g", keyHexChars)} {
		if err := ValidateKeyFormat(key); err == nil {
			t.Fatalf("ValidateKeyFormat(%q) = nil, want error", key)
		}
	}
}
