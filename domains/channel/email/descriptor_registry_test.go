package email

import "testing"

func TestDescriptorRegistryPreservesSplitProfileMetadataAndNormalization(t *testing.T) {
	registry := NewDescriptorRegistry()
	metadata := registry.ListMeta()
	if len(metadata) != 3 {
		t.Fatalf("provider metadata count = %d, want 3", len(metadata))
	}

	tests := []struct {
		name   ProviderName
		config map[string]any
		key    string
		want   any
	}{
		{
			name: ProviderGeneric,
			config: map[string]any{
				"smtp_host": "smtp.example.com",
				"imap_host": "imap.example.com",
				"username":  "user@example.com",
				"password":  "secret",
			},
			key:  "smtp_port",
			want: float64(587),
		},
		{
			name:   ProviderGmail,
			config: map[string]any{"email_address": "user@gmail.com", "client_secret": "legacy"},
			key:    "email_address",
			want:   "user@gmail.com",
		},
		{
			name:   ProviderMailgun,
			config: map[string]any{"domain": "mg.example.com", "api_key": "secret"},
			key:    "inbound_mode",
			want:   "poll",
		},
	}

	for _, tt := range tests {
		t.Run(string(tt.name), func(t *testing.T) {
			adapter, err := registry.Get(tt.name)
			if err != nil {
				t.Fatalf("get descriptor: %v", err)
			}
			normalized, err := adapter.NormalizeConfig(tt.config)
			if err != nil {
				t.Fatalf("normalize config: %v", err)
			}
			if got := normalized[tt.key]; got != tt.want {
				t.Fatalf("normalized[%q] = %#v, want %#v", tt.key, got, tt.want)
			}
			if tt.name == ProviderGmail {
				if _, exists := normalized["client_secret"]; exists {
					t.Fatal("legacy Gmail client secret was not stripped")
				}
			}
		})
	}
}
