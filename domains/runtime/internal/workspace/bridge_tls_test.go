package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/memohai/memoh/internal/config"
)

func TestBridgeTLSOptionsFromConfigDisabled(t *testing.T) {
	opts, err := BridgeTLSOptionsFromConfig(config.Config{})
	if err != nil || opts != nil {
		t.Fatalf("disabled mode = (%v, %v), want (nil, nil)", opts, err)
	}
}

func TestBridgeTLSOptionsFromConfigStrict(t *testing.T) {
	cfg := newBridgeTLSConfig(t)
	opts, err := BridgeTLSOptionsFromConfig(cfg)
	if err != nil {
		t.Fatalf("strict mode: %v", err)
	}
	if opts.ServerName != "memoh-bridge."+cfg.InstanceID+".bridge.memoh.internal" {
		t.Fatalf("derived ServerName = %q", opts.ServerName)
	}
	if opts.ExpectedServerURI != "spiffe://memoh/instance/"+cfg.InstanceID+"/bridge" {
		t.Fatalf("ExpectedServerURI = %q", opts.ExpectedServerURI)
	}
	if opts.ClientCertFile != filepath.Join(cfg.BridgeTLS.ServerDir, serverClientCertFile) ||
		opts.ClientKeyFile != filepath.Join(cfg.BridgeTLS.ServerDir, serverClientKeyFile) ||
		opts.ServerCAFile != filepath.Join(cfg.BridgeTLS.ServerDir, bridgeServerCAFile) {
		t.Fatalf("file paths = %q %q %q", opts.ClientCertFile, opts.ClientKeyFile, opts.ServerCAFile)
	}

	cfg.BridgeTLS.ServerName = "custom.name.internal"
	opts, err = BridgeTLSOptionsFromConfig(cfg)
	if err != nil || opts.ServerName != "custom.name.internal" {
		t.Fatalf("explicit ServerName = (%q, %v)", opts.ServerName, err)
	}
}

func TestBridgeTLSRuntimeOptionsFromConfigStrict(t *testing.T) {
	cfg := newBridgeTLSConfig(t)
	opts, err := BridgeTLSRuntimeOptionsFromConfig(cfg)
	if err != nil {
		t.Fatalf("strict runtime options: %v", err)
	}
	if opts.Client == nil {
		t.Fatal("runtime options missing client TLS config")
	}
	if opts.BridgeMaterialDir != cfg.BridgeTLS.BridgeDir {
		t.Fatalf("BridgeMaterialDir = %q", opts.BridgeMaterialDir)
	}
	if opts.ExpectedClientURI != "spiffe://memoh/instance/"+cfg.InstanceID+"/server" {
		t.Fatalf("ExpectedClientURI = %q", opts.ExpectedClientURI)
	}
}

func TestBridgeTLSOptionsFromConfigStrictRequiresMaterial(t *testing.T) {
	// strict 不许静默回退明文：instance id 或材料目录缺失必须在启动期报错。
	if _, err := BridgeTLSOptionsFromConfig(config.Config{
		BridgeTLS: config.BridgeTLSConfig{Mode: config.BridgeTLSModeStrict, ServerDir: "/x"},
	}); err == nil || !strings.Contains(err.Error(), "instance id") {
		t.Fatalf("missing instance id: err = %v", err)
	}
	if _, err := BridgeTLSOptionsFromConfig(config.Config{
		InstanceID: "i-1",
		BridgeTLS:  config.BridgeTLSConfig{Mode: config.BridgeTLSModeStrict},
	}); err == nil || !strings.Contains(err.Error(), "server material dir") {
		t.Fatalf("missing dir: err = %v", err)
	}
	if _, err := BridgeTLSOptionsFromConfig(config.Config{
		BridgeTLS: config.BridgeTLSConfig{Mode: "permissive"},
	}); err == nil || !strings.Contains(err.Error(), "unknown mode") {
		t.Fatalf("unknown mode: err = %v", err)
	}
	cfg := newBridgeTLSConfig(t)
	cfg.BridgeTLS.BridgeDir = ""
	if _, err := BridgeTLSRuntimeOptionsFromConfig(cfg); err == nil || !strings.Contains(err.Error(), "bridge material dir") {
		t.Fatalf("missing bridge dir: err = %v", err)
	}
}

