package arch

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var (
	protoPackagePattern   = regexp.MustCompile(`(?m)^\s*package\s+([A-Za-z0-9_.]+)\s*;`)
	protoGoPackagePattern = regexp.MustCompile(`(?m)^\s*option\s+go_package\s*=\s*"([^"]+)"\s*;`)
	// The separator is [ \t]+ rather than \s+ on purpose: \s crosses newlines,
	// so a marker with no note would capture the following line as its note.
	protoPlanePattern              = regexp.MustCompile(`(?m)^[ \t]*//[ \t]*memoh:plane=([a-z]+)(?:[ \t]+([^\n]*))?[ \t]*$`)
	protoReservedPattern           = regexp.MustCompile(`(?m)^\s*reserved\s+`)
	protoMapPattern                = regexp.MustCompile(`(?m)^\s*(?:repeated\s+|optional\s+)?map\s*<`)
	protoBytesPattern              = regexp.MustCompile(`(?m)^\s*(?:repeated\s+|optional\s+)?bytes\s+`)
	protoJSONFieldPattern          = regexp.MustCompile(`(?mi)^\s*(?:repeated\s+|optional\s+)?[A-Za-z0-9_.<>]+\s+[A-Za-z0-9_]*json[A-Za-z0-9_]*\s*=`)
	protoGenericMethodFieldPattern = regexp.MustCompile(`(?mi)^\s*(?:repeated\s+|optional\s+)?[A-Za-z0-9_.<>]+\s+method\s*=`)
	protoVersionPackagePattern     = regexp.MustCompile(`(?m)^\s*package\s+[A-Za-z0-9_.]+\.v[0-9]+\s*;`)
	protoCompatTypePattern         = regexp.MustCompile(`(?mi)^\s*(?:message|service)\s+(?:Legacy|Compat|[A-Za-z0-9_]*(?:V1|V2))[A-Za-z0-9_]*\s*\{`)

	// generatedSourcePattern reads the "// source: <path>" line protoc-gen-go
	// stamps into every generated file.
	generatedSourcePattern = regexp.MustCompile(`(?m)^// source: (.+)$`)
)

// Every .proto declares which plane it serves with a `// memoh:plane=` comment,
// so the guards below read intent from the file instead of matching its path.
// A path-keyed allowlist would fail on any directory move and report it as a
// missing contract, which says nothing about whether the architecture still
// holds. These are the recognized planes:
//
//	control — the Server <-> Channel control plane. Exactly one final contract
//	          is allowed; it must carry no generic-envelope wire.
//	data    — bulk data-plane protocols (Runtime Bridge). Free to use bytes.
//	legacy  — pre-existing wire kept until its capability switches atomically.
//	          Exempt from the envelope rules, but every file must name the
//	          migration that retires it so the exemption cannot go stale.
const (
	planeControl = "control"
	planeData    = "data"
	planeLegacy  = "legacy"
)

// wantControlPlaneProtoPackage is the single proto namespace the final control
// plane may declare. Unlike a path, this is a wire-visible identity: changing
// it breaks deployed peers, so pinning it is a real constraint.
const wantControlPlaneProtoPackage = "memoh.channel"

type protoContract struct {
	file      string
	source    string
	plane     string
	planeNote string
}

// loadProtoContracts reads every .proto in the repository and resolves the
// plane it declares. A file with no marker, or an unrecognized one, is an
// error: new wire must state which plane it joins.
func loadProtoContracts(t *testing.T, root string) []protoContract {
	t.Helper()
	files := repositoryFiles(t, root, func(relPath string) bool {
		return strings.HasSuffix(relPath, ".proto")
	})
	if len(files) == 0 {
		t.Fatal("no .proto files found; the guard would vacuously pass")
	}

	contracts := make([]protoContract, 0, len(files))
	var violations []string
	for _, file := range files {
		data, err := os.ReadFile(filepath.Join(root, file)) //nolint:gosec // repository-owned contract
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		source := string(data)
		match := protoPlanePattern.FindStringSubmatch(source)
		if match == nil {
			violations = append(violations, fmt.Sprintf(
				"%s declares no plane; add a `// memoh:plane=control|data|legacy` comment", file))
			continue
		}
		plane, note := match[1], strings.TrimSpace(match[2])
		switch plane {
		case planeControl, planeData:
		case planeLegacy:
			if note == "" {
				violations = append(violations, fmt.Sprintf(
					"%s is marked legacy without naming the migration that retires it", file))
				continue
			}
		default:
			violations = append(violations, fmt.Sprintf(
				"%s declares unknown plane %q; want control, data, or legacy", file, plane))
			continue
		}
		contracts = append(contracts, protoContract{file: file, source: source, plane: plane, planeNote: note})
	}
	if len(violations) > 0 {
		slices.Sort(violations)
		t.Fatalf("every .proto must declare its plane:\n  %s", strings.Join(violations, "\n  "))
	}
	return contracts
}

