package container

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const (
	systemdResolvConf  = "/run/systemd/resolve/resolv.conf"
	fallbackResolv     = "nameserver 1.1.1.1\nnameserver 8.8.8.8\n"
	fallbackResolvPerm = 0o644
)

// ResolveConfSource returns a host path to mount as /etc/resolv.conf.
func ResolveConfSource(dataDir string) (string, error) {
	return resolveConfSource(dataDir, systemdResolvConf)
}

func resolveConfSource(dataDir, preferredPath string) (string, error) {
	if strings.TrimSpace(dataDir) == "" {
		return "", ErrInvalidArgument
	}
	if strings.TrimSpace(preferredPath) != "" {
		if _, err := os.Stat(preferredPath); err == nil {
			return preferredPath, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}

	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return "", err
	}
	fallbackPath := filepath.Join(dataDir, "resolv.conf")
	if _, err := os.Stat(fallbackPath); err == nil {
		if err := os.Chmod(fallbackPath, fallbackResolvPerm); err != nil {
			return "", err
		}
		return fallbackPath, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.WriteFile(fallbackPath, []byte(fallbackResolv), fallbackResolvPerm); err != nil {
		return "", err
	}
	return fallbackPath, nil
}

// TimezoneSpec returns environment variables that propagate the host timezone.
func TimezoneSpec() ([]MountSpec, []string) {
	var env []string
	if tz := detectTimezone(); tz != "" {
		env = append(env, "TZ="+tz)
	}
	return nil, env
}

func detectTimezone() string {
	if tz := strings.TrimSpace(os.Getenv("TZ")); tz != "" {
		return tz
	}
	if data, err := os.ReadFile("/etc/timezone"); err == nil {
		if tz := strings.TrimSpace(string(data)); tz != "" {
			return tz
		}
	}
	if target, err := filepath.EvalSymlinks("/etc/localtime"); err == nil {
		const zoneinfoPrefix = "/usr/share/zoneinfo/"
		if strings.HasPrefix(target, zoneinfoPrefix) {
			return strings.TrimPrefix(target, zoneinfoPrefix)
		}
	}
	return ""
}
