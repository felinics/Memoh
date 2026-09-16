//go:build linux

package payloadlease

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEpochCommandRequiresCurrentProcNamespace(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"sys/kernel/random/boot_id", "1/stat"} {
		data, err := os.ReadFile(filepath.Join("/proc", name)) //nolint:gosec // name is one of the two fixed procfs records listed above.
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "self/ns"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("pid:[1234]", filepath.Join(root, "self/ns/pid")); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"NSpid:\t42\n", "NSpid:\t123 42\n", "Name:\tbridge\n", "NSpid:\t0\n"} {
		if err := os.WriteFile(filepath.Join(root, "self/status"), []byte(status), 0o600); err != nil {
			t.Fatal(err)
		}
		command := exec.CommandContext(t.Context(), "sh", "-s")
		command.Stdin = strings.NewReader(strings.ReplaceAll(EpochCommand, "/proc/", quote(root)+"/"))
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("epoch command: %s %v", output, err)
		}
		if status == "NSpid:\t42\n" {
			if !strings.HasSuffix(strings.TrimSpace(string(output)), ":pid:[1234]") {
				t.Fatalf("self namespace proof was rejected: %q", output)
			}
		} else if len(output) != 0 {
			t.Fatalf("unproven namespace status %q granted lifetime %q", status, output)
		}
	}
}