// TestControlPlaneProtoIsSingleAndTyped keeps the only production control-plane
// RPC boundary on one typed protocol with no compatibility machinery. Legacy
// wire is exempt by its own declaration, which is what makes the exemption
// visible in the file it applies to rather than in a list far away from it.
func TestControlPlaneProtoIsSingleAndTyped(t *testing.T) {
	root := repoRoot(t)

	var control []protoContract
	for _, contract := range loadProtoContracts(t, root) {
		if contract.plane == planeControl {
			control = append(control, contract)
		}
	}
	if len(control) != 1 {
		names := make([]string, 0, len(control))
		for _, contract := range control {
			names = append(names, contract.file)
		}
		slices.Sort(names)
		t.Fatalf("want exactly 1 control-plane proto, found %d: %s", len(control), strings.Join(names, ", "))
	}

	contract := control[0]
	var violations []string
	if match := protoPackagePattern.FindStringSubmatch(contract.source); len(match) != 2 || match[1] != wantControlPlaneProtoPackage {
		got := "missing"
		if len(match) == 2 {
			got = match[1]
		}
		violations = append(violations, fmt.Sprintf("%s uses proto package %q; want %q",
			contract.file, got, wantControlPlaneProtoPackage))
	}
	for _, forbidden := range []struct {
		pattern *regexp.Regexp
		reason  string
	}{
		{protoReservedPattern, "reserved compatibility declaration"},
		{protoMapPattern, "generic map field"},
		{protoBytesPattern, "unapproved bytes wire field"},
		{protoJSONFieldPattern, "raw JSON field"},
		{protoGenericMethodFieldPattern, "generic method dispatch field"},
		{protoVersionPackagePattern, "versioned compatibility namespace"},
		{protoCompatTypePattern, "compatibility service or DTO"},
	} {
		if forbidden.pattern.MatchString(contract.source) {
			violations = append(violations, fmt.Sprintf("%s contains %s", contract.file, forbidden.reason))
		}
	}
	for _, genericImport := range []string{
		"google/protobuf/any.proto",
		"google/protobuf/struct.proto",
	} {
		if strings.Contains(contract.source, genericImport) {
			violations = append(violations, fmt.Sprintf("%s imports forbidden generic wire %s", contract.file, genericImport))
		}
	}
	if len(violations) > 0 {
		slices.Sort(violations)
		t.Fatalf("final Channel RPC must use one typed protocol with no compatibility wire:\n  %s",
			strings.Join(violations, "\n  "))
	}
}

// TestProtoGoPackageMatchesDirectory is the guard that catches a rename the
// compiler cannot: go_package drives what `mise run *-generate` writes, so a
// stale value keeps building fine until someone regenerates and silently gets
// the old package back.
func TestProtoGoPackageMatchesDirectory(t *testing.T) {
	root := repoRoot(t)
	var violations []string
	for _, contract := range loadProtoContracts(t, root) {
		match := protoGoPackagePattern.FindStringSubmatch(contract.source)
		if match == nil {
			violations = append(violations, fmt.Sprintf("%s declares no go_package option", contract.file))
			continue
		}
		importPath, pkgName, _ := strings.Cut(match[1], ";")

		wantImportPath := modulePrefix + path.Dir(contract.file)
		if importPath != wantImportPath {
			violations = append(violations, fmt.Sprintf("%s go_package import path is %q; want %q",
				contract.file, importPath, wantImportPath))
		}
		// The ";name" suffix is optional; protoc derives the Go package from the
		// import path's last element when it is absent. Either way the effective
		// name must match the directory, or regeneration renames the package.
		wantPkgName := path.Base(path.Dir(contract.file))
		if pkgName != "" && pkgName != wantPkgName {
			violations = append(violations, fmt.Sprintf("%s go_package name is %q; want %q (the directory name)",
				contract.file, pkgName, wantPkgName))
		}
	}
	if len(violations) > 0 {
		slices.Sort(violations)
		t.Fatalf("proto go_package must match its directory or regeneration will undo renames:\n  %s",
			strings.Join(violations, "\n  "))
	}
}

