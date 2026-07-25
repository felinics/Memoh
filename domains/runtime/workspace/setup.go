package workspace

import ctr "github.com/memohai/memoh/domains/runtime/container"

type ContainerSetupEvent struct {
	Type             string
	Image            string
	Message          string
	Layers           []ctr.LayerStatus
	ContainerID      string
	WorkspaceBackend string
	RuntimeBackend   string
	ContainerPath    string
	CDIDevices       []string
	Started          bool
	DataRestored     bool
	HasPreservedData bool
}

type ContainerSetupProgress func(ContainerSetupEvent)
