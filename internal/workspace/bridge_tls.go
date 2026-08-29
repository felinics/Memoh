package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

// memoh-server-mtls Secret 在 server pod 内的文件布局（设计 §6.1，由 bytenet 挂载）。
const (
	serverClientCertFile = "server-client.crt"
	serverClientKeyFile  = "server-client.key"
	bridgeServerCAFile   = "bridge-server-ca.crt"

	bridgeMTLSMountPath    = "/run/memoh/mtls/bridge"
	bridgeServerCertFile   = "bridge-server.crt"
	bridgeServerKeyFile    = "bridge-server.key"
	serverClientCACertFile = "server-client-ca.crt"
)

type BridgeTLSRuntimeOptions struct {
	Client            *bridge.TLSOptions
	BridgeMaterialDir string
	ExpectedClientURI string
}

// BridgeTLSOptionsFromConfig 把 server 配置翻译成 bridge dial 的 mTLS options。
// disabled → (nil, nil)。strict 但材料/instance-id 不全 → error：strict 不允许
// 静默回退明文，配置残缺必须在启动期暴露。
func BridgeTLSOptionsFromConfig(cfg config.Config) (*bridge.TLSOptions, error) {
	switch cfg.BridgeTLS.EffectiveMode() {
	case config.BridgeTLSModeDisabled:
		return nil, nil
	case config.BridgeTLSModeStrict:
	default:
		return nil, fmt.Errorf("bridge tls: unknown mode %q (want %s|%s)", cfg.BridgeTLS.Mode, config.BridgeTLSModeDisabled, config.BridgeTLSModeStrict)
	}
	instanceID := strings.TrimSpace(cfg.InstanceID)
	if instanceID == "" {
		return nil, errors.New("bridge tls: strict mode requires instance id (MEMOH_INSTANCE_ID)")
	}
	dir := strings.TrimSpace(cfg.BridgeTLS.ServerDir)
	if dir == "" {
		return nil, errors.New("bridge tls: strict mode requires server material dir (MEMOH_BRIDGE_TLS_SERVER_DIR)")
	}
	dir, err := normalizeMaterialDir(dir)
	if err != nil {
		return nil, fmt.Errorf("bridge tls: normalize server material dir: %w", err)
	}
	serverName := strings.TrimSpace(cfg.BridgeTLS.ServerName)
	if serverName == "" {
		serverName = config.BridgeServerName(instanceID)
	}
	opts := &bridge.TLSOptions{
		ServerName:        serverName,
		ExpectedServerURI: config.BridgeServerSPIFFE(instanceID),
		ClientCertFile:    filepath.Join(dir, serverClientCertFile),
		ClientKeyFile:     filepath.Join(dir, serverClientKeyFile),
		ServerCAFile:      filepath.Join(dir, bridgeServerCAFile),
	}
	if err := validateReadableMaterialFiles(opts.ClientCertFile, opts.ClientKeyFile, opts.ServerCAFile); err != nil {
		return nil, err
	}
	return opts, nil
}

func BridgeTLSRuntimeOptionsFromConfig(cfg config.Config) (*BridgeTLSRuntimeOptions, error) {
	clientOpts, err := BridgeTLSOptionsFromConfig(cfg)
	if err != nil || clientOpts == nil {
		return nil, err
	}
	bridgeDir := strings.TrimSpace(cfg.BridgeTLS.BridgeDir)
	if bridgeDir == "" {
		return nil, errors.New("bridge tls: strict mode requires bridge material dir (MEMOH_BRIDGE_TLS_BRIDGE_DIR)")
	}
	bridgeDir, err = normalizeMaterialDir(bridgeDir)
	if err != nil {
		return nil, fmt.Errorf("bridge tls: normalize bridge material dir: %w", err)
	}
	serverDir := filepath.Dir(clientOpts.ClientCertFile)
	sameDir, err := sameMaterialDir(bridgeDir, serverDir)
	if err != nil {
		return nil, err
	}
	if sameDir {
		return nil, errors.New("bridge tls: server material dir and bridge material dir must be different")
	}
	if err := validateReadableMaterialFiles(
		filepath.Join(bridgeDir, bridgeServerCertFile),
		filepath.Join(bridgeDir, bridgeServerKeyFile),
		filepath.Join(bridgeDir, serverClientCACertFile),
	); err != nil {
		return nil, err
	}
	if err := validateBridgeMaterialDirContents(bridgeDir); err != nil {
		return nil, err
	}
	instanceID := strings.TrimSpace(cfg.InstanceID)
	return &BridgeTLSRuntimeOptions{
		Client:            clientOpts,
		BridgeMaterialDir: bridgeDir,
		ExpectedClientURI: config.ServerClientSPIFFE(instanceID),
	}, nil
}

func normalizeMaterialDir(dir string) (string, error) {
	return filepath.Abs(filepath.Clean(dir))
}

func validateReadableMaterialFiles(paths ...string) error {
	for _, path := range paths {
		// #nosec G304 -- Bridge TLS material paths are operator-configured and must be opened to fail fast.
		file, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("bridge tls: read material file %q: %w", path, err)
		}
		info, statErr := file.Stat()
		closeErr := file.Close()
		if statErr != nil {
			return fmt.Errorf("bridge tls: stat material file %q: %w", path, statErr)
		}
		if closeErr != nil {
			return fmt.Errorf("bridge tls: close material file %q: %w", path, closeErr)
		}
		if info.IsDir() {
			return fmt.Errorf("bridge tls: material path %q is a directory", path)
		}
	}
	return nil
}

func validateBridgeMaterialDirContents(dir string) error {
	allowed := map[string]struct{}{
		bridgeServerCertFile:   {},
		bridgeServerKeyFile:    {},
		serverClientCACertFile: {},
	}
	// #nosec G304 -- Bridge TLS material dirs are operator-configured and must be inspected before mounting.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("bridge tls: read bridge material dir %q: %w", dir, err)
	}
	for _, entry := range entries {
		if _, ok := allowed[entry.Name()]; !ok {
			return fmt.Errorf("bridge tls: bridge material dir %q contains unexpected file %q", dir, entry.Name())
		}
	}
	return nil
}

func sameMaterialDir(left, right string) (bool, error) {
	leftInfo, err := os.Stat(left)
	if err != nil {
		return false, fmt.Errorf("bridge tls: stat material dir %q: %w", left, err)
	}
	if !leftInfo.IsDir() {
		return false, fmt.Errorf("bridge tls: material dir %q is not a directory", left)
	}
	rightInfo, err := os.Stat(right)
	if err != nil {
		return false, fmt.Errorf("bridge tls: stat material dir %q: %w", right, err)
	}
	if !rightInfo.IsDir() {
		return false, fmt.Errorf("bridge tls: material dir %q is not a directory", right)
	}
	return os.SameFile(leftInfo, rightInfo), nil
}
