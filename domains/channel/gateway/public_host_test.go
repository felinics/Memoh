package gateway

import "testing"

func TestIsPublicHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		host string
		want bool
	}{
		{host: "example.com", want: true},
		{host: "Example.COM.", want: true},
		{host: "8.8.8.8", want: true},
		{host: "localhost"},
		{host: "foo.localhost"},
		{host: "app.local"},
		{host: "office"},
		{host: "memoh.internal"},
		{host: "foo.test"},
		{host: "foo.invalid"},
		{host: "foo.example"},
		{host: "router.home.arpa"},
		{host: "127.0.0.1"},
		{host: "001.002.003.004"},
		{host: "0177.0.0.1"},
		{host: "10.0.0.1"},
		{host: "010.0.0.1"},
		{host: "172.16.0.1"},
		{host: "192.168.1.1"},
		{host: "169.254.169.254"},
		{host: "100.64.0.1"},
		{host: "192.0.2.1"},
		{host: "198.51.100.1"},
		{host: "203.0.113.1"},
		{host: "224.0.0.1"},
		{host: "240.0.0.1"},
		{host: "255.255.255.255"},
		{host: "::1"},
		{host: "2606:4700:4700::1111"},
		{host: "::ffff:8.8.8.8"},
		{host: "foo..example.com"},
		{host: "999.1.1.1"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.host, func(t *testing.T) {
			t.Parallel()
			if got := IsPublicHost(tt.host); got != tt.want {
				t.Fatalf("IsPublicHost(%q) = %v, want %v", tt.host, got, tt.want)
			}
		})
	}
}
