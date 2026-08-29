package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/config"
)

const (
	serverClientCertFile = "server-client.crt"
	serverClientKeyFile  = "server-client.key"
	bridgeServerCAFile   = "bridge-server-ca.crt"

	bridgeServerCertFile   = "bridge-server.crt"
	bridgeServerKeyFile    = "bridge-server.key"
	serverClientCACertFile = "server-client-ca.crt"

	defaultLeafDays = 365
	defaultCADays   = 2 * defaultLeafDays
)

type commandOptions struct {
	instanceID string
	outDir     string
	serverName string
	force      bool
	leafDays   int
	caDays     int
}

type fileMaterial struct {
	name string
	data []byte
}

type bridgeMTLSMaterial struct {
	server []fileMaterial
	bridge []fileMaterial
}

type materialLayout struct {
	rootDir   string
	serverDir string
	bridgeDir string
}

type leafSpec struct {
	commonName  string
	extKeyUsage x509.ExtKeyUsage
	dnsNames    []string
	uris        []*url.URL
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	opts, err := parseOptions(args, stdout)
	if err != nil {
		return err
	}
	instanceID, err := canonicalInstanceID(opts.instanceID)
	if err != nil {
		return err
	}
	serverName := strings.TrimSpace(opts.serverName)
	if serverName == "" {
		serverName = config.BridgeServerName(instanceID)
	}
	if opts.leafDays <= 0 {
		return errors.New("leaf-days must be positive")
	}
	if opts.caDays <= 0 {
		return errors.New("ca-days must be positive")
	}
	if opts.caDays < opts.leafDays {
		return errors.New("ca-days must be greater than or equal to leaf-days")
	}

	material, err := generateBridgeMTLS(instanceID, serverName, days(opts.leafDays), days(opts.caDays))
	if err != nil {
		return err
	}
	layout, err := writeBridgeMTLSMaterial(opts.outDir, material, opts.force)
	if err != nil {
		return err
	}
	return printSummary(stdout, instanceID, serverName, layout)
}

func parseOptions(args []string, output io.Writer) (commandOptions, error) {
	opts := commandOptions{
		outDir:   "data/bridge-mtls",
		leafDays: defaultLeafDays,
		caDays:   defaultCADays,
	}
	fs := flag.NewFlagSet("gen-bridge-mtls", flag.ContinueOnError)
	fs.SetOutput(output)
	fs.StringVar(&opts.instanceID, "instance-id", "", "Memoh instance UUID used in SPIFFE URI SANs")
	fs.StringVar(&opts.outDir, "out", opts.outDir, "output directory that will receive server/ and bridge/")
	fs.StringVar(&opts.serverName, "server-name", "", "optional synthetic bridge DNS SAN; defaults from instance-id")
	fs.BoolVar(&opts.force, "force", false, "replace existing server/ and bridge/ material directories")
	fs.IntVar(&opts.leafDays, "leaf-days", opts.leafDays, "leaf certificate validity in days")
	fs.IntVar(&opts.caDays, "ca-days", opts.caDays, "CA certificate validity in days")
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if fs.NArg() != 0 {
		return opts, fmt.Errorf("unexpected positional arguments: %s", strings.Join(fs.Args(), " "))
	}
	if strings.TrimSpace(opts.instanceID) == "" {
		return opts, errors.New("instance-id is required")
	}
	if strings.TrimSpace(opts.outDir) == "" {
		return opts, errors.New("out is required")
	}
	return opts, nil
}

func canonicalInstanceID(raw string) (string, error) {
	id, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("instance-id must be a UUID: %w", err)
	}
	if id == uuid.Nil {
		return "", errors.New("instance-id must not be the nil UUID")
	}
	return id.String(), nil
}

func days(value int) time.Duration {
	return time.Duration(value) * 24 * time.Hour
}

