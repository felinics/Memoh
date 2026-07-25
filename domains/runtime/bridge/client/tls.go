package client

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"google.golang.org/grpc/credentials"
)

// TLSOptions 配置 Memoh Server → bridge 的 TCP gRPC mTLS（设计 §8.2）。
// nil 表示 disabled（开源/本地默认）。strict 下握手或身份校验失败一律拒绝，
// 不回退明文；UDS target 不论 mode 始终走本地信任模型（insecure）。
type TLSOptions struct {
	// ServerName 是 synthetic TLS ServerName（memoh-bridge.<instance-id>.bridge.memoh.internal）。
	// 连接目标是 PodIP（或 port-forward 的 localhost），证书校验必须用稳定名字
	// 而不是关闭 hostname verification。
	ServerName string
	// ExpectedServerURI 是 bridge server cert 必须携带的 URI SAN
	// （spiffe://memoh/instance/<instance-id>/bridge）。
	ExpectedServerURI string
	ClientCertFile    string
	ClientKeyFile     string
	// ServerCAFile 是 bridge-server CA bundle（bridge-server-ca.crt）。
	ServerCAFile string
}

func (o *TLSOptions) validate() error {
	if strings.TrimSpace(o.ServerName) == "" {
		return errors.New("bridge tls: server name is required")
	}
	if strings.TrimSpace(o.ExpectedServerURI) == "" {
		return errors.New("bridge tls: expected server URI is required")
	}
	if strings.TrimSpace(o.ClientCertFile) == "" || strings.TrimSpace(o.ClientKeyFile) == "" {
		return errors.New("bridge tls: client cert/key files are required")
	}
	if strings.TrimSpace(o.ServerCAFile) == "" {
		return errors.New("bridge tls: bridge server CA file is required")
	}
	return nil
}

// transportCredentials 构建 strict mTLS 的 gRPC transport credentials。
// 每次 dial 重新读文件，证书轮换（Secret 更新 + pod 滚动）后新连接自动用新材料。
func (o *TLSOptions) transportCredentials() (credentials.TransportCredentials, error) {
	if err := o.validate(); err != nil {
		return nil, err
	}
	cert, err := tls.LoadX509KeyPair(o.ClientCertFile, o.ClientKeyFile)
	if err != nil {
		return nil, fmt.Errorf("bridge tls: load client keypair: %w", err)
	}
	caPEM, err := os.ReadFile(o.ServerCAFile)
	if err != nil {
		return nil, fmt.Errorf("bridge tls: read bridge server CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("bridge tls: no certificates parsed from %s", o.ServerCAFile)
	}
	expectedURI := o.ExpectedServerURI
	cfg := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		ServerName:   o.ServerName,
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		// 标准握手已验:链由 bridge-server CA 签发、DNS SAN 匹配 ServerName、
		// EKU 含 ServerAuth。这里补 URI SAN 强校验,把对端钉死到本 instance 的
		// bridge 身份(防同 CA 误签/跨用途证书)。
		VerifyConnection: func(cs tls.ConnectionState) error {
			return verifyBridgeServerIdentity(cs, expectedURI)
		},
	}
	return credentials.NewTLS(cfg), nil
}

func verifyBridgeServerIdentity(cs tls.ConnectionState, expectedURI string) error {
	if len(cs.PeerCertificates) == 0 {
		return errors.New("bridge tls: server presented no certificate")
	}
	leaf := cs.PeerCertificates[0]
	if !slices.Contains(leaf.ExtKeyUsage, x509.ExtKeyUsageServerAuth) {
		return errors.New("bridge tls: server certificate lacks ServerAuth EKU")
	}
	for _, uri := range leaf.URIs {
		if uri.String() == expectedURI {
			return nil
		}
	}
	return fmt.Errorf("bridge tls: server certificate URI SAN mismatch (want %s)", expectedURI)
}

// appliesTo 判断 target 是否需要 mTLS：UDS 走文件系统权限的本地信任模型，
// 始终豁免；其余（PodIP:port、port-forward 的 localhost:port）一律 strict。
func (o *TLSOptions) appliesTo(target string) bool {
	if o == nil {
		return false
	}
	return !strings.HasPrefix(target, "unix://") && !strings.HasPrefix(target, "unix:")
}
