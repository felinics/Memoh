package handlers

import (
	"testing"
	"time"

	ctr "github.com/felinics/memoh/internal/container"
	"github.com/felinics/memoh/internal/workspace"
)

func TestSnapshotLineage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		root      string
		input     []ctr.SnapshotInfo
		wantFound bool
		wantNames []string
	}{
		{
			name: "walk full snapshot ancestry",
			root: "active-3",
			input: []ctr.SnapshotInfo{
				{Name: "active-3", Parent: "version-2"},
				{Name: "version-2", Parent: "version-1"},
				{Name: "version-1", Parent: "sha256:base-layer"},
				{Name: "sha256:base-layer", Parent: ""},
				{Name: "unrelated", Parent: ""},
			},
			wantFound: true,
			wantNames: []string{"active-3", "version-2", "version-1", "sha256:base-layer"},
		},
		{
			name: "root snapshot not found",
			root: "missing",
			input: []ctr.SnapshotInfo{
				{Name: "active-1", Parent: "sha256:base-layer"},
				{Name: "sha256:base-layer", Parent: ""},
			},
			wantFound: false,
			wantNames: nil,
		},
		{
			name: "missing parent keeps known chain",
			root: "active-1",
			input: []ctr.SnapshotInfo{
				{Name: "active-1", Parent: "version-1"},
			},
			wantFound: true,
			wantNames: []string{"active-1"},
		},
		{
			name: "cycle is bounded by visited set",
			root: "a",
			input: []ctr.SnapshotInfo{
				{Name: "a", Parent: "b"},
				{Name: "b", Parent: "a"},
			},
			wantFound: true,
			wantNames: []string{"a", "b"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, found := snapshotLineage(tt.root, tt.input)
			if found != tt.wantFound {
				t.Fatalf("found = %v, want %v", found, tt.wantFound)
			}
			if !found {
				return
			}
			if len(got) != len(tt.wantNames) {
				t.Fatalf("len(got) = %d, want %d", len(got), len(tt.wantNames))
			}
			for i := range tt.wantNames {
				if got[i].Name != tt.wantNames[i] {
					t.Fatalf("got[%d].Name = %q, want %q", i, got[i].Name, tt.wantNames[i])
				}
			}
		})
	}
}

func TestBuildSnapshotListResponseAllowsDockerContainerWithoutSnapshotRoot(t *testing.T) {
	t.Parallel()

	resp, ok := buildSnapshotListResponse(&workspace.BotSnapshotData{
		Snapshotter: "docker",
		Info: ctr.ContainerInfo{
			StorageRef: ctr.StorageRef{
				Driver: "docker",
				Key:    "docker-container-id",
				Kind:   "container",
			},
		},
	})
	if !ok {
		t.Fatal("buildSnapshotListResponse returned ok=false")
	}
	if resp.Snapshotter != "docker" {
		t.Fatalf("Snapshotter = %q, want docker", resp.Snapshotter)
	}
	if len(resp.Snapshots) != 0 {
		t.Fatalf("len(Snapshots) = %d, want 0", len(resp.Snapshots))
	}
}

func TestBuildSnapshotListResponseIncludesDockerManagedSnapshotWithoutRoot(t *testing.T) {
	t.Parallel()

	version := 3
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	resp, ok := buildSnapshotListResponse(&workspace.BotSnapshotData{
		Snapshotter: "docker",
		Info: ctr.ContainerInfo{
			StorageRef: ctr.StorageRef{
				Driver: "docker",
				Key:    "docker-container-id",
				Kind:   "container",
			},
		},
		RuntimeSnapshots: []ctr.SnapshotInfo{
			{
				Name:    "workspace-snapshot-1",
				Parent:  "workspace-active-0",
				Kind:    "committed",
				Created: created,
				Updated: created,
			},
		},
		ManagedMeta: map[string]workspace.ManagedSnapshotMeta{
			"workspace-snapshot-1": {
				Source:      workspace.SnapshotSourceManual,
				Version:     &version,
				DisplayName: "Manual checkpoint",
				Snapshotter: "docker",
				CreatedAt:   created,
			},
		},
	})
	if !ok {
		t.Fatal("buildSnapshotListResponse returned ok=false")
	}
	if len(resp.Snapshots) != 1 {
		t.Fatalf("len(Snapshots) = %d, want 1", len(resp.Snapshots))
	}

	got := resp.Snapshots[0]
	if got.Name != "Manual checkpoint" {
		t.Fatalf("Name = %q, want Manual checkpoint", got.Name)
	}
	if got.RuntimeName != "workspace-snapshot-1" {
		t.Fatalf("RuntimeName = %q, want workspace-snapshot-1", got.RuntimeName)
	}
	if got.Snapshotter != "docker" {
		t.Fatalf("Snapshotter = %q, want docker", got.Snapshotter)
	}
	if !got.Managed {
		t.Fatal("Managed = false, want true")
	}
	if got.Version == nil || *got.Version != version {
		t.Fatalf("Version = %v, want %d", got.Version, version)
	}
	if got.Source != workspace.SnapshotSourceManual {
		t.Fatalf("Source = %q, want %q", got.Source, workspace.SnapshotSourceManual)
	}
}

func TestBuildSnapshotListResponseRequiresContainerdSnapshotRoot(t *testing.T) {
	t.Parallel()

	_, ok := buildSnapshotListResponse(&workspace.BotSnapshotData{
		Snapshotter: "overlayfs",
		Info: ctr.ContainerInfo{
			StorageRef: ctr.StorageRef{
				Driver: "overlayfs",
				Key:    "missing-active",
				Kind:   "active",
			},
		},
		RuntimeSnapshots: []ctr.SnapshotInfo{
			{Name: "other-active", Parent: ""},
		},
	})
	if ok {
		t.Fatal("buildSnapshotListResponse returned ok=true, want false")
	}
}

func TestBuildSnapshotListResponseSynthesizesArchiveManagedSnapshot(t *testing.T) {
	t.Parallel()

	version := 1
	created := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	resp, ok := buildSnapshotListResponse(&workspace.BotSnapshotData{
		Snapshotter: "local",
		Info: ctr.ContainerInfo{
			StorageRef: ctr.StorageRef{
				Driver: "local",
				Key:    "/tmp/memoh-workspace",
				Kind:   "directory",
			},
		},
		ManagedMeta: map[string]workspace.ManagedSnapshotMeta{
			"archive:bot-id/1.tar.gz": {
				Source:                    workspace.SnapshotSourceManual,
				Version:                   &version,
				ParentRuntimeSnapshotName: "/tmp/memoh-workspace",
				Snapshotter:               "archive",
				CreatedAt:                 created,
			},
		},
	})
	if !ok {
		t.Fatal("buildSnapshotListResponse returned ok=false")
	}
	if len(resp.Snapshots) != 1 {
		t.Fatalf("len(Snapshots) = %d, want 1", len(resp.Snapshots))
	}

	got := resp.Snapshots[0]
	if got.Snapshotter != "archive" {
		t.Fatalf("Snapshotter = %q, want archive", got.Snapshotter)
	}
	if got.Kind != "archive" {
		t.Fatalf("Kind = %q, want archive", got.Kind)
	}
	if got.Parent != "/tmp/memoh-workspace" {
		t.Fatalf("Parent = %q, want /tmp/memoh-workspace", got.Parent)
	}
	if !got.Managed {
		t.Fatal("Managed = false, want true")
	}
	if !got.CreatedAt.Equal(created) {
		t.Fatalf("CreatedAt = %s, want %s", got.CreatedAt, created)
	}
}
