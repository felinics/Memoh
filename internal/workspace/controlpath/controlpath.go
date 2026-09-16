// Package controlpath names workspace-local kernel coordination files. Durable
// operation receipts and authorization never live in this temporary directory.
package controlpath

import (
	"crypto/sha256"
	"encoding/hex"
	"path"
)

// LinuxRoot is fixed by the workspace image contract, independent of payload
// storage. Tests replace it before creating clients to isolate host fixtures.
var LinuxRoot = "/run/memoh/deps"

// Directory isolates nonstandard data roots without changing the canonical
// /data workspace contract or putting kernel locks back onto its filesystem.
func Directory(metadataRoot string) string {
	if path.Clean(metadataRoot) == "/data/.memoh/deps" {
		return LinuxRoot
	}
	digest := sha256.Sum256([]byte(path.Clean(metadataRoot)))
	return path.Join(LinuxRoot, "workspaces", hex.EncodeToString(digest[:16]))
}

func TransactionLock(home, dependencyID, osName string) string {
	root := path.Dir(home)
	if osName == "linux" {
		root = Directory(root)
	}
	return path.Join(root, ".locks", dependencyID+".lock")
}
