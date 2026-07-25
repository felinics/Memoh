package mcp

import (
	"context"
	"testing"
)

type registryTestProvider struct{}

func (*registryTestProvider) ListTools(_ context.Context, _ ToolSessionContext) ([]ToolDescriptor, error) {
	return nil, nil
}

func (*registryTestProvider) CallTool(_ context.Context, _ ToolSessionContext, _ string, _ map[string]any) (map[string]any, error) {
	return nil, nil
}

func TestToolRegistryRegisterAndLookup(t *testing.T) {
	registry := NewToolRegistry()
	provider := &registryTestProvider{}
	if err := registry.Register(provider, ToolDescriptor{
		Name:        "tool_a",
		Description: "test",
		InputSchema: map[string]any{"type": "object"},
	}); err != nil {
		t.Fatalf("register should succeed: %v", err)
	}

	gotProvider, descriptor, ok := registry.Lookup("tool_a")
	if !ok {
		t.Fatalf("lookup should find registered tool")
	}
	if gotProvider != provider {
		t.Fatalf("lookup provider mismatch")
	}
	if descriptor.Name != "tool_a" {
		t.Fatalf("lookup descriptor mismatch, got: %s", descriptor.Name)
	}
}

func TestToolRegistryRegisterDuplicate(t *testing.T) {
	registry := NewToolRegistry()
	provider := &registryTestProvider{}
	first := ToolDescriptor{
		Name:        "dup_tool",
		Description: "first",
		InputSchema: map[string]any{"type": "object"},
	}
	second := ToolDescriptor{
		Name:        "dup_tool",
		Description: "second",
		InputSchema: map[string]any{"type": "object"},
	}
	if err := registry.Register(provider, first); err != nil {
		t.Fatalf("first register should succeed: %v", err)
	}
	if err := registry.Register(provider, second); err == nil {
		t.Fatalf("duplicate register should fail")
	}
}

func TestToolRegistryListStableOrder(t *testing.T) {
	registry := NewToolRegistry()
	provider := &registryTestProvider{}
	tools := []ToolDescriptor{
		{Name: "b_tool", InputSchema: map[string]any{"type": "object"}},
		{Name: "a_tool", InputSchema: map[string]any{"type": "object"}},
		{Name: "c_tool", InputSchema: map[string]any{"type": "object"}},
	}
	for _, tool := range tools {
		if err := registry.Register(provider, tool); err != nil {
			t.Fatalf("register %s failed: %v", tool.Name, err)
		}
	}

	list := registry.List()
	if len(list) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(list))
	}
	if list[0].Name != "a_tool" || list[1].Name != "b_tool" || list[2].Name != "c_tool" {
		t.Fatalf("unexpected order: %#v", list)
	}
}
