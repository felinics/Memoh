package config

import "time"

// WorkspaceDependenciesConfig controls maintenance of the persisted dependency
// catalog and workspace observations. Offline disables automatic upstream work;
// cached definitions and installed workspace commands remain available.
type WorkspaceDependenciesConfig struct {
	Offline                       bool `toml:"offline"`
	CatalogRefreshIntervalSeconds int  `toml:"catalog_refresh_interval_seconds"`
	UpdateCheckIntervalSeconds    int  `toml:"update_check_interval_seconds"`
	ReapIntervalSeconds           int  `toml:"reap_interval_seconds"`
	DiscoveryCacheTTLSeconds      int  `toml:"discovery_cache_ttl_seconds"`
	// ScriptEnv overrides recipe download mirrors. Only NODEJS_MIRROR,
	// UV_RELEASES_URL, NPM_MIRROR, and UV_PYTHON_INSTALL_MIRROR are accepted.
	ScriptEnv map[string]string `toml:"script_env"`
}

func dependencyInterval(seconds int, fallback time.Duration) time.Duration {
	if seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

func (c WorkspaceDependenciesConfig) CatalogRefreshInterval() time.Duration {
	return dependencyInterval(c.CatalogRefreshIntervalSeconds, 10*time.Minute)
}

func (c WorkspaceDependenciesConfig) UpdateCheckInterval() time.Duration {
	return dependencyInterval(c.UpdateCheckIntervalSeconds, 24*time.Hour)
}

func (c WorkspaceDependenciesConfig) ReapInterval() time.Duration {
	return dependencyInterval(c.ReapIntervalSeconds, time.Minute)
}

func (c WorkspaceDependenciesConfig) DiscoveryCacheTTL() time.Duration {
	return dependencyInterval(c.DiscoveryCacheTTLSeconds, 10*time.Minute)
}
