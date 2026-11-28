package handlers

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	echoImportPath     = "github.com/labstack/echo/v4"
	apperrorImportPath = "github.com/felinics/memoh/internal/apperror"
	httpImportPath     = "net/http"
	dynamicStatusKey   = "<dynamic>"
)

// httpErrorGuardDirs are the handler/app directories policed by Guard 1.
// Non-handler middleware and external IM webhook adapters live outside these
// directories by design.
var httpErrorGuardDirs = []string{
	filepath.Join("internal", "handlers"),
	filepath.Join("internal", "apps"),
}

// allowedNewHTTPError is the exact, monotonically shrinking allowlist for
// echo.NewHTTPError calls by repo-relative file path and status key.
// Both increases and decreases fail CI so migrations must ratchet this table down.
var allowedNewHTTPError = map[string]map[string]int{
	"internal/handlers/apps.go":                   {"http.StatusServiceUnavailable": 1},
	"internal/handlers/attention.go":              {"http.StatusInternalServerError": 3},
	"internal/handlers/auth.go":                   {"http.StatusInternalServerError": 4, "http.StatusUnauthorized": 1},
	"internal/handlers/bot_agents.go":             {"http.StatusInternalServerError": 1},
	"internal/handlers/bot_backup.go":             {"http.StatusInternalServerError": 4, "http.StatusUnauthorized": 4},
	"internal/handlers/containerd.go":             {"http.StatusInternalServerError": 4},
	"internal/handlers/containerd_browser.go":     {"http.StatusBadGateway": 2, "http.StatusInternalServerError": 1},
	"internal/handlers/containerd_terminal.go":    {"http.StatusInternalServerError": 1},
	"internal/handlers/display.go":                {"http.StatusInternalServerError": 2},
	"internal/handlers/filemanager.go":            {"http.StatusBadRequest": 1, "http.StatusForbidden": 1},
	"internal/handlers/handler_helpers.go":        {"http.StatusForbidden": 1, "http.StatusInternalServerError": 1},
	"internal/handlers/hooks.go":                  {"http.StatusInternalServerError": 2},
	"internal/handlers/local_channel.go":          {"http.StatusForbidden": 2, "http.StatusInternalServerError": 7, "http.StatusRequestEntityTooLarge": 1},
	"internal/handlers/mcp.go":                    {"http.StatusInternalServerError": 1},
	"internal/handlers/mcp_tools.go":              {"http.StatusServiceUnavailable": 1},
	"internal/handlers/memory.go":                 {"http.StatusBadRequest": 2, "http.StatusForbidden": 1, "http.StatusInternalServerError": 1},
	"internal/handlers/message.go":                {"http.StatusForbidden": 1, "http.StatusInternalServerError": 7},
	"internal/handlers/message_stream.go":         {"http.StatusInternalServerError": 3},
	"internal/handlers/providers.go":              {"http.StatusBadRequest": 1, "http.StatusInternalServerError": 1},
	"internal/handlers/public_media.go":           {"http.StatusBadRequest": 2, "http.StatusForbidden": 2, "http.StatusInternalServerError": 1, "http.StatusRequestEntityTooLarge": 4, "http.StatusUnsupportedMediaType": 2},
	"internal/handlers/runtime_connect.go":        {"http.StatusBadRequest": 1, "http.StatusServiceUnavailable": 1, "http.StatusUnauthorized": 2, "http.StatusUpgradeRequired": 1},
	"internal/handlers/session.go":                {"http.StatusForbidden": 3, "http.StatusInternalServerError": 5, "http.StatusServiceUnavailable": 2},
	"internal/handlers/session_info.go":           {"http.StatusInternalServerError": 1},
	"internal/handlers/session_queue.go":          {"http.StatusForbidden": 1, "http.StatusInternalServerError": 1},
	"internal/handlers/supermarket_skills.go":     {"http.StatusBadGateway": 3},
	"internal/handlers/users.go":                  {"http.StatusBadRequest": 3, "http.StatusForbidden": 8, "http.StatusInternalServerError": 9, "http.StatusUnauthorized": 3},
	"internal/handlers/workdir.go":                {"http.StatusForbidden": 1},
	"internal/handlers/workspace_dependencies.go": {"http.StatusTooManyRequests": 1},
}