func generateBridgeMTLS(instanceID, serverName string, leafTTL, caTTL time.Duration) (bridgeMTLSMaterial, error) {
	clientCACert, clientCAKey, clientCAPEM, err := newMTLSCA(fmt.Sprintf("memoh instance %s server-client CA", instanceID), caTTL)
	if err != nil {
		return bridgeMTLSMaterial{}, err
	}
	bridgeCACert, bridgeCAKey, bridgeCAPEM, err := newMTLSCA(fmt.Sprintf("memoh instance %s bridge-server CA", instanceID), caTTL)
	if err != nil {
		return bridgeMTLSMaterial{}, err
	}

	clientURI, err := url.Parse(config.ServerClientSPIFFE(instanceID))
	if err != nil {
		return bridgeMTLSMaterial{}, err
	}
	clientCertPEM, clientKeyPEM, err := newMTLSLeaf(clientCACert, clientCAKey, leafSpec{
		commonName:  fmt.Sprintf("memoh instance %s server", instanceID),
		extKeyUsage: x509.ExtKeyUsageClientAuth,
		uris:        []*url.URL{clientURI},
	}, leafTTL)
	if err != nil {
		return bridgeMTLSMaterial{}, err
	}

	bridgeURI, err := url.Parse(config.BridgeServerSPIFFE(instanceID))
	if err != nil {
		return bridgeMTLSMaterial{}, err
	}
	bridgeCertPEM, bridgeKeyPEM, err := newMTLSLeaf(bridgeCACert, bridgeCAKey, leafSpec{
		commonName:  fmt.Sprintf("memoh instance %s bridge", instanceID),
		extKeyUsage: x509.ExtKeyUsageServerAuth,
		dnsNames:    []string{serverName},
		uris:        []*url.URL{bridgeURI},
	}, leafTTL)
	if err != nil {
		return bridgeMTLSMaterial{}, err
	}

	return bridgeMTLSMaterial{
		server: []fileMaterial{
			{name: serverClientCertFile, data: clientCertPEM},
			{name: serverClientKeyFile, data: clientKeyPEM},
			{name: bridgeServerCAFile, data: bridgeCAPEM},
		},
		bridge: []fileMaterial{
			{name: bridgeServerCertFile, data: bridgeCertPEM},
			{name: bridgeServerKeyFile, data: bridgeKeyPEM},
			{name: serverClientCACertFile, data: clientCAPEM},
		},
	}, nil
}

func newMTLSCA(commonName string, ttl time.Duration) (*x509.Certificate, *ecdsa.PrivateKey, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	serial, err := randomCertSerial()
	if err != nil {
		return nil, nil, nil, err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName, Organization: []string{"memoh"}},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(ttl),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, nil, err
	}
	return cert, key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

func newMTLSLeaf(ca *x509.Certificate, caKey *ecdsa.PrivateKey, spec leafSpec, ttl time.Duration) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := randomCertSerial()
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: spec.commonName, Organization: []string{"memoh"}},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     now.Add(ttl),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{spec.extKeyUsage},
		DNSNames:     spec.dnsNames,
		URIs:         spec.uris,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

func randomCertSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, limit)
}

func writeBridgeMTLSMaterial(root string, material bridgeMTLSMaterial, force bool) (materialLayout, error) {
	rootDir, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return materialLayout{}, err
	}
	if rootDir == string(filepath.Separator) {
		return materialLayout{}, errors.New("refusing to write bridge mTLS material to filesystem root")
	}
	layout := materialLayout{
		rootDir:   rootDir,
		serverDir: filepath.Join(rootDir, "server"),
		bridgeDir: filepath.Join(rootDir, "bridge"),
	}
	if err := prepareMaterialDir(layout.serverDir, force); err != nil {
		return materialLayout{}, fmt.Errorf("prepare server material dir: %w", err)
	}
	if err := prepareMaterialDir(layout.bridgeDir, force); err != nil {
		return materialLayout{}, fmt.Errorf("prepare bridge material dir: %w", err)
	}
	if err := writeMaterialFiles(layout.serverDir, material.server); err != nil {
		return materialLayout{}, err
	}
	if err := writeMaterialFiles(layout.bridgeDir, material.bridge); err != nil {
		return materialLayout{}, err
	}
	return layout, nil
}

func prepareMaterialDir(dir string, force bool) error {
	if force {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		return os.MkdirAll(dir, 0o700)
	}
	entries, err := os.ReadDir(dir)
	if err == nil {
		if len(entries) != 0 {
			return fmt.Errorf("%s already exists and is not empty; use -force to replace it", dir)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	return os.MkdirAll(dir, 0o700)
}

func writeMaterialFiles(dir string, files []fileMaterial) error {
	for _, file := range files {
		path := filepath.Join(dir, file.name)
		// #nosec G304,G703 -- The output directory is operator-provided and this tool intentionally writes there.
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		_, writeErr := f.Write(file.data)
		closeErr := f.Close()
		if writeErr != nil {
			return fmt.Errorf("write %s: %w", path, writeErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close %s: %w", path, closeErr)
		}
	}
	return nil
}

func printSummary(w io.Writer, instanceID, serverName string, layout materialLayout) error {
	serverNameValue := "\"\""
	if serverName != config.BridgeServerName(instanceID) {
		serverNameValue = fmt.Sprintf("%q", serverName)
	}
	summary := fmt.Sprintf(`generated bridge mTLS material under %s

config.toml:
instance_id = %q

[bridge_tls]
mode = %q
server_dir = %q
bridge_dir = %q
server_name = %s
`, layout.rootDir, instanceID, config.BridgeTLSModeStrict, layout.serverDir, layout.bridgeDir, serverNameValue)
	// #nosec G705 -- This is CLI stdout, not HTML or a web response.
	_, err := io.WriteString(w, summary)
	return err
}