// TestGeneratedProtoCodeMatchesItsSource pairs every *.pb.go with a real .proto
// and every .proto with generated output, so a moved contract cannot leave
// orphaned generated code that still compiles against a path nobody owns.
func TestGeneratedProtoCodeMatchesItsSource(t *testing.T) {
	root := repoRoot(t)

	protoExists := map[string]bool{}
	for _, contract := range loadProtoContracts(t, root) {
		protoExists[contract.file] = true
	}
	generatedFor := map[string]bool{}

	var violations []string
	for _, file := range repositoryFiles(t, root, func(relPath string) bool {
		return strings.HasSuffix(relPath, ".pb.go")
	}) {
		data, err := os.ReadFile(filepath.Join(root, file)) //nolint:gosec // repository-owned generated contract
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		source := string(data)
		if !strings.HasPrefix(strings.TrimSpace(source), "// Code generated by protoc-gen-go") {
			violations = append(violations, fmt.Sprintf("%s must remain protoc-generated", file))
			continue
		}
		match := generatedSourcePattern.FindStringSubmatch(source)
		if match == nil {
			violations = append(violations, fmt.Sprintf("%s has no `// source:` line", file))
			continue
		}
		src := strings.TrimSpace(match[1])
		if !protoExists[src] {
			violations = append(violations, fmt.Sprintf("%s is generated from %s, which does not exist", file, src))
			continue
		}
		if path.Dir(src) != path.Dir(file) {
			violations = append(violations, fmt.Sprintf("%s sits apart from its source %s", file, src))
		}
		generatedFor[src] = true
	}
	for src := range protoExists {
		if !generatedFor[src] {
			violations = append(violations, fmt.Sprintf("%s has no generated *.pb.go beside it", src))
		}
	}
	if len(violations) > 0 {
		slices.Sort(violations)
		t.Fatalf("generated protobuf must stay paired with its source:\n  %s", strings.Join(violations, "\n  "))
	}
}

// TestServerOwnerRPCScaffoldingDoesNotReturn prevents Server-local owners from
// becoming artificial process boundaries. Inspect directories as well as files
// so an empty scaffold cannot be introduced ahead of an implementation.
func TestServerOwnerRPCScaffoldingDoesNotReturn(t *testing.T) {
	root := repoRoot(t)
	var violations []string
	for _, ownerRoot := range []string{"services", "domains"} {
		err := filepath.WalkDir(filepath.Join(root, ownerRoot), func(filePath string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			relPath, err := filepath.Rel(root, filePath)
			if err != nil {
				return err
			}
			if reason := forbiddenServerOwnerScaffold(relPath); reason != "" {
				violations = append(violations, fmt.Sprintf("%s: %s", filepath.ToSlash(relPath), reason))
				if entry.IsDir() {
					return filepath.SkipDir
				}
			}
			return nil
		})
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("walk %s: %v", ownerRoot, err)
		}
	}
	if len(violations) > 0 {
		slices.Sort(violations)
		t.Fatalf("Server-local owner RPC scaffolding must not return:\n  %s", strings.Join(violations, "\n  "))
	}
}