// allowedCodeHTTP is the exact allowlist for direct references to
// apperror.CodeHTTP* constants. CodeHTTPConflict is permanently locked at 0.
var allowedCodeHTTP = map[string]map[string]int{
	"internal/handlers/display.go":        {"CodeHTTPServiceUnavailable": 1},
	"internal/handlers/local_channel.go":  {"CodeHTTPBadRequest": 2},
	"internal/handlers/memory.go":         {"CodeHTTPServiceUnavailable": 2},
	"internal/handlers/provider_oauth.go": {"CodeHTTPBadRequest": 1},
	"internal/handlers/schedule.go":       {"CodeHTTPForbidden": 3},
}

// allowedEchoErr is the exact allowlist for references to Echo's predefined
// *echo.HTTPError singletons (echo.Err*). ErrConflict is permanently locked at 0.
var allowedEchoErr = map[string]map[string]int{
	"internal/handlers/file_embed.go": {"ErrNotFound": 4},
}

// allowedDirectErrorWrites is the exact allowlist for direct Echo response
// writes carrying a 4xx/5xx or dynamic status code. Explicit >= 400 writes are
// permanently 0 in OSS; only two existing structured/HTML helpers pass a status
// parameter.
var allowedDirectErrorWrites = map[string]map[string]int{
	"internal/handlers/local_channel.go": {"JSON:" + dynamicStatusKey: 1},
	"internal/handlers/mcp_oauth.go":     {"HTML:" + dynamicStatusKey: 1},
}

// echoResponseWriteMethods are Echo context methods whose first argument is an
// HTTP status code.
var echoResponseWriteMethods = map[string]bool{
	"JSON":       true,
	"JSONPretty": true,
	"JSONBlob":   true,
	"JSONP":      true,
	"JSONPBlob":  true,
	"XML":        true,
	"XMLPretty":  true,
	"XMLBlob":    true,
	"String":     true,
	"HTML":       true,
	"HTMLBlob":   true,
	"Blob":       true,
	"Stream":     true,
	"NoContent":  true,
}

