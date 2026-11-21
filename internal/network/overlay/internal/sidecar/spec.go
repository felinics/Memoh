package sidecar

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"

	ctr "github.com/felinics/memoh/internal/container"
)

const (
	LabelManaged    = "memoh.network.managed"
	LabelBotID      = "memoh.network.bot_id"
	LabelKind       = "memoh.network.provider_kind"
	LabelConfigHash = "memoh.network.config_hash"
)

// Spec describes the official client sidecar container to run in a workspace
// network stack.
type Spec struct {
	Image     string
	Cmd       []string
	Env       []string
	Mounts    []ctr.MountSpec
	AddedCaps []string
	// ResolvConfPath is the host path of the sidecar's own writable
	// /etc/resolv.conf. The manager refills it from the host resolver config
	// before every task start; the client may rewrite it while running. It
	// must lie outside every mounted directory: clients write their own files
	// into their state directory, and netbird keeps a resolv.conf there.
	ResolvConfPath string
	ProxyAddress   string
	Details        map[string]any
}

// ConfigHash computes a stable fingerprint of the sidecar spec so callers can
// recreate the sidecar when config or network target changes.
func ConfigHash(spec Spec, networkTarget string) string {
	h := sha256.New()
	h.Write([]byte(spec.Image))
	h.Write([]byte{0})
	for _, c := range spec.Cmd {
		h.Write([]byte(c))
		h.Write([]byte{0})
	}
	env := append([]string(nil), spec.Env...)
	sort.Strings(env)
	for _, e := range env {
		h.Write([]byte(e))
		h.Write([]byte{0})
	}
	for _, cap := range spec.AddedCaps {
		h.Write([]byte(cap))
		h.Write([]byte{0})
	}
	h.Write([]byte{0})
	for _, m := range spec.Mounts {
		h.Write([]byte(m.Type))
		h.Write([]byte{0})
		h.Write([]byte(m.Source))
		h.Write([]byte{0})
		h.Write([]byte(m.Destination))
		h.Write([]byte{0})
		for _, o := range m.Options {
			h.Write([]byte(o))
			h.Write([]byte{0})
		}
	}
	h.Write([]byte(spec.ResolvConfPath))
	h.Write([]byte{0})
	h.Write([]byte(networkTarget))
	return hex.EncodeToString(h.Sum(nil))[:16]
}
