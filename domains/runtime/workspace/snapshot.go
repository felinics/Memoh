package workspace

import (
	"time"

	ctr "github.com/memohai/memoh/domains/runtime/container"
)

const (
	SnapshotSourceManual   = "manual"
	SnapshotSourcePreExec  = "pre_exec"
	SnapshotSourceRollback = "rollback"
)

type VersionInfo struct {
	ID                  string
	Version             int
	SnapshotName        string
	RuntimeSnapshotName string
	DisplayName         string
	CreatedAt           time.Time
}

type SnapshotCreateInfo struct {
	ContainerID         string
	SnapshotName        string
	RuntimeSnapshotName string
	DisplayName         string
	Snapshotter         string
	Version             int
	CreatedAt           time.Time
}

type ManagedSnapshotMeta struct {
	Source                    string
	Version                   *int
	DisplayName               string
	ParentRuntimeSnapshotName string
	Snapshotter               string
	CreatedAt                 time.Time
}

type BotSnapshotData struct {
	ContainerID      string
	Info             ctr.ContainerInfo
	Snapshotter      string
	RuntimeSnapshots []ctr.SnapshotInfo
	ManagedMeta      map[string]ManagedSnapshotMeta
}
