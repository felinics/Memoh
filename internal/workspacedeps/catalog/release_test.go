package catalog

import "testing"

func TestUsingRejectsEmptyDefinition(t *testing.T) {
	if _, err := Empty().Using(Definition{}); err == nil {
		t.Fatal("Using accepted an empty definition")
	}
}
