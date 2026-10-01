package fetchproviders

import (
	"context"
	"testing"
)

func TestFirecrawlProviderMetadata(t *testing.T) {
	service := &Service{}
	if !isValidProviderName(ProviderFirecrawl) {
		t.Fatal("Firecrawl must be accepted as a fetch provider")
	}
	for _, meta := range service.ListMeta(context.Background()) {
		if meta.Provider != string(ProviderFirecrawl) {
			continue
		}
		fields := meta.ConfigSchema.Fields
		if fields["api_key"].Type != "secret" || !fields["api_key"].Required {
			t.Fatalf("api_key schema = %#v", fields["api_key"])
		}
		if fields["base_url"].Example != "https://api.firecrawl.dev/v2/scrape" {
			t.Fatalf("base_url schema = %#v", fields["base_url"])
		}
		return
	}
	t.Fatal("Firecrawl metadata not found")
}