func TestBridgeTLSOptionsFromConfigStrictRequiresReadableServerMaterial(t *testing.T) {
	cfg := newBridgeTLSConfig(t)
	if err := os.Remove(filepath.Join(cfg.BridgeTLS.ServerDir, serverClientKeyFile)); err != nil {
		t.Fatal(err)
	}

	if _, err := BridgeTLSOptionsFromConfig(cfg); err == nil || !strings.Contains(err.Error(), serverClientKeyFile) {
		t.Fatalf("missing server material: err = %v", err)
	}
}

func TestBridgeTLSRuntimeOptionsFromConfigRejectsSharedMaterialDir(t *testing.T) {
	cfg := newBridgeTLSConfig(t)
	cfg.BridgeTLS.BridgeDir = cfg.BridgeTLS.ServerDir

	if _, err := BridgeTLSRuntimeOptionsFromConfig(cfg); err == nil || !strings.Contains(err.Error(), "must be different") {
		t.Fatalf("shared material dir: err = %v", err)
	}
}

func TestBridgeTLSRuntimeOptionsFromConfigRejectsSymlinkedSharedMaterialDir(t *testing.T) {
	cfg := newBridgeTLSConfig(t)
	bridgeLink := filepath.Join(t.TempDir(), "bridge-link")
	if err := os.Symlink(cfg.BridgeTLS.ServerDir, bridgeLink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	cfg.BridgeTLS.BridgeDir = bridgeLink

	if _, err := BridgeTLSRuntimeOptionsFromConfig(cfg); err == nil || !strings.Contains(err.Error(), "must be different") {
		t.Fatalf("symlinked shared material dir: err = %v", err)
	}
}

func TestBridgeTLSRuntimeOptionsFromConfigRequiresReadableBridgeMaterial(t *testing.T) {
	cfg := newBridgeTLSConfig(t)
	if err := os.Remove(filepath.Join(cfg.BridgeTLS.BridgeDir, bridgeServerKeyFile)); err != nil {
		t.Fatal(err)
	}

	if _, err := BridgeTLSRuntimeOptionsFromConfig(cfg); err == nil || !strings.Contains(err.Error(), bridgeServerKeyFile) {
		t.Fatalf("missing bridge material: err = %v", err)
	}
}

func TestBridgeTLSRuntimeOptionsFromConfigRejectsUnexpectedBridgeMaterial(t *testing.T) {
	cfg := newBridgeTLSConfig(t)
	if err := os.WriteFile(filepath.Join(cfg.BridgeTLS.BridgeDir, serverClientKeyFile), []byte("must not leak\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := BridgeTLSRuntimeOptionsFromConfig(cfg); err == nil || !strings.Contains(err.Error(), "unexpected file") {
		t.Fatalf("unexpected bridge material: err = %v", err)
	}
}

func newBridgeTLSConfig(t *testing.T) config.Config {
	t.Helper()
	root := t.TempDir()
	serverDir := filepath.Join(root, "server")
	bridgeDir := filepath.Join(root, "bridge")
	writeMaterialFiles(t, serverDir, serverClientCertFile, serverClientKeyFile, bridgeServerCAFile)
	writeMaterialFiles(t, bridgeDir, bridgeServerCertFile, bridgeServerKeyFile, serverClientCACertFile)
	return config.Config{
		InstanceID: "11111111-1111-1111-1111-111111111111",
		BridgeTLS: config.BridgeTLSConfig{
			Mode:      config.BridgeTLSModeStrict,
			ServerDir: serverDir,
			BridgeDir: bridgeDir,
		},
	}
}

func writeMaterialFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("test material\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
