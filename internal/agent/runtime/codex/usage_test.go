package codex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/apperror"
)

func TestFetchAccountUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/wham/usage" || r.Header.Get("Authorization") != "Bearer token" || r.Header.Get("ChatGPT-Account-Id") != "account" {
			t.Errorf("request = %s %v", r.URL.Path, r.Header)
		}
		_, _ = w.Write([]byte(`{"plan_type":"plus","rate_limit":{"allowed":true,"limit_reached":false,
			"primary_window":{"used_percent":52,"limit_window_seconds":18000,"reset_after_seconds":100,"reset_at":1790000000},
			"secondary_window":{"used_percent":12.4,"limit_window_seconds":604800,"reset_after_seconds":200,"reset_at":0}}}`))
	}))
	defer server.Close()

	usage, err := fetchAccountUsage(context.Background(), server.Client(), server.URL+"/backend-api/", "token", "account")
	if err != nil {
		t.Fatal(err)
	}
	if usage.LimitReached || len(usage.Windows) != 2 {
		t.Fatalf("usage = %+v", usage)
	}
	primary, secondary := usage.Windows[0], usage.Windows[1]
	if primary.UsedPercent != 52 || primary.WindowMinutes != 300 || primary.ResetsAt == nil || !primary.ResetsAt.Equal(time.Unix(1790000000, 0)) {
		t.Fatalf("primary = %+v", primary)
	}
	if secondary.UsedPercent != 12 || secondary.WindowMinutes != 10080 || secondary.ResetsAt != nil {
		t.Fatalf("secondary = %+v", secondary)
	}
}

func TestFetchAccountUsageReportsExpiredSignIn(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := fetchAccountUsage(context.Background(), server.Client(), server.URL, "stale", "account")
	if apperror.CodeOf(err) != apperror.CodeAgentCredentialUsageAuthExpired {
		t.Fatalf("err = %v, want usage_auth_expired", err)
	}
}
