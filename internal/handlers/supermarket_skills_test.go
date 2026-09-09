package handlers

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/skillpackages"
	supermarketclient "github.com/felinics/memoh/internal/supermarket"
	"github.com/felinics/memoh/internal/workspace"
)

const validSkillArtifactContent = "---\nname: skill\ndescription: Demo\n---\n\n# Demo\n"

const validSkillArtifactUncompressedSize = int64(len(validSkillArtifactContent))

func TestSupermarketSkillRoutesUseRegistryCatalogOnly(t *testing.T) {
	var upstreamRequestURI string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamRequestURI = r.URL.RequestURI()
		w.Header().Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		_, _ = w.Write([]byte(`{"data":[],"total":0,"page":1,"limit":50}`))
	}))
	t.Cleanup(upstream.Close)

	handler := &SupermarketHandler{
		upstream: supermarketclient.NewClient(upstream.URL, upstream.Client()),
		logger:   slog.New(slog.DiscardHandler),
	}
	e := echo.New()
	handler.Register(e)

	req := httptest.NewRequest(http.MethodGet, "/supermarket/skills?registry=memoh&limit=50", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /supermarket/skills status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if upstreamRequestURI != "/api/skills?registry=memoh&limit=50" {
		t.Fatalf("upstream request URI = %q, want canonical Skill collection", upstreamRequestURI)
	}
}

func TestSupermarketPackageRoutesUsePackageCatalog(t *testing.T) {
	var upstreamRequestURI string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamRequestURI = r.URL.RequestURI()
		w.Header().Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(upstream.Close)

	handler := &SupermarketHandler{
		upstream: supermarketclient.NewClient(upstream.URL, upstream.Client()), logger: slog.New(slog.DiscardHandler),
	}
	e := echo.New()
	handler.Register(e)

	tests := []struct {
		path string
		want string
	}{
		{"/supermarket/packages?registry=memoh&limit=50", "/api/packages?registry=memoh&limit=50"},
		{"/supermarket/registries/memoh/packages?q=web", "/api/registries/memoh/packages?q=web"},
		{"/supermarket/registries/memoh/packages/web-tools", "/api/registries/memoh/packages/web-tools"},
	}
	for _, test := range tests {
		req := httptest.NewRequest(http.MethodGet, test.path, nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200: %s", test.path, rec.Code, rec.Body.String())
		}
		if upstreamRequestURI != test.want {
			t.Fatalf("GET %s upstream = %q, want %q", test.path, upstreamRequestURI, test.want)
		}
	}
}