func forbiddenServerOwnerScaffold(relPath string) string {
	parts := strings.Split(filepath.ToSlash(relPath), "/")
	if len(parts) < 3 || (parts[0] != "services" && parts[0] != "domains") {
		return ""
	}
	switch parts[1] {
	case "api", "agent", "memory", "model", "runtime", "media":
	default:
		return ""
	}
	switch parts[2] {
	case "grpc":
		return "RPC belongs under internal/rpc/channel at the real process boundary"
	case "local":
		return "embedded composition must inject the owner service directly"
	case "proto", "pb":
		return "production protobuf contracts belong under internal/rpc/channel"
	default:
		return ""
	}
}

func TestFinalChannelTransportHasNoCompatibilityPackages(t *testing.T) {
	root := repoRoot(t)
	var violations []string
	for _, file := range repositoryFiles(t, root, func(relPath string) bool {
		return strings.HasPrefix(relPath, "internal/rpc/channel/")
	}) {
		pathParts := strings.Split(strings.ToLower(filepath.ToSlash(file)), "/")
		for _, part := range pathParts {
			name := strings.TrimSuffix(part, filepath.Ext(part))
			switch name {
			case "compat", "compatibility", "legacy", "fallback", "v1", "v2":
				violations = append(violations, file)
			}
		}
	}
	if len(violations) > 0 {
		slices.Sort(violations)
		t.Fatalf("final Channel transport contains compatibility packages or files:\n  %s", strings.Join(violations, "\n  "))
	}
}

// TestDomainRootContractsDoNotImportImplementation keeps owner roots as pure
// values and errors. Transport, generated persistence, HTTP, and composition
// dependencies belong in adapters below the owner root.
func TestDomainRootContractsDoNotImportImplementation(t *testing.T) {
	root := repoRoot(t)
	var violations []string
	for _, file := range repositoryGoFiles(t, root, false) {
		parts := strings.Split(filepath.ToSlash(file), "/")
		if len(parts) != 3 ||
			(parts[0] != "services" && parts[0] != "domains") ||
			path.Ext(parts[2]) != ".go" {
			continue
		}
		for _, importPath := range imports(t, root, file) {
			if reason := forbiddenDomainRootImport(importPath); reason != "" {
				violations = append(violations, fmt.Sprintf("%s imports %s (%s)", file, importPath, reason))
			}
		}
	}
	if len(violations) > 0 {
		slices.Sort(violations)
		t.Fatalf("domain root contracts must not depend on implementation layers:\n  %s", strings.Join(violations, "\n  "))
	}
}

func forbiddenDomainRootImport(importPath string) string {
	switch {
	case strings.HasPrefix(importPath, "google.golang.org/grpc"):
		return "gRPC transport"
	case strings.HasPrefix(importPath, "go.uber.org/fx"):
		return "FX composition"
	case strings.HasPrefix(importPath, "github.com/labstack/echo"):
		return "Echo HTTP transport"
	case strings.Contains(importPath, "/grpc/pb"):
		return "generated protobuf transport"
	case importPath == modulePrefix+"internal/rpc/channel/channelpb":
		return "generated Channel protobuf transport"
	case strings.Contains(importPath, "/postgres/sqlc") || importPath == modulePrefix+"internal/db/postgres/sqlc":
		return "generated SQLC persistence"
	default:
		return ""
	}
}

const (
	runtimeBridgeClientPrefix          = "domains/runtime/bridge/client/"
	runtimeBridgeServerPrefix          = "domains/runtime/bridge/server/"
	retiredWorkspaceBridgeImport       = modulePrefix + "internal/workspace/bridgepb"
	retiredWorkspaceBridgeClientImport = modulePrefix + "internal/workspace/bridge"
	retiredWorkspaceBridgeServerImport = modulePrefix + "internal/workspace/bridgesvc"
)