// httpStatusCodeByConst maps standard net/http status constants to their
// numeric values so AST checks distinguish 2xx/3xx from 4xx/5xx accurately.
var httpStatusCodeByConst = map[string]int{
	"StatusContinue":                      http.StatusContinue,
	"StatusSwitchingProtocols":            http.StatusSwitchingProtocols,
	"StatusProcessing":                    http.StatusProcessing,
	"StatusEarlyHints":                    http.StatusEarlyHints,
	"StatusOK":                            http.StatusOK,
	"StatusCreated":                       http.StatusCreated,
	"StatusAccepted":                      http.StatusAccepted,
	"StatusNonAuthoritativeInfo":          http.StatusNonAuthoritativeInfo,
	"StatusNoContent":                     http.StatusNoContent,
	"StatusResetContent":                  http.StatusResetContent,
	"StatusPartialContent":                http.StatusPartialContent,
	"StatusMultiStatus":                   http.StatusMultiStatus,
	"StatusAlreadyReported":               http.StatusAlreadyReported,
	"StatusIMUsed":                        http.StatusIMUsed,
	"StatusMultipleChoices":               http.StatusMultipleChoices,
	"StatusMovedPermanently":              http.StatusMovedPermanently,
	"StatusFound":                         http.StatusFound,
	"StatusSeeOther":                      http.StatusSeeOther,
	"StatusNotModified":                   http.StatusNotModified,
	"StatusUseProxy":                      http.StatusUseProxy,
	"StatusTemporaryRedirect":             http.StatusTemporaryRedirect,
	"StatusPermanentRedirect":             http.StatusPermanentRedirect,
	"StatusBadRequest":                    http.StatusBadRequest,
	"StatusUnauthorized":                  http.StatusUnauthorized,
	"StatusPaymentRequired":               http.StatusPaymentRequired,
	"StatusForbidden":                     http.StatusForbidden,
	"StatusNotFound":                      http.StatusNotFound,
	"StatusMethodNotAllowed":              http.StatusMethodNotAllowed,
	"StatusNotAcceptable":                 http.StatusNotAcceptable,
	"StatusProxyAuthRequired":             http.StatusProxyAuthRequired,
	"StatusRequestTimeout":                http.StatusRequestTimeout,
	"StatusConflict":                      http.StatusConflict,
	"StatusGone":                          http.StatusGone,
	"StatusLengthRequired":                http.StatusLengthRequired,
	"StatusPreconditionFailed":            http.StatusPreconditionFailed,
	"StatusRequestEntityTooLarge":         http.StatusRequestEntityTooLarge,
	"StatusRequestURITooLong":             http.StatusRequestURITooLong,
	"StatusUnsupportedMediaType":          http.StatusUnsupportedMediaType,
	"StatusRequestedRangeNotSatisfiable":  http.StatusRequestedRangeNotSatisfiable,
	"StatusExpectationFailed":             http.StatusExpectationFailed,
	"StatusTeapot":                        http.StatusTeapot,
	"StatusMisdirectedRequest":            http.StatusMisdirectedRequest,
	"StatusUnprocessableEntity":           http.StatusUnprocessableEntity,
	"StatusLocked":                        http.StatusLocked,
	"StatusFailedDependency":              http.StatusFailedDependency,
	"StatusTooEarly":                      http.StatusTooEarly,
	"StatusUpgradeRequired":               http.StatusUpgradeRequired,
	"StatusPreconditionRequired":          http.StatusPreconditionRequired,
	"StatusTooManyRequests":               http.StatusTooManyRequests,
	"StatusRequestHeaderFieldsTooLarge":   http.StatusRequestHeaderFieldsTooLarge,
	"StatusUnavailableForLegalReasons":    http.StatusUnavailableForLegalReasons,
	"StatusInternalServerError":           http.StatusInternalServerError,
	"StatusNotImplemented":                http.StatusNotImplemented,
	"StatusBadGateway":                    http.StatusBadGateway,
	"StatusServiceUnavailable":            http.StatusServiceUnavailable,
	"StatusGatewayTimeout":                http.StatusGatewayTimeout,
	"StatusHTTPVersionNotSupported":       http.StatusHTTPVersionNotSupported,
	"StatusVariantAlsoNegotiates":         http.StatusVariantAlsoNegotiates,
	"StatusInsufficientStorage":           http.StatusInsufficientStorage,
	"StatusLoopDetected":                  http.StatusLoopDetected,
	"StatusNotExtended":                   http.StatusNotExtended,
	"StatusNetworkAuthenticationRequired": http.StatusNetworkAuthenticationRequired,
}

type httpErrorScanReport struct {
	newHTTPError     map[string]map[string]int
	echoErr          map[string]map[string]int
	httpErrorStructs []string
	directErrorWrite map[string]map[string]int
	codeHTTP         map[string]map[string]int
}

func newHTTPErrorScanReport() *httpErrorScanReport {
	return &httpErrorScanReport{
		newHTTPError:     make(map[string]map[string]int),
		echoErr:          make(map[string]map[string]int),
		directErrorWrite: make(map[string]map[string]int),
		codeHTTP:         make(map[string]map[string]int),
	}
}

func incNestedCount(m map[string]map[string]int, file, key string) {
	if m[file] == nil {
		m[file] = make(map[string]int)
	}
	m[file][key]++
}