func TestGetRegistryPackageReleaseReturnsPinnedDescriptor(t *testing.T) {
	pkg := validRegistryPackageDescriptor()
	release := registryPackageReleaseBytes(t, pkg)
	digest := sha256.Sum256(release)
	revision := hex.EncodeToString(digest[:])
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := "/api/registries/registry/packages/package/releases/" + revision
		if r.URL.Path != want {
			t.Fatalf("upstream path = %q, want %q", r.URL.Path, want)
		}
		w.Header().Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		_, _ = w.Write(release)
	}))
	t.Cleanup(upstream.Close)

	handler := &SupermarketHandler{
		upstream: supermarketclient.NewClient(upstream.URL, upstream.Client()),
		logger:   slog.New(slog.DiscardHandler),
	}
	e := echo.New()
	handler.Register(e)
	req := httptest.NewRequest(http.MethodGet, "/supermarket/registries/registry/packages/package/releases/"+revision, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("release status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got SupermarketSkillPackageDescriptor
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Revision != revision || len(got.Skills) != len(pkg.Skills) {
		t.Fatalf("release = %+v, want revision %s with %d Skills", got, revision, len(pkg.Skills))
	}
}

func TestInstallRegistryPackagePublishesMembersInOneMutation(t *testing.T) {
	env := newSkillsTestEnv(t)
	manager := workspace.NewManager(
		slog.Default(), nil, nil, config.WorkspaceConfig{DataRoot: env.dataRoot}, "", nil,
	)
	artifact := validSkillArtifact(t)
	digest := sha256.Sum256(artifact)
	pkg := validRegistryPackageDescriptor()
	for index := range pkg.Skills {
		pkg.Skills[index].Artifact.Digest = hex.EncodeToString(digest[:])
		pkg.Skills[index].Artifact.Size = int64(len(artifact))
	}
	release := registryPackageReleaseBytes(t, pkg)
	revision := sha256.Sum256(release)
	pkg.Revision = hex.EncodeToString(revision[:])
	obsoletePath := "/data/skills/registry/package/obsolete/SKILL.md"
	env.writeSkillFile(t, obsoletePath, managedSkillRaw("obsolete", "Obsolete"))
	handler := &SupermarketHandler{
		upstream: supermarketclient.NewClient("https://supermarket.example", &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if strings.HasPrefix(req.URL.Path, "/api/artifacts/skill/") {
				return testHTTPResponse(req, http.StatusOK, artifact), nil
			}
			wantReleasePath := "/api/registries/registry/packages/package/releases/" + pkg.Revision
			if req.URL.Path != wantReleasePath {
				t.Fatalf("unexpected upstream request path %q, want %q", req.URL.Path, wantReleasePath)
			}
			return testHTTPResponse(req, http.StatusOK, release), nil
		})}),
	}

	packageService := skillpackages.NewService(&directPackageStore{})
	service := supermarketclient.NewInstaller(handler.upstream, packageService, manager, slog.New(slog.DiscardHandler))
	result, err := service.InstallPackage(context.Background(), env.botID, supermarketclient.InstallPackageRequest{
		RegistryID: pkg.RegistryID, PackageID: pkg.PackageID, Revision: pkg.Revision,
	})
	if err != nil || !result.OK || len(result.Skills) != len(pkg.Skills) {
		t.Fatalf("installRegistryPackage() result=%+v error=%v", result, err)
	}
	if _, err := os.Stat(env.localPath(obsoletePath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("obsolete Package member survived replacement: %v", err)
	}
	for _, skillID := range []string{"skill", "second"} {
		installedPath := "/data/skills/registry/package/" + skillID + "/SKILL.md"
		content, err := os.ReadFile(env.localPath(installedPath))
		if err != nil || string(content) != validSkillArtifactContent {
			t.Fatalf("installed Package member %q content=%q error=%v", skillID, content, err)
		}
	}
	if _, err := os.Stat(env.localPath("/data/skills/.staging/registry/package")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Package staging was not cleaned: %v", err)
	}
}

type directPackageStore struct {
	skillpackages.Store
}

func (*directPackageStore) GetBotSkillPackageInstallation(context.Context, dbsqlc.GetBotSkillPackageInstallationParams) (dbsqlc.BotSkillPackageInstallation, error) {
	return dbsqlc.BotSkillPackageInstallation{}, pgx.ErrNoRows
}

func (*directPackageStore) UpsertBotSkillPackageInstallation(_ context.Context, arg dbsqlc.UpsertBotSkillPackageInstallationParams) (dbsqlc.BotSkillPackageInstallation, error) {
	return dbsqlc.BotSkillPackageInstallation{
		ID:    pgtype.UUID{Bytes: [16]byte{9, 9, 9, 9, 9, 9, 0x49, 9, 0x89, 9, 9, 9, 9, 9, 9, 9}, Valid: true},
		BotID: arg.BotID, WorkspaceTargetID: arg.WorkspaceTargetID,
		RegistryID: arg.RegistryID, PackageID: arg.PackageID, Revision: arg.Revision,
	}, nil
}

type uninstallPackageStore struct {
	skillpackages.Store
	row       dbsqlc.BotSkillPackageInstallation
	deleteErr error
}

func (s *uninstallPackageStore) GetBotSkillPackageInstallationByID(context.Context, dbsqlc.GetBotSkillPackageInstallationByIDParams) (dbsqlc.BotSkillPackageInstallation, error) {
	return s.row, nil
}

func (s *uninstallPackageStore) DeleteBotSkillPackageInstallation(context.Context, dbsqlc.DeleteBotSkillPackageInstallationParams) (dbsqlc.BotSkillPackageInstallation, error) {
	return s.row, s.deleteErr
}

