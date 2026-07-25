package epoch_test

import (
	"io/fs"
	"testing"

	dbembed "github.com/memohai/memoh/db"
	"github.com/memohai/memoh/internal/db/epoch"
)

func TestEmbeddedPostgresAssets(t *testing.T) {
	postgres, err := fs.Sub(dbembed.MigrationsFS, "postgres")
	if err != nil {
		t.Fatalf("fs.Sub(postgres) error = %v", err)
	}
	if _, err := epoch.Load(postgres); err != nil {
		t.Fatalf("epoch.Load() error = %v", err)
	}
}