func scanFileForHTTPErrors(fset *token.FileSet, rel string, file *ast.File, report *httpErrorScanReport) {
	echoAliases := map[string]bool{}
	apperrorAliases := map[string]bool{}
	httpAliases := map[string]bool{}
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		alias := filepath.Base(path)
		if imp.Name != nil {
			alias = imp.Name.Name
		} else if path == echoImportPath {
			alias = "echo"
		}
		if alias == "_" || alias == "." {
			continue
		}
		switch path {
		case echoImportPath:
			echoAliases[alias] = true
		case apperrorImportPath:
			apperrorAliases[alias] = true
		case httpImportPath:
			httpAliases[alias] = true
		}
	}

	var inspectNode func(node ast.Node, echoCtxVars map[string]bool)
	inspectNode = func(node ast.Node, echoCtxVars map[string]bool) {
		ast.Inspect(node, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.FuncLit:
				childVars := mergeEchoContextVars(echoCtxVars, v.Type, echoAliases)
				if v.Body != nil {
					inspectNode(v.Body, childVars)
				}
				return false
			case *ast.CallExpr:
				// new(echo.HTTPError)
				if id, ok := v.Fun.(*ast.Ident); ok && id.Name == "new" && len(v.Args) == 1 {
					if isEchoHTTPErrorType(v.Args[0], echoAliases) {
						report.httpErrorStructs = append(report.httpErrorStructs, fmt.Sprintf("%s:%d: new(echo.HTTPError)", rel, fset.Position(v.Pos()).Line))
					}
					return true
				}
				sel, ok := v.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				recv, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				// echo.NewHTTPError(...)
				if echoAliases[recv.Name] && sel.Sel.Name == "NewHTTPError" {
					statusKey := dynamicStatusKey
					if len(v.Args) > 0 {
						statusKey, _ = classifyStatusExpr(v.Args[0], httpAliases)
					}
					incNestedCount(report.newHTTPError, rel, statusKey)
					return true
				}
				// c.JSON / c.String / c.NoContent / c.Blob / c.HTML / ... with >= 400 or dynamic status
				if echoCtxVars[recv.Name] && echoResponseWriteMethods[sel.Sel.Name] && len(v.Args) > 0 {
					statusKey, code := classifyStatusExpr(v.Args[0], httpAliases)
					if statusKey == dynamicStatusKey || code >= http.StatusBadRequest {
						incNestedCount(report.directErrorWrite, rel, sel.Sel.Name+":"+statusKey)
					}
					return true
				}
			case *ast.CompositeLit:
				// echo.HTTPError{...} or &echo.HTTPError{...}
				if isEchoHTTPErrorType(v.Type, echoAliases) {
					report.httpErrorStructs = append(report.httpErrorStructs, fmt.Sprintf("%s:%d: echo.HTTPError composite literal", rel, fset.Position(v.Pos()).Line))
				}
			case *ast.SelectorExpr:
				pkg, ok := v.X.(*ast.Ident)
				if !ok {
					return true
				}
				// echo.Err* predefined *echo.HTTPError singletons
				if echoAliases[pkg.Name] && strings.HasPrefix(v.Sel.Name, "Err") {
					incNestedCount(report.echoErr, rel, v.Sel.Name)
				}
				// apperror.CodeHTTP*
				if apperrorAliases[pkg.Name] && strings.HasPrefix(v.Sel.Name, "CodeHTTP") {
					incNestedCount(report.codeHTTP, rel, v.Sel.Name)
				}
			}
			return true
		})
	}

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			inspectNode(decl, nil)
			continue
		}
		echoCtxVars := mergeEchoContextVars(nil, fn.Type, echoAliases)
		inspectNode(fn, echoCtxVars)
	}
}

func mergeEchoContextVars(parent map[string]bool, fnType *ast.FuncType, echoAliases map[string]bool) map[string]bool {
	vars := make(map[string]bool, len(parent))
	for k, v := range parent {
		vars[k] = v
	}
	if fnType != nil && fnType.Params != nil {
		for _, field := range fnType.Params.List {
			isCtx := isEchoContextType(field.Type, echoAliases)
			for _, name := range field.Names {
				if isCtx {
					vars[name.Name] = true
				} else {
					delete(vars, name.Name)
				}
			}
		}
	}
	return vars
}

func isEchoContextType(expr ast.Expr, echoAliases map[string]bool) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && echoAliases[pkg.Name] && sel.Sel.Name == "Context"
}

func isEchoHTTPErrorType(expr ast.Expr, echoAliases map[string]bool) bool {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && echoAliases[pkg.Name] && sel.Sel.Name == "HTTPError"
}