func TestUninstallRegistryPackageRemovesDirectoryAndRollsBackOnDatabaseFailure(t *testing.T) {
	for _, test := range []struct {
		name      string
		deleteErr error
		wantFile  bool
	}{
		{name: "success"},
		{name: "database failure", deleteErr: errors.New("injected database failure"), wantFile: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			env := newSkillsTestEnv(t)
			manager := workspace.NewManager(
				slog.Default(), nil, nil, config.WorkspaceConfig{DataRoot: env.dataRoot}, "", nil,
			)
			packageFile := "/data/skills/openai/documents/pdf/SKILL.md"
			env.writeSkillFile(t, packageFile, validSkillArtifactContent)
			botID, err := db.ParseUUID(env.botID)
			if err != nil {
				t.Fatalf("parse bot ID: %v", err)
			}
			installationID := pgtype.UUID{Bytes: [16]byte{9, 9, 9, 9, 9, 9, 0x49, 9, 0x89, 9, 9, 9, 9, 9, 9, 9}, Valid: true}
			store := &uninstallPackageStore{
				row: dbsqlc.BotSkillPackageInstallation{
					ID: installationID, BotID: botID, WorkspaceTargetID: "native", RegistryID: "openai", PackageID: "documents",
					Revision: strings.Repeat("a", 64),
				},
				deleteErr: test.deleteErr,
			}
			installer := supermarketclient.NewInstaller(
				nil,
				skillpackages.NewService(store),
				manager,
				slog.New(slog.DiscardHandler),
			)

			result, uninstallErr := installer.UninstallPackage(context.Background(), env.botID, installationID.String())
			if test.deleteErr == nil {
				if uninstallErr != nil || !result.OK {
					t.Fatalf("UninstallPackage() result=%+v error=%v", result, uninstallErr)
				}
			} else if !errors.Is(uninstallErr, test.deleteErr) {
				t.Fatalf("UninstallPackage() error=%v, want %v", uninstallErr, test.deleteErr)
			}
			_, statErr := os.Stat(env.localPath(packageFile))
			if test.wantFile {
				if statErr != nil {
					t.Fatalf("Package file was not restored: %v", statErr)
				}
			} else if !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("Package file still exists after uninstall: %v", statErr)
			}
		})
	}
}

func TestProxySkillIconVerifiesDigestAndHeaders(t *testing.T) {
	content := []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)
	digest := sha256.Sum256(content)
	digestText := hex.EncodeToString(digest[:])
	handler := &SupermarketHandler{
		upstream: supermarketclient.NewClient("https://supermarket.example", &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			header := make(http.Header)
			header.Set("Content-Type", "image/svg+xml")
			header.Set("Cache-Control", "public, max-age=31536000, immutable")
			header.Set("ETag", `"`+digestText+`"`)
			return &http.Response{
				StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(content)),
				ContentLength: int64(len(content)), Request: req, Header: header,
			}, nil
		})}),
	}
	e := echo.New()
	request := httptest.NewRequest(http.MethodGet, "/supermarket/artifacts/icon/"+digestText, nil)
	recorder := httptest.NewRecorder()
	if err := handler.proxySkillIcon(e.NewContext(request, recorder), digestText); err != nil {
		t.Fatalf("proxySkillIcon() error = %v", err)
	}
	if recorder.Code != http.StatusOK || !bytes.Equal(recorder.Body.Bytes(), content) {
		t.Fatalf("response = %d %q", recorder.Code, recorder.Body.Bytes())
	}
	if recorder.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("immutable cache header was not forwarded")
	}

	handler.upstream = supermarketclient.NewClient("https://supermarket.example", &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("wrong")),
			ContentLength: 5, Request: req, Header: http.Header{"Content-Type": []string{"image/svg+xml"}},
		}, nil
	})})
	if err := handler.proxySkillIcon(e.NewContext(request, httptest.NewRecorder()), digestText); err == nil {
		t.Fatal("proxySkillIcon() accepted content with the wrong digest")
	}
}

