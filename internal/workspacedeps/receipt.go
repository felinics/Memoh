package workspacedeps

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/workspace/bridge"
	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

// OperationReceipt freezes only the non-secret values needed to finish an
// accepted mutation after a Server or stream failure. Result and completion
// are read from files written by the workspace process under its kernel lock.
type OperationReceipt struct {
	ID                 string         `json:"id"`
	DependencyID       string         `json:"dependency_id"`
	Action             catalog.Action `json:"action"`
	SourceURL          string         `json:"source_url,omitempty"`
	RegistryID         string         `json:"registry_id,omitempty"`
	DefinitionRevision string         `json:"definition_revision,omitempty"`
	ManifestDigest     string         `json:"manifest_digest,omitempty"`
	RequestedVersion   string         `json:"requested_version,omitempty"`
	StartedAt          time.Time      `json:"started_at"`
	Previous           *State         `json:"previous,omitempty"`
	Directory          string         `json:"-"`
	Result             Result         `json:"-"`
	ExitCode           int            `json:"-"`
	Completed          bool           `json:"-"`
}

func operationRoot(home, depID string) string {
	return path.Join(path.Dir(home), ".operations", depID)
}

func prepareReceipt(ctx context.Context, client *bridge.Client, spec RunSpec) (*OperationReceipt, error) {
	receipt := &OperationReceipt{}
	if spec.Receipt != nil {
		*receipt = *spec.Receipt
	}
	if receipt.ID == "" {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return nil, err
		}
		receipt.ID = hex.EncodeToString(nonce[:])
	} else if !validReceiptID(receipt.ID) {
		return nil, errors.New("workspacedeps: invalid operation id")
	}
	receipt.DependencyID, receipt.Action = spec.DepID, spec.Action
	receipt.RequestedVersion = spec.Version
	if receipt.StartedAt.IsZero() {
		receipt.StartedAt = time.Now().UTC()
	}
	receipt.Directory = path.Join(operationRoot(spec.Home, spec.DepID), receipt.ID)
	receipt.Completed, receipt.ExitCode, receipt.Result = false, 0, Result{}
	if err := client.Mkdir(ctx, path.Dir(receipt.Directory)); err != nil {
		return nil, fmt.Errorf("workspacedeps: create operation receipt root: %w", err)
	}
	// An accepted operation executes once. Repeating its ID must recover the
	// original receipt, never overwrite an earlier completion or active run.
	created, err := client.ExecWithOptions(ctx, "mkdir -- "+shellQuote(receipt.Directory), defaultWorkDir, int32(cleanupTimeout/time.Second), nil, bridge.ExecOptions{})
	if err != nil {
		return nil, err
	}
	if created.ExitCode != 0 {
		return nil, fmt.Errorf("workspacedeps: claim operation receipt directory (existing operations must be reconciled): %s", strings.TrimSpace(created.Stderr))
	}
	metadata, err := json.Marshal(receipt)
	if err != nil {
		return nil, err
	}
	if err := client.WriteFile(ctx, path.Join(receipt.Directory, "metadata.json"), metadata); err != nil {
		_ = CleanupReceipt(ctx, client, receipt)
		return nil, fmt.Errorf("workspacedeps: freeze operation receipt: %w", err)
	}
	return receipt, nil
}

func validReceiptID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil && strings.ToLower(id) == id
}

// CleanupReceipt removes only this operation's unique directory after its
// result has been committed to state.json and the database. It never removes
// a kernel lock or the shared current pointer; a later owner may have changed
// either while a previous Server was completing its own bookkeeping.
func CleanupReceipt(ctx context.Context, client *bridge.Client, receipt *OperationReceipt) error {
	if receipt == nil {
		return nil
	}
	if !validReceiptID(receipt.ID) || path.Base(receipt.Directory) != receipt.ID || path.Base(path.Dir(receipt.Directory)) != receipt.DependencyID || path.Base(path.Dir(path.Dir(receipt.Directory))) != ".operations" {
		return errors.New("workspacedeps: invalid receipt directory")
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	result, err := client.ExecWithOptions(cleanupCtx, "rm -rf -- "+shellQuote(receipt.Directory), "", int32(cleanupTimeout/time.Second), nil, bridge.ExecOptions{})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("workspacedeps: receipt cleanup exited %d", result.ExitCode)
	}
	return nil
}

func readReceiptFile(ctx context.Context, client *bridge.Client, filename string, limit int64) ([]byte, error) {
	reader, err := client.ReadRaw(ctx, filename)
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err == nil && int64(len(data)) > limit {
		err = errors.New("operation receipt exceeds byte budget")
	}
	return data, err
}

func readOperationReceipt(ctx context.Context, client *bridge.Client, home, depID string) (*OperationReceipt, error) {
	root := operationRoot(home, depID)
	current, err := readReceiptFile(ctx, client, path.Join(root, "current"), 128)
	if errors.Is(err, bridge.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(string(current))
	if !validReceiptID(id) {
		return nil, errors.New("invalid current operation receipt")
	}
	directory := path.Join(root, id)
	metadata, err := readReceiptFile(ctx, client, path.Join(directory, "metadata.json"), 1024*1024)
	if errors.Is(err, bridge.ErrNotFound) {
		return nil, nil // Already acknowledged; the current pointer is harmless.
	}
	if err != nil {
		return nil, err
	}
	var receipt OperationReceipt
	if err := json.Unmarshal(metadata, &receipt); err != nil {
		return nil, err
	}
	if receipt.ID != id || receipt.DependencyID != depID {
		return nil, errors.New("operation receipt identity mismatch")
	}
	receipt.Directory = directory
	exit, err := readReceiptFile(ctx, client, path.Join(directory, "exit-code"), 64)
	if errors.Is(err, bridge.ErrNotFound) {
		return &receipt, nil
	}
	if err != nil {
		return nil, err
	}
	code, err := strconv.Atoi(strings.TrimSpace(string(exit)))
	if err != nil || code < 0 || code > 255 {
		return nil, errors.New("invalid operation exit status")
	}
	receipt.Completed, receipt.ExitCode = true, code
	if code == 0 {
		receipt.Result, err = readResult(ctx, client, path.Join(directory, "result.json"))
		if err != nil {
			return nil, err
		}
	}
	return &receipt, nil
}

// ReadOperationReceipt reads the currently published receipt. A workspace
// command that finalizes its result must hold the kernel lock and recheck the
// receipt identity before publishing state or shims.
func ReadOperationReceipt(ctx context.Context, client *bridge.Client, home, depID string) (*OperationReceipt, error) {
	return readOperationReceipt(ctx, client, home, depID)
}
