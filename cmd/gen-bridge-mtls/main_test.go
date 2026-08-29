package main

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/config"
)

const testInstanceID = "11111111-1111-1111-1111-111111111111"

func TestGenerateBridgeMTLS(t *testing.T) {
	serverName := config.BridgeServerName(testInstanceID)
	material, err := generateBridgeMTLS(testInstanceID, serverName, 365*24*time.Hour, 2*365*24*time.Hour)
	if err != nil {
		t.Fatalf("generateBridgeMTLS: %v", err)
	}

	serverFiles := materialMap(material.server)
	bridgeFiles := materialMap(material.bridge)
	for _, name := range []string{serverClientCertFile, serverClientKeyFile, bridgeServerCAFile} {
		if len(serverFiles[name]) == 0 {
			t.Fatalf("server material missing %s", name)
		}
	}
	for _, name := range []string{bridgeServerCertFile, bridgeServerKeyFile, serverClientCACertFile} {
		if len(bridgeFiles[name]) == 0 {
			t.Fatalf("bridge material missing %s", name)
		}
	}
	for _, forbidden := range []string{serverClientCertFile, serverClientKeyFile, bridgeServerCAFile} {
		if _, ok := bridgeFiles[forbidden]; ok {
			t.Fatalf("bridge material must not contain %s", forbidden)
		}
	}
	for _, data := range [][]byte{serverFiles[bridgeServerCAFile], bridgeFiles[serverClientCACertFile]} {
		if strings.Contains(string(data), "PRIVATE KEY") {
			t.Fatal("CA bundle must not contain private key material")
		}
	}

	serverClientCA := mustParseCertPEM(t, bridgeFiles[serverClientCACertFile])
	bridgeServerCA := mustParseCertPEM(t, serverFiles[bridgeServerCAFile])
	serverClientCert := mustParseCertPEM(t, serverFiles[serverClientCertFile])
	bridgeServerCert := mustParseCertPEM(t, bridgeFiles[bridgeServerCertFile])

	serverClientPool := x509.NewCertPool()
	serverClientPool.AddCert(serverClientCA)
	bridgeServerPool := x509.NewCertPool()
	bridgeServerPool.AddCert(bridgeServerCA)

	if _, err := serverClientCert.Verify(x509.VerifyOptions{
		Roots:     serverClientPool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Fatalf("server client cert does not verify against server-client CA: %v", err)
	}
	if got := serverClientCert.URIs; len(got) != 1 || got[0].String() != config.ServerClientSPIFFE(testInstanceID) {
		t.Fatalf("server client cert URI SAN = %v", got)
	}
	if got := serverClientCert.ExtKeyUsage; len(got) != 1 || got[0] != x509.ExtKeyUsageClientAuth {
		t.Fatalf("server client cert EKU = %v, want ClientAuth only", got)
	}

	if _, err := bridgeServerCert.Verify(x509.VerifyOptions{
		Roots:     bridgeServerPool,
		DNSName:   serverName,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Fatalf("bridge server cert does not verify against bridge-server CA: %v", err)
	}
	if got := bridgeServerCert.URIs; len(got) != 1 || got[0].String() != config.BridgeServerSPIFFE(testInstanceID) {
		t.Fatalf("bridge server cert URI SAN = %v", got)
	}
	if got := bridgeServerCert.ExtKeyUsage; len(got) != 1 || got[0] != x509.ExtKeyUsageServerAuth {
		t.Fatalf("bridge server cert EKU = %v, want ServerAuth only", got)
	}

	if _, err := bridgeServerCert.Verify(x509.VerifyOptions{
		Roots:     serverClientPool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}); err == nil {
		t.Fatal("bridge server cert must not verify against server-client CA")
	}
	if _, err := serverClientCert.Verify(x509.VerifyOptions{
		Roots:     bridgeServerPool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}); err == nil {
		t.Fatal("server client cert must not verify against bridge-server CA")
	}
}

func TestRunWritesExpectedLayout(t *testing.T) {
	outDir := filepath.Join(t.TempDir(), "bridge-mtls")
	var stdout bytes.Buffer
	if err := run([]string{"-instance-id", testInstanceID, "-out", outDir}, &stdout); err != nil {
		t.Fatalf("run: %v", err)
	}

	for _, path := range []string{
		filepath.Join(outDir, "server", serverClientCertFile),
		filepath.Join(outDir, "server", serverClientKeyFile),
		filepath.Join(outDir, "server", bridgeServerCAFile),
		filepath.Join(outDir, "bridge", bridgeServerCertFile),
		filepath.Join(outDir, "bridge", bridgeServerKeyFile),
		filepath.Join(outDir, "bridge", serverClientCACertFile),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("stat generated file %s: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(outDir, "bridge", serverClientKeyFile)); !os.IsNotExist(err) {
		t.Fatalf("bridge dir must not contain %s: %v", serverClientKeyFile, err)
	}
	output := stdout.String()
	if !strings.Contains(output, `mode = "strict"`) ||
		!strings.Contains(output, `instance_id = "`+testInstanceID+`"`) {
		t.Fatalf("summary missing config snippet:\n%s", output)
	}
}

func TestRunRejectsInvalidInstanceID(t *testing.T) {
	var stdout bytes.Buffer
	if err := run([]string{"-instance-id", "not-a-uuid", "-out", t.TempDir()}, &stdout); err == nil || !strings.Contains(err.Error(), "must be a UUID") {
		t.Fatalf("invalid instance id err = %v", err)
	}
}

func TestRunRejectsExistingMaterialWithoutForce(t *testing.T) {
	outDir := filepath.Join(t.TempDir(), "bridge-mtls")
	var stdout bytes.Buffer
	if err := run([]string{"-instance-id", testInstanceID, "-out", outDir}, &stdout); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := run([]string{"-instance-id", testInstanceID, "-out", outDir}, &stdout); err == nil || !strings.Contains(err.Error(), "use -force") {
		t.Fatalf("second run err = %v", err)
	}
}

func materialMap(files []fileMaterial) map[string][]byte {
	out := make(map[string][]byte, len(files))
	for _, file := range files {
		out[file.name] = file.data
	}
	return out
}

func mustParseCertPEM(t *testing.T, data []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatalf("expected CERTIFICATE PEM block, got %v", block)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	return cert
}
