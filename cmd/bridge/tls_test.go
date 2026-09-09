package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/workspace/bridge"
)

const (
	testInstanceID  = "11111111-1111-1111-1111-111111111111"
	testServerName  = "memoh-bridge." + testInstanceID + ".bridge.memoh.internal"
	testServerURI   = "spiffe://memoh/instance/" + testInstanceID + "/server"
	testBridgeURI   = "spiffe://memoh/instance/" + testInstanceID + "/bridge"
	wrongInstanceID = "22222222-2222-2222-2222-222222222222"
)

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newTestCA(t *testing.T, cn string) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &testCA{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

func (ca *testCA) issue(t *testing.T, cn string, eku x509.ExtKeyUsage, dnsNames []string, uriSAN string) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{eku},
		DNSNames:     dnsNames,
	}
	if uriSAN != "" {
		parsed, err := url.Parse(uriSAN)
		if err != nil {
			t.Fatal(err)
		}
		template.URIs = []*url.URL{parsed}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

func writeTempFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// testMTLSMaterial 在临时目录铺出 instance 级全套材料（镜像 bytenet provisioning 的产物）。
type testMTLSMaterial struct {
	dir            string
	clientCA       *testCA
	bridgeCA       *testCA
	bridgeCertFile string
	bridgeKeyFile  string
	clientCAFile   string
	clientCertFile string
	clientKeyFile  string
	bridgeCAFile   string
}

func newTestMTLSMaterial(t *testing.T) *testMTLSMaterial {
	t.Helper()
	dir := t.TempDir()
	clientCA := newTestCA(t, "server-client CA")
	bridgeCA := newTestCA(t, "bridge-server CA")
	bridgeCert, bridgeKey := bridgeCA.issue(t, "bridge", x509.ExtKeyUsageServerAuth, []string{testServerName}, testBridgeURI)
	clientCert, clientKey := clientCA.issue(t, "server", x509.ExtKeyUsageClientAuth, nil, testServerURI)
	return &testMTLSMaterial{
		dir:            dir,
		clientCA:       clientCA,
		bridgeCA:       bridgeCA,
		bridgeCertFile: writeTempFile(t, dir, "bridge-server.crt", bridgeCert),
		bridgeKeyFile:  writeTempFile(t, dir, "bridge-server.key", bridgeKey),
		clientCAFile:   writeTempFile(t, dir, "server-client-ca.crt", clientCA.pem),
		clientCertFile: writeTempFile(t, dir, "server-client.crt", clientCert),
		clientKeyFile:  writeTempFile(t, dir, "server-client.key", clientKey),
		bridgeCAFile:   writeTempFile(t, dir, "bridge-server-ca.crt", bridgeCA.pem),
	}
}

func (m *testMTLSMaterial) setStrictEnv(t *testing.T) {
	t.Helper()
	t.Setenv(bridgeTLSModeEnv, bridgeTLSModeStrict)
	t.Setenv(bridgeTLSCertFileEnv, m.bridgeCertFile)
	t.Setenv(bridgeTLSKeyFileEnv, m.bridgeKeyFile)
	t.Setenv(bridgeTLSClientCAFileEnv, m.clientCAFile)
	t.Setenv(bridgeTLSExpectedClientURIEnv, testServerURI)
}

func (m *testMTLSMaterial) clientOptions() *bridge.TLSOptions {
	return &bridge.TLSOptions{
		ServerName:        testServerName,
		ExpectedServerURI: testBridgeURI,
		ClientCertFile:    m.clientCertFile,
		ClientKeyFile:     m.clientKeyFile,
		ServerCAFile:      m.bridgeCAFile,
	}
}

// startStrictServer 用 bridgeServerCredentials() 起一个真实 TLS gRPC listener。
// 不注册任何 service：握手成功 → RPC 报 Unimplemented；握手/身份校验失败 → Unavailable。
func startStrictServer(t *testing.T) string {
	t.Helper()
	creds, err := bridgeServerCredentials()
	if err != nil {
		t.Fatalf("bridgeServerCredentials: %v", err)
	}
	if creds == nil {
		t.Fatal("expected strict credentials, got nil")
	}
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(grpc.Creds(creds))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

// rpcErr 经 DialTLS 发起一次 RPC 并返回 bridge 客户端映射后的错误：
// 握手成功 → "grpc Unimplemented"（测试服务器未注册任何 service）；
// 握手/身份校验失败 → bridge.ErrUnavailable。
func rpcErr(t *testing.T, addr string, opts *bridge.TLSOptions) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := bridge.DialTLS(ctx, addr, opts)
	if err != nil {
		t.Fatalf("DialTLS: %v", err)
	}
	defer func() { _ = c.Close() }()
	_, err = c.ReadFile(ctx, "/etc/hostname", 0, 0)
	return err
}

func assertHandshakeOK(t *testing.T, err error) {
	t.Helper()
	if err == nil || errors.Is(err, bridge.ErrUnavailable) || !strings.Contains(err.Error(), "Unimplemented") {
		t.Fatalf("want Unimplemented (handshake ok), got %v", err)
	}
}

func assertHandshakeRejected(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, bridge.ErrUnavailable) {
		t.Fatalf("want ErrUnavailable (handshake rejected), got %v", err)
	}
}

