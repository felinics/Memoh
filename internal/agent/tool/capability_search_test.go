package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/supermarket"
)

func catalogToolFixture(t *testing.T, handler http.HandlerFunc) (sdk.Tool, *capabilityTestApproval) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	provider, _, review, session := capabilityFixture(t)
	provider.opts.Apps = &capabilityTestApps{}
	provider.opts.Registry = &capabilityTestRegistry{}
	provider.opts.Catalog = supermarket.NewClient(server.URL, server.Client())
	provider.opts.Access = func(_ context.Context, _, _ string, manage bool) error {
		if manage {
			return errors.New("chat-only user")
		}
		return nil
	}
	available, err := provider.Tools(t.Context(), session)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range available {
		if tool.Name == ToolAppSearch().String() {
			return tool, review
		}
	}
	t.Fatal("App search not registered")
	return sdk.Tool{}, nil
}

func TestAppCategoriesFilterCountsBeforePagination(t *testing.T) {
	tool, review := catalogToolFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/categories" {
			t.Errorf("unexpected catalog path: %s", r.URL.Path)
		}
		// The upstream list may be unordered and contains categories that have
		// no Apps in the selected registry. Page boundaries follow filtering.
		response := supermarket.AppCategoryListResponse{Data: []supermarket.AppCategory{
			{ID: "creativity", Order: 100, AppCount: 1, Registries: []supermarket.AppCategoryRegistry{{ID: "openai", Count: 1}}},
			{ID: "empty", Order: 0},
			{ID: "developer-tools", Name: "Developer Tools", Names: map[string]string{"zh": "开发工具"}, Order: 40, AppCount: 5, Registries: []supermarket.AppCategoryRegistry{{ID: "memoh", Count: 2}, {ID: "openai", Count: 3}}},
			{ID: "agent", Order: 10, AppCount: 2, Registries: []supermarket.AppCategoryRegistry{{ID: "memoh", Count: 2}}},
		}}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Error(err)
		}
	})
	for _, tc := range []struct {
		name, registry     string
		page, limit, total int
		ids                []string
	}{
		{name: "all", page: 1, limit: 2, total: 3, ids: []string{"agent", "developer-tools"}},
		{name: "registry-first", registry: "openai", page: 1, limit: 1, total: 2, ids: []string{"developer-tools"}},
		{name: "registry-next", registry: "openai", page: 2, limit: 1, total: 2, ids: []string{"creativity"}},
		{name: "past-end", registry: "openai", page: 3, limit: 1, total: 2, ids: []string{}},
		{name: "unknown-registry", registry: "missing", page: 1, limit: 20, total: 0, ids: []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{"action": "categories", "page": tc.page, "limit": tc.limit}
			if tc.registry != "" {
				args["registry"] = tc.registry
			}
			result, err := tool.Execute(&sdk.ToolExecContext{Context: t.Context()}, args)
			if err != nil {
				t.Fatal(err)
			}
			data := result.(map[string]any)
			assertCapabilityMessage(t, data)
			items := data["items"].([]supermarket.AppCategory)
			ids := make([]string, 0, len(items))
			for _, item := range items {
				ids = append(ids, item.ID)
				if item.ID == "developer-tools" {
					if item.Names["zh"] != "开发工具" {
						t.Fatal("localized category name lost")
					}
					if tc.registry == "openai" && (item.AppCount != 3 || len(item.Registries) != 1) {
						t.Fatalf("registry count mismatch: %+v", item)
					}
				}
			}
			if data["total"] != tc.total || !reflect.DeepEqual(ids, tc.ids) {
				t.Fatalf("category page = %#v", data)
			}
		})
	}
	if review.reviews != 0 {
		t.Fatal("catalog browsing required management approval")
	}
}

func TestAppSearchBrowsesCategoryWithoutKeywords(t *testing.T) {
	tool, review := catalogToolFixture(t, func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if r.URL.Path != "/api/apps" || query.Get("category") != "developer-tools" || query.Get("registry") != "openai" || query.Get("page") != "2" || query.Get("limit") != "2" || query.Has("q") {
			t.Errorf("unexpected browse request: %s", r.URL)
		}
		items := []supermarket.AppSummary{}
		for _, id := range []string{"app-c", "app-d", "excess-result"} {
			items = append(items, supermarket.AppSummary{RegistryID: "openai", AppID: id, AppMetadata: supermarket.AppMetadata{Category: "developer-tools", CategoryName: "Developer Tools"}})
		}
		if err := json.NewEncoder(w).Encode(supermarket.AppListResponse{Data: items, Total: 5}); err != nil {
			t.Error(err)
		}
	})
	result, err := tool.Execute(&sdk.ToolExecContext{Context: t.Context()}, map[string]any{"action": "search", "category": "developer-tools", "registry": "openai", "page": 2, "limit": 2})
	if err != nil {
		t.Fatal(err)
	}
	data := result.(map[string]any)
	assertCapabilityMessage(t, data)
	items := data["items"].([]map[string]any)
	if len(items) != 2 || items[0]["app_id"] != "app-c" || items[1]["category"] != "developer-tools" || data["total"] != 5 {
		t.Fatalf("browse result = %#v", data)
	}
	if review.reviews != 0 {
		t.Fatal("category browsing required management approval")
	}
}

func TestAppCategoriesFailureDoesNotBecomeEmptyCatalog(t *testing.T) {
	tool, _ := catalogToolFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "PRIVATE upstream diagnostic", http.StatusBadGateway)
	})
	result, err := tool.Execute(&sdk.ToolExecContext{Context: t.Context()}, map[string]any{"action": "categories"})
	if err != nil {
		t.Fatal(err)
	}
	assertCapabilityCode(t, result, apperror.CodeCapabilityOperationFailed)
	assertCapabilityMessage(t, result)
	if _, ok := result.(map[string]any)["items"]; ok {
		t.Fatal("upstream failure reported as empty catalog")
	}
}
