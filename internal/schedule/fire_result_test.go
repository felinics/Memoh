package schedule

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/felinics/memoh/internal/job"
)

// A fire that finds the session busy is dropped by design. Its result record
// says so at INFO instead of reporting a failure on every collision; any
// other failure is still the unit's outcome.
func TestFireResultSkipsABusySession(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantErr     bool
		wantLevel   string
		wantSkipped string
	}{
		{name: "busy", err: fmt.Errorf("%w: ledger busy", ErrSessionBusy), wantLevel: "INFO", wantSkipped: "busy"},
		{name: "failure", err: errors.New("resolve bot owner"), wantErr: true, wantLevel: "ERROR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := slog.New(slog.NewJSONHandler(&buf, nil))

			err := job.Run(context.Background(), log, "schedule.fire", job.Options{OwnRequestID: true}, func(ctx context.Context) error {
				return fireResult(ctx, tt.err)
			})

			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			var record map[string]any
			if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
				t.Fatalf("decode %q: %v", buf.String(), err)
			}
			if record["level"] != tt.wantLevel || record["msg"] != "job" {
				t.Fatalf("record = %v, want one %s job record", record, tt.wantLevel)
			}
			if got, _ := record["skipped"].(string); got != tt.wantSkipped {
				t.Fatalf("skipped = %q, want %q", got, tt.wantSkipped)
			}
		})
	}
}
