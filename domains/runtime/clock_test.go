package runtime

import "testing"

func TestResolveClockDefaultTimezone(t *testing.T) {
	t.Setenv("TZ", "")

	clock, err := ResolveClock("UTC")
	if err != nil {
		t.Fatalf("ResolveClock returned error: %v", err)
	}
	if clock.Name != "UTC" {
		t.Fatalf("Name = %q, want UTC", clock.Name)
	}
	if clock.Location == nil {
		t.Fatal("Location is nil")
	}
	if clock.Location.String() != "UTC" {
		t.Fatalf("Location = %q, want UTC", clock.Location.String())
	}
}

func TestResolveClockPrefersTZEnv(t *testing.T) {
	t.Setenv("TZ", "Asia/Tokyo")

	clock, err := ResolveClock("UTC")
	if err != nil {
		t.Fatalf("ResolveClock returned error: %v", err)
	}
	if clock.Name != "Asia/Tokyo" {
		t.Fatalf("Name = %q, want Asia/Tokyo", clock.Name)
	}
	if clock.Location == nil || clock.Location.String() != "Asia/Tokyo" {
		t.Fatalf("Location = %v, want Asia/Tokyo", clock.Location)
	}
}
