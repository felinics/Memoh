package workspace

import (
	"testing"
)

func TestResourceLimitCapabilitiesForKataRuntime(t *testing.T) {
	t.Parallel()

	caps := ResourceLimitCapabilitiesFor("container", "io.containerd.kata.v2")
	if !caps.CPU.HardLimitSupported {
		t.Fatal("kata runtime should support CPU hard limits")
	}
	if !caps.Memory.HardLimitSupported {
		t.Fatal("kata runtime should support memory hard limits")
	}
	if caps.Storage.HardLimitSupported {
		t.Fatal("kata runtime should not report storage hard limits without a disk quota implementation")
	}
	if !caps.Storage.SoftLimitSupported {
		t.Fatal("kata runtime should keep storage soft limits")
	}
}
