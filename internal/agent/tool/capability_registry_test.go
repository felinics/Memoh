package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/agent/toolexec"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/apps"
	"github.com/felinics/memoh/internal/supermarket"
)

// unavailableCapabilityRegistry fails as the Installer does when the registry
// cannot be reached.
type unavailableCapabilityRegistry struct{ apps.RegistryClient }

func (unavailableCapabilityRegistry) FetchCurrentApp(context.Context, string, string) (supermarket.AppDescriptor, error) {
	return supermarket.AppDescriptor{}, fmt.Errorf("fetch Registry App: %w: %w", supermarket.ErrRegistryUnavailable, errors.New("PRIVATE upstream diagnostic"))
}

func TestCapabilityReportsARegistryFailureByItsCode(t *testing.T) {
	p, _, _, session := capabilityFixture(t)
	p.opts.Apps = &capabilityTestApps{}
	p.opts.Registry = unavailableCapabilityRegistry{}
	p.opts.Catalog = &supermarket.Client{}
	available, err := p.Tools(t.Context(), session)
	if err != nil {
		t.Fatal(err)
	}
	var result any
	for _, tool := range available {
		if tool.Name == ToolAppSearch().String() {
			output, err := tool.Execute(&toolexec.ToolExecContext{Context: t.Context()}, toolexec.ArgumentsFromValue(map[string]any{"action": "get", "registry_id": "memoh", "app_id": "node"}))
			if err != nil {
				t.Fatal(err)
			}
			result = toolexec.OutputValue(output)
		}
	}
	definition, _ := apperror.Lookup(apperror.CodeRegistryUnavailable)
	data, _ := result.(map[string]any)
	if data["code"] != string(apperror.CodeRegistryUnavailable) || data["detail"] != definition.Detail || data["message"] != definition.Detail {
		t.Fatalf("result = %#v, want %s with detail %q", result, apperror.CodeRegistryUnavailable, definition.Detail)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "PRIVATE") {
		t.Fatalf("result repeats the cause: %s", raw)
	}
}
