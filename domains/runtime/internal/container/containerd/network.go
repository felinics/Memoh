package containerd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	gocni "github.com/containerd/go-cni"

	containerapi "github.com/memohai/memoh/domains/runtime/container"
)

func (s *DefaultService) setupNetwork(ctx context.Context, req containerapi.NetworkRequest) (string, error) {
	containerID, netnsPath, err := s.resolveNetworkTarget(req)
	if err != nil {
		return "", err
	}
	cni, err := gocni.New(
		gocni.WithPluginDir([]string{s.cniBinDir}),
		gocni.WithPluginConfDir(s.cniConfDir),
	)
	if err != nil {
		return "", err
	}
	if err := cni.Load(gocni.WithLoNetwork, gocni.WithDefaultConf); err != nil {
		return "", err
	}
	result, err := cni.Setup(ctx, containerID, netnsPath)
	if err != nil {
		retryable := isDuplicateAllocationError(err) || isVethExistsError(err) || isBridgeMACError(err)
		if !retryable {
			return "", err
		}
		if isBridgeMACError(err) {
			// Stale bridge with zeroed MAC after container restart; delete it so
			// the plugin can recreate a healthy one.
			_ = exec.CommandContext(ctx, "ip", "link", "delete", "cni0").Run()
		}
		_ = cni.Remove(ctx, containerID, netnsPath)
		result, err = cni.Setup(ctx, containerID, netnsPath)
		if err != nil {
			return "", err
		}
	}
	ip := extractIP(result)
	if ip == "" {
		return "", fmt.Errorf("cni setup returned no usable IP for %s", containerID)
	}
	return ip, nil
}

func (s *DefaultService) resolveNetworkTarget(req containerapi.NetworkRequest) (string, string, error) {
	containerID := strings.TrimSpace(req.ContainerID)
	if containerID == "" {
		return "", "", containerapi.ErrInvalidArgument
	}
	if _, err := os.Stat(s.cniConfDir); err != nil {
		return "", "", fmt.Errorf("cni config dir missing: %s: %w", s.cniConfDir, err)
	}
	if _, err := os.Stat(s.cniBinDir); err != nil {
		return "", "", fmt.Errorf("cni bin dir missing: %s: %w", s.cniBinDir, err)
	}

	netnsPath := strings.TrimSpace(req.JoinTarget.Value)
	if netnsPath == "" {
		if req.JoinTarget.PID == 0 {
			return "", "", fmt.Errorf("task pid not available for %s", containerID)
		}
		netnsPath = networkNamespacePath(req.JoinTarget.PID)
	}
	if _, err := os.Stat(netnsPath); err != nil {
		return "", "", fmt.Errorf("netns not found: %s: %w", netnsPath, err)
	}
	return containerID, netnsPath, nil
}

func extractIP(result *gocni.Result) string {
	if result == nil {
		return ""
	}
	for _, cfg := range result.Interfaces {
		for _, ipCfg := range cfg.IPConfigs {
			if ipCfg.IP != nil {
				ip := ipCfg.IP.String()
				if ip != "" && ip != "127.0.0.1" && ip != "::1" {
					return ip
				}
			}
		}
	}
	return ""
}

func (s *DefaultService) checkNetwork(ctx context.Context, req containerapi.NetworkRequest) error {
	containerID, netnsPath, err := s.resolveNetworkTarget(req)
	if err != nil {
		return err
	}
	cni, err := gocni.New(
		gocni.WithPluginDir([]string{s.cniBinDir}),
		gocni.WithPluginConfDir(s.cniConfDir),
	)
	if err != nil {
		return err
	}
	// Only load the default network config (skip loopback) because go-cni's
	// WithLoNetwork hardcodes cniVersion "0.3.1" which predates the CHECK
	// command (requires >= 0.4.0).
	if err := cni.Load(gocni.WithDefaultConf); err != nil {
		return err
	}
	if err := cni.Check(ctx, containerID, netnsPath); err != nil {
		if isCNICheckUnsupported(err) {
			return containerapi.ErrNotSupported
		}
		return err
	}
	return nil
}

func (s *DefaultService) removeNetwork(ctx context.Context, req containerapi.NetworkRequest) error {
	containerID, netnsPath, err := s.resolveNetworkTarget(req)
	if err != nil {
		return err
	}
	cni, err := gocni.New(
		gocni.WithPluginDir([]string{s.cniBinDir}),
		gocni.WithPluginConfDir(s.cniConfDir),
	)
	if err != nil {
		return err
	}
	if err := cni.Load(gocni.WithLoNetwork, gocni.WithDefaultConf); err != nil {
		return err
	}
	return cni.Remove(ctx, containerID, netnsPath)
}

func networkNamespacePath(pid uint32) string {
	if pid == 0 {
		return ""
	}
	return filepath.Join("/proc", strconv.FormatUint(uint64(pid), 10), "ns", "net")
}

func isDuplicateAllocationError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "duplicate allocation")
}

// isVethExistsError returns true if the CNI setup failed because veth devices
// already exist (e.g. after container restart with stale network state).
func isVethExistsError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "already exists")
}

// isBridgeMACError returns true if the CNI bridge plugin failed because the
// stale cni0 bridge has a zeroed MAC address (common after container restart).
func isBridgeMACError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "set bridge") && strings.Contains(msg, "mac")
}

// isCNICheckUnsupported returns true when the CNI configuration version
// predates the CHECK command (requires spec >= 0.4.0).
func isCNICheckUnsupported(err error) bool {
	return err != nil && strings.Contains(err.Error(), "does not support the CHECK command")
}
