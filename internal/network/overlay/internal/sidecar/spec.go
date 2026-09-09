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
	Image        string
	Cmd          []string
	Env          []string
	Mounts       []ctr.MountSpec
	AddedCaps    []string
	ProxyAddress string
	Details      map[string]any
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
	h.Write([]byte(networkTarget))
	return hex.EncodeToString(h.Sum(nil))[:16]
}
