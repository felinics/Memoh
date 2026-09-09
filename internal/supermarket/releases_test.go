package supermarket

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestFetchPackageReleaseHydratesArtifactURLs(t *testing.T) {
	release := SkillPackageRelease{
		SchemaVersion: "1", RegistryID: "openai", PackageID: "docs",
		Name: "Docs", Description: "Docs", Tags: []string{},
		Skills: []SkillPackageReleaseSkill{{
			SchemaVersion: "1", RegistryID: "openai", PackageID: "docs", SkillID: "write-docs",
			InstallID: "openai+docs+write-docs", Name: "Write docs", Description: "Write docs",
			Author: Author{Name: "OpenAI", Email: "support@example.com"}, Tags: []string{}, Files: []string{"SKILL.md"},
			Artifact: SkillArtifact{
				Format: "memoh_skill_v1", Digest: strings.Repeat("b", 64), Size: 10,
				UncompressedSize: 10, ArchiveSize: 512, FileCount: 1, ContentType: "application/gzip",
			},
		}},
	}
	payload := mustJSONBytes(t, release)
	revision := digestText(payload)
	client := NewClient("https://supermarket.example", &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return protocolTestResponse(req, http.StatusOK, payload), nil
	})})

	pkg, err := client.FetchPackageRelease(context.Background(), "openai", "docs", revision)
	if err != nil {
		t.Fatalf("FetchPackageRelease() error = %v", err)
	}
	if pkg.Revision != revision || len(pkg.Skills) != 1 || pkg.SkillCount != 1 {
		t.Fatalf("Package = %+v", pkg)
	}
	wantURL := "/api/artifacts/skill/" + release.Skills[0].Artifact.Digest
	if pkg.Skills[0].Artifact.DownloadURL != wantURL {
		t.Fatalf("download URL = %q, want %q", pkg.Skills[0].Artifact.DownloadURL, wantURL)
	}
}

func TestDownloadArtifactVerifiesDescriptor(t *testing.T) {
	content := []byte("artifact")
	digest := digestText(content)
	client := NewClient("https://supermarket.example", &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return protocolTestResponse(req, http.StatusOK, content), nil
	})})

	got, err := client.DownloadArtifact(context.Background(), ArtifactDownloadDescriptor{
		Digest: digest, Size: int64(len(content)), DownloadURL: "/api/artifacts/skill/" + digest,
	})
	if err != nil || string(got) != string(content) {
		t.Fatalf("DownloadArtifact() = %q, %v", got, err)
	}
	_, err = client.DownloadArtifact(context.Background(), ArtifactDownloadDescriptor{
		Digest: strings.Repeat("0", 64), Size: int64(len(content)), DownloadURL: "/api/artifacts/skill/invalid",
	})
	if ErrorKindOf(err) != ErrorInvalidResponse {
		t.Fatalf("digest mismatch kind = %q, error = %v", ErrorKindOf(err), err)
	}
}

func mustJSONBytes(t *testing.T, value any) []byte {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return payload
}

func digestText(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func protocolTestResponse(req *http.Request, status int, body []byte) *http.Response {
	return &http.Response{
		StatusCode:    status,
		Body:          io.NopCloser(strings.NewReader(string(body))),
		Request:       req,
		Header:        make(http.Header),
		ContentLength: int64(len(body)),
	}
}