// classifyStatusExpr returns the canonical status key and numeric HTTP code
// (or dynamicStatusKey, -1 if the expression is not an http.Status* constant or
// integer literal).
func classifyStatusExpr(expr ast.Expr, httpAliases map[string]bool) (string, int) {
	switch v := expr.(type) {
	case *ast.SelectorExpr:
		if pkg, ok := v.X.(*ast.Ident); ok && httpAliases[pkg.Name] {
			if code, exists := httpStatusCodeByConst[v.Sel.Name]; exists {
				return "http." + v.Sel.Name, code
			}
		}
	case *ast.BasicLit:
		if v.Kind == token.INT {
			if code, err := strconv.Atoi(v.Value); err == nil {
				return v.Value, code
			}
		}
	}
	return dynamicStatusKey, -1
}

func compareNestedAllowlist(t *testing.T, label string, got, want map[string]map[string]int) {
	t.Helper()
	files := make(map[string]struct{})
	for f := range got {
		files[f] = struct{}{}
	}
	for f := range want {
		files[f] = struct{}{}
	}
	sortedFiles := make([]string, 0, len(files))
	for f := range files {
		sortedFiles = append(sortedFiles, f)
	}
	sort.Strings(sortedFiles)

	for _, file := range sortedFiles {
		keys := make(map[string]struct{})
		for k := range got[file] {
			keys[k] = struct{}{}
		}
		for k := range want[file] {
			keys[k] = struct{}{}
		}
		sortedKeys := make([]string, 0, len(keys))
		for k := range keys {
			sortedKeys = append(sortedKeys, k)
		}
		sort.Strings(sortedKeys)

		for _, key := range sortedKeys {
			g := got[file][key]
			w := want[file][key]
			if g != w {
				t.Errorf("%s mismatch in %s [%s]: got %d, allowlist expects %d", label, file, key, g, w)
			}
		}
	}
}

