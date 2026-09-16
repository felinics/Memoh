package bridge

import (
	"os"
	"testing"

	"github.com/felinics/memoh/internal/workspace/controlpath"
)

func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "memoh-control-tests-")
	if err != nil {
		panic(err)
	}
	controlpath.LinuxRoot = root
	code := m.Run()
	_ = os.RemoveAll(root)
	os.Exit(code)
}
