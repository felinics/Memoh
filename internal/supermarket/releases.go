package supermarket

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

const maxPackageMetadataBytes = 8 * 1024 * 1024

type ErrorKind string

const (
	ErrorNotFound        ErrorKind = "not_found"
	ErrorUnavailable     ErrorKind = "unavailable"
	ErrorInvalidResponse ErrorKind = "invalid_response"
)

// ProtocolError classifies failures at the Supermarket HTTP and wire boundary.
// Callers map the kind to their public API without exposing upstream details.
type ProtocolError struct {
	Kind   ErrorKind
	Status int
	Op     string
	Err    error
}

func (e *ProtocolError) Error() string {
	if e == nil {
		return "supermarket protocol error"
	}
	if e.Err != nil {
		return e.Op + ": " + e.Err.Error()
	}
	return e.Op
}

func (e *ProtocolError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func ErrorKindOf(err error) ErrorKind {
	var protocolErr *ProtocolError
	if errors.As(err, &protocolErr) {
		return protocolErr.Kind
	}
	return ""
}

// FetchPackageRelease downloads one immutable, digest-verified Package release.
func (c *Client) FetchPackageRelease(
	ctx context.Context,
	registryID, packageID, revision string,
) (SkillPackageDescriptor, error) {
	requestPath := "/api/registries/" + url.PathEscape(registryID) +
		"/packages/" + url.PathEscape(packageID) + "/releases/" + url.PathEscape(revision)
	var release SkillPackageRelease
	if err := c.getImmutableJSON(ctx, requestPath, revision, maxPackageMetadataBytes, &release); err != nil {
		return SkillPackageDescriptor{}, err
	}
	skills := make([]CatalogSkill, 0, len(release.Skills))
	for _, member := range release.Skills {
		member.Artifact.DownloadURL = "/api/artifacts/skill/" + member.Artifact.Digest
		skills = append(skills, CatalogSkill{
			SchemaVersion: member.SchemaVersion, RegistryID: member.RegistryID, PackageID: member.PackageID,
			SkillID: member.SkillID, InstallID: member.InstallID, Name: member.Name,
			Description: member.Description, Author: member.Author, Homepage: member.Homepage,
			Tags: member.Tags, Category: member.Category, CategoryName: member.CategoryName,
			SourceCategory: member.SourceCategory, Files: member.Files, Icon: member.Icon, Artifact: member.Artifact,
		})
	}
	return SkillPackageDescriptor{
		SkillPackageSummary: SkillPackageSummary{
			SchemaVersion: release.SchemaVersion,
			RegistryID:    release.RegistryID, PackageID: release.PackageID,
			Name: release.Name, Description: release.Description, Tags: release.Tags,
			SkillCount: len(release.Skills), Icon: release.Icon,
		},
		Revision: revision,
		Skills:   skills,
	}, nil
}

// DownloadArtifact retrieves and verifies one same-origin immutable Artifact.
func (c *Client) DownloadArtifact(ctx context.Context, artifact ArtifactDownloadDescriptor) ([]byte, error) {
	resp, err := c.GetArtifact(ctx, artifact.DownloadURL, "application/gzip")
	if err != nil {
		kind := ErrorUnavailable
		if errors.Is(err, ErrCrossOrigin) || errors.Is(err, ErrRedirectLimit) {
			kind = ErrorInvalidResponse
		}
		return nil, &ProtocolError{Kind: kind, Op: "download Artifact", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, &ProtocolError{
			Kind: ErrorInvalidResponse, Status: resp.StatusCode, Op: "download Artifact",
			Err: errors.New("artifact was not found"),
		}
	}
	if resp.StatusCode != http.StatusOK {
		kind := ErrorInvalidResponse
		if resp.StatusCode >= http.StatusInternalServerError {
			kind = ErrorUnavailable
		}
		return nil, &ProtocolError{
			Kind: kind, Status: resp.StatusCode, Op: "download Artifact",
			Err: fmt.Errorf("supermarket returned status %d", resp.StatusCode),
		}
	}
	if resp.ContentLength >= 0 && resp.ContentLength != artifact.Size {
		return nil, invalidResponse("download Artifact", errors.New("content length does not match its descriptor"))
	}
	content, err := io.ReadAll(io.LimitReader(resp.Body, artifact.Size+1))
	if err != nil {
		return nil, &ProtocolError{Kind: ErrorUnavailable, Op: "read Artifact", Err: err}
	}
	if int64(len(content)) != artifact.Size {
		return nil, invalidResponse("download Artifact", errors.New("size does not match its descriptor"))
	}
	digest := sha256.Sum256(content)
	if hex.EncodeToString(digest[:]) != artifact.Digest {
		return nil, invalidResponse("download Artifact", errors.New("SHA-256 verification failed"))
	}
	return content, nil
}

func (c *Client) getImmutableJSON(
	ctx context.Context,
	requestPath, revision string,
	limit int64,
	target any,
) error {
	resp, err := c.Get(ctx, requestPath, "application/json")
	if err != nil {
		return &ProtocolError{Kind: ErrorUnavailable, Op: "fetch immutable release", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return &ProtocolError{Kind: ErrorNotFound, Status: resp.StatusCode, Op: "fetch immutable release"}
	}
	if resp.StatusCode != http.StatusOK {
		return &ProtocolError{
			Kind: ErrorUnavailable, Status: resp.StatusCode, Op: "fetch immutable release",
			Err: fmt.Errorf("supermarket returned status %d", resp.StatusCode),
		}
	}
	payload, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return &ProtocolError{Kind: ErrorUnavailable, Op: "read immutable release", Err: err}
	}
	if int64(len(payload)) > limit {
		return invalidResponse("read immutable release", errors.New("release is too large"))
	}
	digest := sha256.Sum256(payload)
	if hex.EncodeToString(digest[:]) != revision {
		return invalidResponse("verify immutable release", errors.New("SHA-256 verification failed"))
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(target); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return invalidResponse("decode immutable release", errors.New("release is malformed"))
	}
	return nil
}

func invalidResponse(op string, err error) error {
	return &ProtocolError{Kind: ErrorInvalidResponse, Op: op, Err: err}
}

func isCanonicalSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}

// IsCanonicalSHA256 reports whether value is a lowercase SHA-256 digest.
func IsCanonicalSHA256(value string) bool {
	return isCanonicalSHA256(value)
}
