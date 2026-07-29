package userruntime

import "time"

// Runtime is one registered client. Machine details only exist while its
// reverse-RPC connection is online; they are not persisted.
type Runtime struct {
	ID            string    `json:"id"`
	TeamID        string    `json:"team_id"`
	Name          string    `json:"name"`
	Key           string    `json:"key"`
	CreatedAt     time.Time `json:"created_at"`
	Online        bool      `json:"online"`
	WorkspaceBase string    `json:"workspace_base,omitempty"`
	Hostname      string    `json:"hostname,omitempty"`
	OS            string    `json:"os,omitempty"`
	Arch          string    `json:"arch,omitempty"`
	ClientVersion string    `json:"client_version,omitempty"`
	Capabilities  []string  `json:"capabilities,omitempty"`
}

type CreateRuntimeRequest struct {
	Name string `json:"name"`
}

type HandshakeInfo struct {
	Version       int      `json:"version"`
	Hostname      string   `json:"hostname"`
	OS            string   `json:"os"`
	Arch          string   `json:"arch"`
	ClientVersion string   `json:"client_version"`
	WorkspaceBase string   `json:"workspace_base"`
	Capabilities  []string `json:"capabilities"`
}
