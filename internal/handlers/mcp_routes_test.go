package handlers

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

var swaggerRouterPattern = regexp.MustCompile(`@Router\s+(\S+)\s+\[(\w+)\]`)

func TestMCPHandlerRoutesMatchSwaggerAnnotations(t *testing.T) {
	e := echo.New()
	(&MCPHandler{}).Register(e)

	registered := make(map[string]bool)
	for _, route := range e.Routes() {
		registered[strings.ToUpper(route.Method)+" "+route.Path] = true
	}

	source, err := os.ReadFile("mcp.go")
	if err != nil {
		t.Fatal(err)
	}
	matches := swaggerRouterPattern.FindAllStringSubmatch(string(source), -1)
	if len(matches) == 0 {
		t.Fatal("no @Router annotations found in mcp.go")
	}
	for _, m := range matches {
		path := strings.NewReplacer("{", ":", "}", "").Replace(m[1])
		key := strings.ToUpper(m[2]) + " " + path
		if !registered[key] {
			t.Errorf("swagger annotation %q documents unregistered route %s", strings.TrimSpace(m[0]), key)
		}
	}
}
