package handlers

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	supermarketclient "github.com/felinics/memoh/internal/supermarket"
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
