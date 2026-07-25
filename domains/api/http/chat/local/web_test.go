package local

import "testing"

func TestWebAdapterDescriptorAdvertisesMarkdown(t *testing.T) {
	t.Parallel()

	caps := NewWebAdapter(nil).Descriptor().Capabilities
	if !caps.Markdown {
		t.Fatal("Web descriptor must advertise Markdown for the local rich renderer")
	}
}