func TestProxySkillIconOverridesUpstreamSecurityHeaders(t *testing.T) {
	content := []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)
	digest := sha256.Sum256(content)
	digestText := hex.EncodeToString(digest[:])
	handler := &SupermarketHandler{
		upstream: supermarketclient.NewClient("https://supermarket.example", &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			header := make(http.Header)
			header.Set("Content-Type", "image/svg+xml")
			// A compromised/misconfigured upstream sends a permissive CSP; the
			// proxy must not forward it.
			header.Set("Content-Security-Policy", "default-src *")
			return &http.Response{
				StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(content)),
				ContentLength: int64(len(content)), Request: req, Header: header,
			}, nil
		})}),
	}
	e := echo.New()
	request := httptest.NewRequest(http.MethodGet, "/supermarket/artifacts/icon/"+digestText, nil)
	recorder := httptest.NewRecorder()
	if err := handler.proxySkillIcon(e.NewContext(request, recorder), digestText); err != nil {
		t.Fatalf("proxySkillIcon() error = %v", err)
	}
	if got := recorder.Header().Get("Content-Security-Policy"); !strings.Contains(got, "sandbox") || strings.Contains(got, "*") {
		t.Fatalf("upstream CSP was not overridden: %q", got)
	}
	if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

func validSkillArtifact(t *testing.T) []byte {
	t.Helper()
	var output bytes.Buffer
	gz := gzip.NewWriter(&output)
	tw := tar.NewWriter(gz)
	content := []byte(validSkillArtifactContent)
	if err := tw.WriteHeader(&tar.Header{Name: "SKILL.md", Mode: 0o644, Typeflag: tar.TypeReg, Size: int64(len(content))}); err != nil {
		t.Fatalf("WriteHeader(SKILL.md): %v", err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatalf("Write(SKILL.md): %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar Close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip Close: %v", err)
	}
	return output.Bytes()
}

func validRegistrySkillDescriptor() SupermarketCatalogSkill {
	return SupermarketCatalogSkill{
		RegistryID: "registry", PackageID: "package", SkillID: "skill", InstallID: "registry+package+skill",
		Artifact: SupermarketSkillArtifact{
			Format: "memoh_skill_v1",
			Digest: strings.Repeat("a", 64), Size: 1,
			UncompressedSize: validSkillArtifactUncompressedSize,
			ArchiveSize:      2 * 1024,
			FileCount:        1,
			ContentType:      "application/gzip", DownloadURL: "/artifact",
		},
	}
}

func validRegistryPackageDescriptor() SupermarketSkillPackageDescriptor {
	first := validRegistrySkillDescriptor()
	second := validRegistrySkillDescriptor()
	second.SkillID = "second"
	second.InstallID = "registry+package+second"
	return SupermarketSkillPackageDescriptor{
		SkillPackageSummary: SupermarketSkillPackageSummary{
			SchemaVersion: "1", RegistryID: "registry", PackageID: "package", Name: "Package",
			Description: "Demo", Tags: []string{}, Categories: []SupermarketSkillPackageCategory{}, SkillCount: 2,
		},
		Revision: strings.Repeat("b", 64),
		Skills:   []SupermarketCatalogSkill{first, second},
	}
}

func registryPackageReleaseBytes(t *testing.T, pkg SupermarketSkillPackageDescriptor) []byte {
	t.Helper()
	members := make([]supermarketSkillPackageReleaseSkill, 0, len(pkg.Skills))
	for _, skill := range pkg.Skills {
		members = append(members, supermarketSkillPackageReleaseSkill{
			SchemaVersion: skill.SchemaVersion, RegistryID: skill.RegistryID, PackageID: skill.PackageID,
			SkillID: skill.SkillID, InstallID: skill.InstallID, Name: skill.Name,
			Description: skill.Description, Author: skill.Author, Homepage: skill.Homepage,
			Tags: skill.Tags, Category: skill.Category, CategoryName: skill.CategoryName,
			SourceCategory: skill.SourceCategory, Files: skill.Files, Icon: skill.Icon, Artifact: skill.Artifact,
		})
	}
	payload, err := json.Marshal(SupermarketSkillPackageRelease{
		SchemaVersion: pkg.SchemaVersion,
		RegistryID:    pkg.RegistryID,
		PackageID:     pkg.PackageID,
		Name:          pkg.Name,
		Description:   pkg.Description,
		Tags:          pkg.Tags,
		Icon:          pkg.Icon,
		Skills:        members,
	})
	if err != nil {
		t.Fatalf("marshal immutable Package release: %v", err)
	}
	return payload
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func testHTTPResponse(req *http.Request, status int, content []byte) *http.Response {
	return &http.Response{
		StatusCode:    status,
		Header:        make(http.Header),
		Body:          io.NopCloser(bytes.NewReader(content)),
		ContentLength: int64(len(content)),
		Request:       req,
	}
}