// TestRuntimeBridgePBContract keeps the public Bridge wire package free of
// Memoh internal imports and free of the retired workspace/bridgepb path. The
// data-plane proto locates itself by its plane marker, so moving the package
// stays a rename rather than a contract violation.
func TestRuntimeBridgePBContract(t *testing.T) {
	root := repoRoot(t)

	var dataPlane []protoContract
	for _, contract := range loadProtoContracts(t, root) {
		if contract.plane == planeData {
			dataPlane = append(dataPlane, contract)
		}
	}
	if len(dataPlane) != 1 {
		t.Fatalf("want exactly 1 data-plane proto (the Runtime Bridge), found %d", len(dataPlane))
	}
	bridgeDir := path.Dir(dataPlane[0].file)
	if _, err := os.Stat(filepath.Join(root, "internal/workspace/bridgepb")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retired path internal/workspace/bridgepb must not exist")
	}

	var violations []string
	for _, file := range goFiles(t, root, bridgeDir) {
		for _, imp := range imports(t, root, file) {
			if strings.HasPrefix(imp, modulePrefix+"internal/") {
				violations = append(violations, fmt.Sprintf("%s imports %s", file, imp))
			}
		}
	}
	for _, file := range repositoryGoFiles(t, root, true) {
		for _, imp := range imports(t, root, file) {
			if imp == retiredWorkspaceBridgeImport {
				violations = append(violations, fmt.Sprintf("%s imports retired %s", file, imp))
			}
		}
	}
	if len(violations) > 0 {
		slices.Sort(violations)
		t.Fatalf("runtime bridge pb contract violated:\n  %s", strings.Join(violations, "\n  "))
	}
}

// TestRuntimeBridgeClientContract keeps the public Bridge gRPC client at its
// approved domains path, free of Memoh internal imports, and free of the
// retired workspace/bridge import path.
func TestRuntimeBridgeClientContract(t *testing.T) {
	root := repoRoot(t)

	clientPath := filepath.Join(root, runtimeBridgeClientPrefix+"client.go")
	if _, err := os.Stat(clientPath); err != nil {
		t.Fatalf("runtime bridge client missing at %s: %v", runtimeBridgeClientPrefix+"client.go", err)
	}
	if _, err := os.Stat(filepath.Join(root, "internal/workspace/bridge")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retired path internal/workspace/bridge must not exist")
	}

	var violations []string
	for _, file := range allGoFiles(t, root, strings.TrimSuffix(runtimeBridgeClientPrefix, "/")) {
		for _, imp := range imports(t, root, file) {
			if strings.HasPrefix(imp, modulePrefix+"internal/") {
				violations = append(violations, fmt.Sprintf("%s imports %s", file, imp))
			}
		}
	}
	for _, file := range repositoryGoFiles(t, root, true) {
		for _, imp := range imports(t, root, file) {
			if imp == retiredWorkspaceBridgeClientImport {
				violations = append(violations, fmt.Sprintf("%s imports retired %s", file, imp))
			}
		}
	}
	if len(violations) > 0 {
		slices.Sort(violations)
		t.Fatalf("runtime bridge client contract violated:\n  %s", strings.Join(violations, "\n  "))
	}
}

// TestRuntimeBridgeServerContract keeps the public Bridge gRPC server at its
// approved domains path, free of Memoh internal imports, and free of the
// retired workspace/bridgesvc import path.
func TestRuntimeBridgeServerContract(t *testing.T) {
	root := repoRoot(t)

	serverPath := filepath.Join(root, runtimeBridgeServerPrefix+"server.go")
	if _, err := os.Stat(serverPath); err != nil {
		t.Fatalf("runtime bridge server missing at %s: %v", runtimeBridgeServerPrefix+"server.go", err)
	}
	if _, err := os.Stat(filepath.Join(root, "internal/workspace/bridgesvc")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retired path internal/workspace/bridgesvc must not exist")
	}

	var violations []string
	for _, file := range allGoFiles(t, root, strings.TrimSuffix(runtimeBridgeServerPrefix, "/")) {
		for _, imp := range imports(t, root, file) {
			if strings.HasPrefix(imp, modulePrefix+"internal/") {
				violations = append(violations, fmt.Sprintf("%s imports %s", file, imp))
			}
		}
	}
	for _, file := range repositoryGoFiles(t, root, true) {
		for _, imp := range imports(t, root, file) {
			if imp == retiredWorkspaceBridgeServerImport {
				violations = append(violations, fmt.Sprintf("%s imports retired %s", file, imp))
			}
		}
	}
	if len(violations) > 0 {
		slices.Sort(violations)
		t.Fatalf("runtime bridge server contract violated:\n  %s", strings.Join(violations, "\n  "))
	}
}
