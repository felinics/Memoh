package client

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRuntimeLeaseRejectsPreplantedRuntimeParentSymlink(t *testing.T) {
	client, server := newRecordingBridgeClient(t)
	server.mu.Lock()
	server.fs[runtimeStateRoot] = recordingFSNode{isSymlink: true, modTime: time.Now()}
	server.mu.Unlock()

	_, err := prepareRuntimeLease(context.Background(), client, processOptions{
		BotID:     "bot-1",
		AgentID:   "acp",
		SetupMode: SetupModeAPIKey,
	})
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("prepare RuntimeLease error = %v, want pre-planted symlink rejection", err)
	}
}

func TestRuntimeLeaseRejectsPreplantedCacheSymlink(t *testing.T) {
	client, server := newRecordingBridgeClient(t)
	server.mu.Lock()
	server.fs[runtimeCacheRoot] = recordingFSNode{isSymlink: true, modTime: time.Now()}
	server.mu.Unlock()

	_, err := prepareRuntimeLease(context.Background(), client, processOptions{
		BotID:     "bot-1",
		AgentID:   "acp",
		SetupMode: SetupModeAPIKey,
	})
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("prepare RuntimeLease error = %v, want pre-planted cache symlink rejection", err)
	}

	server.mu.Lock()
	defer server.mu.Unlock()
	for filePath := range server.fs {
		if strings.HasPrefix(filePath, runtimeCacheRoot+"/") {
			t.Fatalf("directory %q was created through the pre-planted cache symlink", filePath)
		}
		if strings.HasPrefix(filePath, runtimeStateRoot+"/custom-agent/") {
			t.Fatalf("failed startup left runtime state behind: %s", filePath)
		}
	}
}
