package workspace

import (
	"reflect"
	"testing"

	runtimeworkspace "github.com/memohai/memoh/domains/runtime/workspace"
)

func TestWorkspaceCDIDevicesLabelRoundTrip(t *testing.T) {
	t.Parallel()

	devices := []string{" nvidia.com/gpu=0 ", "amd.com/gpu=1", "nvidia.com/gpu=0"}
	value := workspaceCDIDevicesLabelValue(devices)
	got := workspaceCDIDevicesFromLabels(map[string]string{
		runtimeworkspace.WorkspaceCDIDevicesLabelKey: value,
	})

	want := []string{"nvidia.com/gpu=0", "amd.com/gpu=1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected devices %v, got %v", want, got)
	}
}

func TestWorkspaceCDIDevicesFromLabelsIgnoresMissingValue(t *testing.T) {
	t.Parallel()

	if got := workspaceCDIDevicesFromLabels(nil); len(got) != 0 {
		t.Fatalf("expected empty devices for nil labels, got %v", got)
	}
}
