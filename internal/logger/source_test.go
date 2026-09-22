package logger_test

import (
	"bytes"
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/logger"
)

func TestNewIncludesSourceLocation(t *testing.T) {
	var output bytes.Buffer
	log := logger.New(&output, "info", "json")
	_, file, line, _ := runtime.Caller(0)
	log.Info("source-check")

	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatalf("decode log record: %v", err)
	}
	source, ok := record["source"].(map[string]any)
	if !ok {
		t.Fatalf("source field = %#v, want object", record["source"])
	}
	if source["file"] != file {
		t.Fatalf("source.file = %q", source["file"])
	}
	if source["line"].(float64) != float64(line+1) || !strings.HasSuffix(source["function"].(string), ".TestNewIncludesSourceLocation") {
		t.Fatalf("source = %#v", source)
	}
}