func TestStrictMTLSAcceptsMemohServerIdentity(t *testing.T) {
	material := newTestMTLSMaterial(t)
	material.setStrictEnv(t)
	addr := startStrictServer(t)

	// 合法 server client cert：握手通过，错误是 Unimplemented（无注册服务），
	// 证明 TLS 通道建立成功。
	assertHandshakeOK(t, rpcErr(t, addr, material.clientOptions()))
}

func TestStrictMTLSRejectsBridgeCertAsClientCert(t *testing.T) {
	material := newTestMTLSMaterial(t)
	material.setStrictEnv(t)
	addr := startStrictServer(t)

	// 核心威胁模型：被攻破的 bot 拿 pod 内仅有的 shared bridge server cert 当
	// client cert 横向调用其他 bridge——证书由 bridge CA 签发且无 ClientAuth EKU，
	// 必须被拒。
	opts := material.clientOptions()
	opts.ClientCertFile = material.bridgeCertFile
	opts.ClientKeyFile = material.bridgeKeyFile
	assertHandshakeRejected(t, rpcErr(t, addr, opts))
}

func TestStrictMTLSRejectsClientWithoutCert(t *testing.T) {
	material := newTestMTLSMaterial(t)
	material.setStrictEnv(t)
	addr := startStrictServer(t)

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(material.bridgeCA.pem)
	creds := credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, ServerName: testServerName, RootCAs: pool})
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds), grpc.WithAuthority(testServerName))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Invoke(ctx, "/bridge.v1.ContainerService/ReadFile", nil, nil); status.Code(err) != codes.Unavailable {
		t.Fatalf("certless client got %v, want Unavailable", status.Code(err))
	}
}

func TestStrictMTLSRejectsWrongCAClientCert(t *testing.T) {
	material := newTestMTLSMaterial(t)
	material.setStrictEnv(t)
	addr := startStrictServer(t)

	rogueCA := newTestCA(t, "rogue CA")
	rogueCert, rogueKey := rogueCA.issue(t, "rogue", x509.ExtKeyUsageClientAuth, nil, testServerURI)
	opts := material.clientOptions()
	opts.ClientCertFile = writeTempFile(t, material.dir, "rogue.crt", rogueCert)
	opts.ClientKeyFile = writeTempFile(t, material.dir, "rogue.key", rogueKey)
	assertHandshakeRejected(t, rpcErr(t, addr, opts))
}

func TestStrictMTLSRejectsWrongInstanceURI(t *testing.T) {
	material := newTestMTLSMaterial(t)
	material.setStrictEnv(t)
	addr := startStrictServer(t)

	// CA 正确、EKU 正确、但 instance 不匹配 → VerifyConnection 拒绝。
	wrongURI := "spiffe://memoh/instance/" + wrongInstanceID + "/server"
	cert, key := material.clientCA.issue(t, "other-instance server", x509.ExtKeyUsageClientAuth, nil, wrongURI)
	opts := material.clientOptions()
	opts.ClientCertFile = writeTempFile(t, material.dir, "other.crt", cert)
	opts.ClientKeyFile = writeTempFile(t, material.dir, "other.key", key)
	assertHandshakeRejected(t, rpcErr(t, addr, opts))
}

func TestClientRejectsWrongBridgeURI(t *testing.T) {
	material := newTestMTLSMaterial(t)
	material.setStrictEnv(t)
	addr := startStrictServer(t)

	// 客户端把对端钉到本 instance 的 bridge URI；server cert instance 不符必须拒连。
	opts := material.clientOptions()
	opts.ExpectedServerURI = "spiffe://memoh/instance/" + wrongInstanceID + "/bridge"
	assertHandshakeRejected(t, rpcErr(t, addr, opts))
}

func TestBridgeServerCredentialsModeValidation(t *testing.T) {
	t.Setenv(bridgeTLSModeEnv, "")
	if creds, err := bridgeServerCredentials(); err != nil || creds != nil {
		t.Fatalf("empty mode = (%v, %v), want (nil, nil)", creds, err)
	}
	t.Setenv(bridgeTLSModeEnv, bridgeTLSModeDisabled)
	if creds, err := bridgeServerCredentials(); err != nil || creds != nil {
		t.Fatalf("disabled mode = (%v, %v), want (nil, nil)", creds, err)
	}
	t.Setenv(bridgeTLSModeEnv, "permissive")
	if _, err := bridgeServerCredentials(); err == nil {
		t.Fatal("unknown mode must error")
	}
	// strict 缺材料：必须报错，不允许静默回退明文。
	t.Setenv(bridgeTLSModeEnv, bridgeTLSModeStrict)
	t.Setenv(bridgeTLSCertFileEnv, "")
	t.Setenv(bridgeTLSKeyFileEnv, "")
	t.Setenv(bridgeTLSClientCAFileEnv, "")
	t.Setenv(bridgeTLSExpectedClientURIEnv, "")
	if _, err := bridgeServerCredentials(); err == nil {
		t.Fatal("strict without material must error")
	}
}