// TestHTTPErrorExitGuard enforces Guard 1 across internal/handlers and
// internal/apps: every bare echo.NewHTTPError, echo.HTTPError struct literal,
// direct >= 400 Echo response write, and direct apperror.CodeHTTP* reference is
// pinned to an exact shrinking allowlist.
func TestHTTPErrorExitGuard(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	report := newHTTPErrorScanReport()

	for _, dir := range httpErrorGuardDirs {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			file, parseErr := parser.ParseFile(fset, path, nil, 0)
			if parseErr != nil {
				return parseErr
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			scanFileForHTTPErrors(fset, filepath.ToSlash(rel), file, report)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}

	// 1. echo.NewHTTPError exact shrinking allowlist, with 409 and dynamic status permanently locked at 0
	compareNestedAllowlist(t, "echo.NewHTTPError", report.newHTTPError, allowedNewHTTPError)
	for file, counts := range report.newHTTPError {
		if n := counts["http.StatusConflict"] + counts["409"]; n > 0 {
			t.Errorf("bare echo.NewHTTPError(409) is permanently forbidden in %s: found %d", file, n)
		}
		if n := counts[dynamicStatusKey]; n > 0 {
			t.Errorf("dynamic echo.NewHTTPError(status) is permanently forbidden in %s: found %d", file, n)
		}
	}

	// 2. Predefined echo.Err* singletons exact shrinking allowlist, ErrConflict permanently 0
	compareNestedAllowlist(t, "echo.Err*", report.echoErr, allowedEchoErr)
	for file, counts := range report.echoErr {
		if n := counts["ErrConflict"]; n > 0 {
			t.Errorf("echo.ErrConflict is permanently forbidden in %s: found %d", file, n)
		}
	}

	// 3. echo.HTTPError struct literals and new(echo.HTTPError) permanently 0
	for _, violation := range report.httpErrorStructs {
		t.Errorf("forbidden echo.HTTPError literal/new: %s", violation)
	}

	// 4. Direct >= 400 / dynamic Echo response writes
	compareNestedAllowlist(t, "direct Echo error write", report.directErrorWrite, allowedDirectErrorWrites)
	for file, counts := range report.directErrorWrite {
		for key, count := range counts {
			if !strings.HasSuffix(key, ":"+dynamicStatusKey) && count > 0 {
				t.Errorf("forbidden explicit >=400 direct Echo write in %s [%s]: %d", file, key, count)
			}
		}
	}

	// 5. Direct apperror.CodeHTTP* references, with CodeHTTPConflict permanently 0
	compareNestedAllowlist(t, "apperror.CodeHTTP*", report.codeHTTP, allowedCodeHTTP)
	for file, counts := range report.codeHTTP {
		if n := counts["CodeHTTPConflict"]; n > 0 {
			t.Errorf("apperror.CodeHTTPConflict is permanently forbidden in %s: found %d", file, n)
		}
	}
}

// TestHTTPErrorExitGuardCatchesBypasses verifies the AST scanner detects import
// aliases, echo.Err* singletons, struct literals, new(echo.HTTPError), direct
// >= 400 response writes (including inside FuncLit closures), dynamic direct
// writes, and apperror.CodeHTTPConflict.
func TestHTTPErrorExitGuardCatchesBypasses(t *testing.T) {
	t.Parallel()

	const synthetic = `package sample

import (
	h "net/http"
	e "github.com/labstack/echo/v4"
	ae "github.com/felinics/memoh/internal/apperror"
)

func bypass(c e.Context, status int) error {
	_ = e.NewHTTPError(h.StatusConflict)
	_ = e.NewHTTPError(409)
	_ = e.NewHTTPError(status)
	_ = e.ErrConflict
	_ = &e.HTTPError{Code: h.StatusConflict}
	_ = e.HTTPError{Code: 400}
	_ = new(e.HTTPError)
	_ = c.JSON(h.StatusConflict, nil)
	_ = c.String(400, "bad")
	_ = c.NoContent(500)
	_ = c.Blob(h.StatusForbidden, "text/plain", nil)
	_ = c.HTML(h.StatusBadGateway, "err")
	_ = c.JSON(status, nil)
	_ = c.JSON(h.StatusOK, nil)
	_ = func(inner e.Context) error {
		return inner.JSON(h.StatusConflict, nil)
	}
	_ = ae.CodeHTTPConflict
	return nil
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "synthetic.go", synthetic, 0)
	if err != nil {
		t.Fatalf("parse synthetic: %v", err)
	}
	report := newHTTPErrorScanReport()
	scanFileForHTTPErrors(fset, "synthetic.go", file, report)

	if got := report.newHTTPError["synthetic.go"]["http.StatusConflict"]; got != 1 {
		t.Errorf("aliased NewHTTPError(StatusConflict) = %d, want 1", got)
	}
	if got := report.newHTTPError["synthetic.go"]["409"]; got != 1 {
		t.Errorf("aliased NewHTTPError(409) = %d, want 1", got)
	}
	if got := report.newHTTPError["synthetic.go"][dynamicStatusKey]; got != 1 {
		t.Errorf("aliased NewHTTPError(dynamic) = %d, want 1", got)
	}
	if got := report.echoErr["synthetic.go"]["ErrConflict"]; got != 1 {
		t.Errorf("aliased echo.ErrConflict = %d, want 1", got)
	}
	if len(report.httpErrorStructs) != 3 {
		t.Errorf("httpErrorStructs = %v, want 3 violations", report.httpErrorStructs)
	}
	wantDirect := map[string]int{
		"JSON:http.StatusConflict":   2,
		"String:400":                 1,
		"NoContent:500":              1,
		"Blob:http.StatusForbidden":  1,
		"HTML:http.StatusBadGateway": 1,
		"JSON:" + dynamicStatusKey:   1,
	}
	for k, w := range wantDirect {
		if got := report.directErrorWrite["synthetic.go"][k]; got != w {
			t.Errorf("directErrorWrite[%q] = %d, want %d", k, got, w)
		}
	}
	if len(report.directErrorWrite["synthetic.go"]) != len(wantDirect) {
		t.Errorf("unexpected directErrorWrite entries (2xx should be ignored): %v", report.directErrorWrite["synthetic.go"])
	}
	if got := report.codeHTTP["synthetic.go"]["CodeHTTPConflict"]; got != 1 {
		t.Errorf("aliased CodeHTTPConflict = %d, want 1", got)
	}
}
