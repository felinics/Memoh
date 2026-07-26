package workspace

import "testing"

func TestResourceLimitCapabilitiesForContainerdRuntime(t *testing.T) {
	t.Parallel()

	caps := ResourceLimitCapabilitiesFor("container", "io.containerd.runc.v2")
	if !caps.CPU.HardLimitSupported {
		t.Fatal("containerd runtime should support CPU hard limits")
	}
	if !caps.Memory.HardLimitSupported {
		t.Fatal("containerd runtime should support memory hard limits")
	}
	if caps.Storage.HardLimitSupported {
		t.Fatal("containerd runtime should not report storage hard limits without a disk quota implementation")
	}
	if !caps.Storage.SoftLimitSupported {
		t.Fatal("containerd runtime should keep storage soft limits")
	}
}
