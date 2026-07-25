package gateway

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeWebhookEndpoint(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{
			name: "trycloudflare endpoint",
			raw:  "https://integrating-hence-penn-paris.trycloudflare.com/channels/line/webhook/cfg-1",
			want: "https://integrating-hence-penn-paris.trycloudflare.com/channels/line/webhook/cfg-1",
		},
		{
			name: "custom public endpoint lowercases host",
			raw:  "https://Hooks.Example.org/channels/line/webhook/cfg-1",
			want: "https://hooks.example.org/channels/line/webhook/cfg-1",
		},
		{
			name:    "localhost rejected",
			raw:     "https://localhost/channels/line/webhook/cfg-1",
			wantErr: true,
		},
		{
			name:    "query rejected",
			raw:     "https://hooks.example.org/channels/line/webhook/cfg-1?x=1",
			wantErr: true,
		},
		{
			name:    "wrong config rejected",
			raw:     "https://hooks.example.org/channels/line/webhook/other",
			wantErr: true,
		},
		{
			name:    "http rejected",
			raw:     "http://hooks.example.org/channels/line/webhook/cfg-1",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeWebhookEndpoint(tt.raw, ChannelTypeLine, "cfg-1")
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidWebhookEndpoint) {
					t.Fatalf("error = %v, want ErrInvalidWebhookEndpoint", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeWebhookEndpoint returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("endpoint = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeWebhookEndpointRejectsTooLong(t *testing.T) {
	raw := "https://hooks.example.org/channels/line/webhook/cfg-1" + strings.Repeat("a", maxWebhookEndpointLength)
	if _, err := normalizeWebhookEndpoint(raw, ChannelTypeLine, "cfg-1"); !errors.Is(err, ErrInvalidWebhookEndpoint) {
		t.Fatalf("error = %v, want ErrInvalidWebhookEndpoint", err)
	}
}
