package supermarket

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

var installerErrors = []error{ErrAppNotFound, ErrRegistryUnavailable, ErrAppInvalid, ErrInstallFailed}

// requireOnlyInstallerError checks that err matches want and none of the
// other Installer errors.
func requireOnlyInstallerError(t *testing.T, err, want error) {
	t.Helper()
	for _, sentinel := range installerErrors {
		if errors.Is(err, sentinel) != (sentinel == want) { //nolint:errorlint // Sentinels are compared by identity.
			t.Fatalf("error = %v, want only %v", err, want)
		}
	}
}

func answeringInstaller(status int, body []byte) *Installer {
	return &Installer{client: NewClient("https://supermarket.example", &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return protocolTestResponse(req, status, body), nil
	})})}
}

func TestFetchReleaseReportsWhyTheRegistryCouldNotServeIt(t *testing.T) {
	revision := strings.Repeat("a", 64)
	cases := []struct {
		name      string
		installer *Installer
		want      error
	}{
		{"the registry does not publish it", answeringInstaller(http.StatusNotFound, nil), ErrAppNotFound},
		{"the registry is down", answeringInstaller(http.StatusServiceUnavailable, nil), ErrRegistryUnavailable},
		{"the release does not match its revision", answeringInstaller(http.StatusOK, []byte(`{}`)), ErrAppInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.installer.FetchRelease(context.Background(), "openai", "docs", revision)
			requireOnlyInstallerError(t, err, tc.want)
		})
	}
}

func TestPrepareSkillSeparatesAnUnavailableRegistryFromAnInvalidArtifact(t *testing.T) {
	digest := strings.Repeat("b", 64)
	skill := CatalogSkill{Artifact: SkillArtifact{Digest: digest, Size: 10, DownloadURL: "/api/artifacts/skill/" + digest}}
	cases := []struct {
		name   string
		status int
		want   error
	}{
		{"the registry fails to serve the Artifact", http.StatusServiceUnavailable, ErrRegistryUnavailable},
		{"the registry has no such Artifact", http.StatusNotFound, ErrAppInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := answeringInstaller(tc.status, nil).prepareSkill(context.Background(), skill)
			requireOnlyInstallerError(t, err, tc.want)
			if ErrorKindOf(err) == "" {
				t.Fatalf("error = %v lost the protocol error it was caused by", err)
			}
		})
	}
}

func TestPublishSkillsWithoutAWorkspaceReportsAFailedInstall(t *testing.T) {
	_, err := (&Installer{}).PublishSkills(context.Background(), "bot", AppDescriptor{}, "")
	requireOnlyInstallerError(t, err, ErrInstallFailed)
}
