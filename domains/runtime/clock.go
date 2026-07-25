package runtime

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Clock is the process-default timezone used by schedule and agent context.
type Clock struct {
	Name     string
	Location *time.Location
}

// ResolveClock resolves the operator timezone, preferring the process TZ env.
func ResolveClock(configured string) (Clock, error) {
	tzName := strings.TrimSpace(configured)
	if envTZ := strings.TrimSpace(os.Getenv("TZ")); envTZ != "" {
		tzName = envTZ
	}
	location, resolved, err := resolveTimezone(tzName)
	if err != nil {
		return Clock{}, err
	}
	return Clock{Name: resolved, Location: location}, nil
}

func resolveTimezone(name string) (*time.Location, string, error) {
	normalized := strings.TrimSpace(name)
	if normalized == "" {
		return time.UTC, "UTC", nil
	}
	if strings.EqualFold(normalized, "local") {
		return time.Local, "local", nil
	}
	loc, err := time.LoadLocation(normalized)
	if err != nil {
		return nil, "", fmt.Errorf("load timezone %q: %w", normalized, err)
	}
	return loc, normalized, nil
}
