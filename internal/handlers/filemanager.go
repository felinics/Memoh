package handlers

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/attachment"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

// ---------- request / response types ----------

type FSFileInfo struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
	ModTime string `json:"modTime"`
	IsDir   bool   `json:"isDir"`
}

type FSListResponse struct {
	Path    string       `json:"path"`
	Entries []FSFileInfo `json:"entries"`
}

type FSReadResponse struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Size     int64  `json:"size"`
	Revision string `json:"revision"`
}

type FSUploadResponse struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// FSWriteRequest is the body for creating / overwriting a file.
type FSWriteRequest struct {
	Path             string  `json:"path"`
	Content          string  `json:"content"`
	ExpectedRevision *string `json:"expectedRevision,omitempty"`
}

// FSMkdirRequest is the body for creating a directory.
type FSMkdirRequest struct {
	Path string `json:"path"`
}

// FSDeleteRequest is the body for deleting a file or directory.
type FSDeleteRequest struct {
	Path      string `json:"path"`
	Recursive bool   `json:"recursive"`
}

// FSRenameRequest is the body for renaming / moving an entry.
type FSRenameRequest struct {
	OldPath string `json:"oldPath"`
	NewPath string `json:"newPath"`
}

// FSArchiveRequest is the body for downloading multiple files/directories as tar.gz.
type FSArchiveRequest struct {
	Paths []string `json:"paths"`
}

// FSExtractRequest is the body for extracting an archive in the workspace.
type FSExtractRequest struct {
	Path string `json:"path"`
}

type FSExtractResponse struct {
	Destination string `json:"destination"`
	Files       int    `json:"files"`
	Directories int    `json:"directories"`
}

type fsOpResponse struct {
	OK       bool   `json:"ok"`
	Revision string `json:"revision,omitempty"`
}

// ---------- helpers ----------

// resolveContainerPath cleans and validates a container-relative path.
func resolveContainerPath(rawPath string) (string, error) {
	cleaned := path.Clean("/" + strings.ReplaceAll(strings.TrimSpace(rawPath), "\\", "/"))
	if cleaned == "" {
		cleaned = "/"
	}
	if strings.HasPrefix(cleaned, "..") {
		return "", errors.New("invalid path")
	}
	return cleaned, nil
}

func isContainerMediaPath(containerPath string) bool {
	return attachment.IsMediaAccessPath(containerPath)
}

func isPathWithin(parentPath, childPath string) bool {
	parentPath = path.Clean(parentPath)
	childPath = path.Clean(childPath)
	return childPath == parentPath || strings.HasPrefix(childPath, strings.TrimRight(parentPath, "/")+"/")
}

func dedupeArchivePaths(paths []string) ([]string, error) {
	seen := make(map[string]struct{}, len(paths))
	cleaned := make([]string, 0, len(paths))
	for _, raw := range paths {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		containerPath, err := resolveContainerPath(raw)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[containerPath]; ok {
			continue
		}
		seen[containerPath] = struct{}{}
		cleaned = append(cleaned, containerPath)
	}
	parentsOnly := cleaned[:0]
	for _, candidate := range cleaned {
		nested := false
		for _, other := range cleaned {
			if candidate != other && isPathWithin(other, candidate) {
				nested = true
				break
			}
		}
		if !nested {
			parentsOnly = append(parentsOnly, candidate)
		}
	}
	if len(parentsOnly) == 0 {
		return nil, errors.New("paths are required")
	}
	return parentsOnly, nil
}

func uniqueArchiveName(containerPath string, used map[string]int) string {
	name := path.Base(containerPath)
	if name == "." || name == "/" || name == "" {
		name = "workspace"
	}
	if count := used[name]; count > 0 {
		used[name] = count + 1
		ext := path.Ext(name)
		base := strings.TrimSuffix(name, ext)
		if base == "" {
			base = name
			ext = ""
		}
		return fmt.Sprintf("%s-%d%s", base, count+1, ext)
	}
	used[name] = 1
	return name
}

func safeArchiveEntryPath(name string) (string, error) {
	cleaned := path.Clean(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/"))
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "." || cleaned == "" {
		return "", nil
	}
	for _, part := range strings.Split(cleaned, "/") {
		if part == ".." {
			return "", errors.New("archive contains invalid path")
		}
	}
	return cleaned, nil
}

func defaultExtractDestination(containerPath string) string {
	dir := path.Dir(containerPath)
	name := path.Base(containerPath)
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".tar.gz"):
		name = name[:len(name)-len(".tar.gz")]
	case strings.HasSuffix(lower, ".tgz"):
		name = name[:len(name)-len(".tgz")]
	case strings.HasSuffix(lower, ".zip"):
		name = name[:len(name)-len(".zip")]
	default:
		name = strings.TrimSuffix(name, path.Ext(name))
	}
	if strings.TrimSpace(name) == "" || name == "." {
		name = "extracted"
	}
	return path.Join(dir, name)
}

func archiveDownloadName(containerPath string, isDir bool) string {
	name := path.Base(containerPath)
	if name == "." || name == "/" || name == "" {
		name = "workspace"
	}
	if isDir {
		return name + ".tar.gz"
	}
	return name
}

func fsContentRevision(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", sum)
}

func readContainerFileRevision(ctx context.Context, client *bridge.Client, containerPath string) (string, error) {
	rc, err := client.ReadRaw(ctx, containerPath)
	if err != nil {
		if errors.Is(err, bridge.ErrNotFound) {
			return "", nil
		}
		return "", err
	}
	defer func() { _ = rc.Close() }()

	data, err := io.ReadAll(rc)
	if err != nil {
		return "", err
	}
	return fsContentRevision(data), nil
}

// getGRPCClient returns the gRPC client for the bot's container.
func (h *ContainerdHandler) getGRPCClient(ctx context.Context, botID string) (*bridge.Client, error) {
	return h.manager.MCPClient(ctx, botID)
}

// fsFileInfoFromEntry converts a gRPC FileEntry to FSFileInfo.
func fsFileInfoFromEntry(containerPath, name string, isDir bool, size int64, mode, modTime string) FSFileInfo {
	return FSFileInfo{
		Name:    name,
		Path:    path.Join(containerPath, name),
		Size:    size,
		Mode:    mode,
		ModTime: modTime,
		IsDir:   isDir,
	}
}

// fsHTTPError maps mcpclient domain errors to HTTP status codes.
func fsHTTPError(err error) error {
	switch {
	case errors.Is(err, bridge.ErrNotFound):
		return echo.NewHTTPError(http.StatusNotFound, err.Error())
	case errors.Is(err, bridge.ErrBadRequest):
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	case errors.Is(err, bridge.ErrForbidden):
		return echo.NewHTTPError(http.StatusForbidden, err.Error())
	case errors.Is(err, bridge.ErrUnavailable):
		return workspaceUnavailableError(err)
	default:
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
}

func workspaceUnavailableError(cause error) error {
	return apperror.Wrap(apperror.CodeWorkspaceUnreachable, cause, nil)
}

// ---------- handlers ----------

// FSStat godoc
// @Summary Get file or directory info
// @Description Returns metadata about a file or directory at the given workspace path
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Param path query string true "Workspace path"
// @Success 200 {object} FSFileInfo
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/container/fs [get].
func (h *ContainerdHandler) FSStat(c echo.Context) error {
	botID, err := h.requireBotAccessWithPermission(c, bots.PermissionWorkspaceRead)
	if err != nil {
		return err
	}
	rawPath := c.QueryParam("path")
	if strings.TrimSpace(rawPath) == "" {
		rawPath = "/"
	}

	containerPath, err := resolveContainerPath(rawPath)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	ctx := c.Request().Context()
	client, err := h.getGRPCClient(ctx, botID)
	if err != nil {
		return workspaceUnavailableError(err)
	}

	entry, err := client.Stat(ctx, containerPath)
	if err != nil {
		return fsHTTPError(err)
	}

	return c.JSON(http.StatusOK, FSFileInfo{
		Name:    path.Base(containerPath),
		Path:    containerPath,
		Size:    entry.GetSize(),
		Mode:    entry.GetMode(),
		ModTime: entry.GetModTime(),
		IsDir:   entry.GetIsDir(),
	})
}

// FSList godoc
// @Summary List directory contents
// @Description Lists files and directories at the given workspace path
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Param path query string true "Workspace directory path"
// @Success 200 {object} FSListResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/container/fs/list [get].
func (h *ContainerdHandler) FSList(c echo.Context) error {
	botID, err := h.requireBotAccessWithPermission(c, bots.PermissionWorkspaceRead)
	if err != nil {
		return err
	}
	rawPath := c.QueryParam("path")
	if strings.TrimSpace(rawPath) == "" {
		rawPath = "/"
	}

	containerPath, err := resolveContainerPath(rawPath)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	ctx := c.Request().Context()
	client, err := h.getGRPCClient(ctx, botID)
	if err != nil {
		return workspaceUnavailableError(err)
	}

	entries, err := client.ListDirAll(ctx, containerPath, false)
	if err != nil {
		return fsHTTPError(err)
	}

	fileInfos := make([]FSFileInfo, 0, len(entries))
	for _, e := range entries {
		if e.Path == containerPath {
			continue
		}
		fileInfos = append(fileInfos, fsFileInfoFromEntry(
			containerPath,
			path.Base(e.Path),
			e.IsDir,
			e.Size,
			e.Mode,
			e.ModTime,
		))
	}

	return c.JSON(http.StatusOK, FSListResponse{
		Path:    containerPath,
		Entries: fileInfos,
	})
}

// FSRead godoc
// @Summary Read file content as text
// @Description Reads the content of a file and returns it as a JSON string
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Param path query string true "Workspace file path"
// @Success 200 {object} FSReadResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/container/fs/read [get].
func (h *ContainerdHandler) FSRead(c echo.Context) error {
	botID, err := h.requireBotAccessWithPermission(c, bots.PermissionWorkspaceRead)
	if err != nil {
		return err
	}
	rawPath := c.QueryParam("path")
	if strings.TrimSpace(rawPath) == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "path is required")
	}

	containerPath, err := resolveContainerPath(rawPath)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	ctx := c.Request().Context()
	client, err := h.getGRPCClient(ctx, botID)
	if err != nil {
		return workspaceUnavailableError(err)
	}

	rc, err := client.ReadRaw(ctx, containerPath)
	if err != nil {
		return fsHTTPError(err)
	}
	defer func() { _ = rc.Close() }()

	data, err := io.ReadAll(rc)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to read file")
	}

	return c.JSON(http.StatusOK, FSReadResponse{
		Path:     containerPath,
		Content:  string(data),
		Size:     int64(len(data)),
		Revision: fsContentRevision(data),
	})
}

// FSDownload godoc
// @Summary Download a file as binary stream
// @Description Downloads a file from the workspace with appropriate Content-Type
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Param path query string true "Workspace file path"
// @Produce octet-stream
// @Success 200 {file} binary
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/container/fs/download [get].
func (h *ContainerdHandler) FSDownload(c echo.Context) error {
	rawPath := c.QueryParam("path")
	if strings.TrimSpace(rawPath) == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "path is required")
	}

	containerPath, err := resolveContainerPath(rawPath)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	requireAccess := func(c echo.Context) (string, error) {
		return h.requireBotAccessWithPermission(c, bots.PermissionWorkspaceRead)
	}
	if isContainerMediaPath(containerPath) {
		requireAccess = h.requireBotAccessWithGuest
	}
	botID, err := requireAccess(c)
	if err != nil {
		return err
	}

	ctx := c.Request().Context()
	client, err := h.getGRPCClient(ctx, botID)
	if err != nil {
		return workspaceUnavailableError(err)
	}

	entry, err := client.Stat(ctx, containerPath)
	if err != nil {
		return fsHTTPError(err)
	}
	if entry.GetIsDir() {
		fileName := archiveDownloadName(containerPath, true)
		c.Response().Header().Set(echo.HeaderContentType, "application/gzip")
		c.Response().Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, fileName))
		return h.writeArchive(ctx, client, []string{containerPath}, c.Response().Writer)
	}

	rc, err := client.ReadRaw(ctx, containerPath)
	if err != nil {
		return fsHTTPError(err)
	}
	defer func() { _ = rc.Close() }()

	data, err := io.ReadAll(rc)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to read file")
	}

	fileName := path.Base(containerPath)
	contentType := mime.TypeByExtension(path.Ext(fileName))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	c.Response().Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, fileName))
	return c.Blob(http.StatusOK, contentType, data)
}

// FSArchive godoc
// @Summary Download files and directories as tar.gz
// @Description Downloads selected files/directories from the workspace as a tar.gz archive
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Param payload body FSArchiveRequest true "Archive request"
// @Produce octet-stream
// @Success 200 {file} binary
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/container/fs/archive [post].
func (h *ContainerdHandler) FSArchive(c echo.Context) error {
	botID, err := h.requireBotAccessWithPermission(c, bots.PermissionWorkspaceRead)
	if err != nil {
		return err
	}
	var req FSArchiveRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	paths, err := dedupeArchivePaths(req.Paths)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	ctx := c.Request().Context()
	client, err := h.getGRPCClient(ctx, botID)
	if err != nil {
		return workspaceUnavailableError(err)
	}
	for _, p := range paths {
		if _, err := client.Stat(ctx, p); err != nil {
			return fsHTTPError(err)
		}
	}

	c.Response().Header().Set(echo.HeaderContentType, "application/gzip")
	c.Response().Header().Set("Content-Disposition", `attachment; filename="workspace-selection.tar.gz"`)
	return h.writeArchive(ctx, client, paths, c.Response().Writer)
}

func (h *ContainerdHandler) writeArchive(ctx context.Context, client *bridge.Client, containerPaths []string, w io.Writer) error {
	gw := gzip.NewWriter(w)
	defer func() { _ = gw.Close() }()
	tw := tar.NewWriter(gw)
	defer func() { _ = tw.Close() }()

	paths, err := dedupeArchivePaths(containerPaths)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	usedNames := make(map[string]int, len(paths))
	for _, containerPath := range paths {
		entry, err := client.Stat(ctx, containerPath)
		if err != nil {
			return fsHTTPError(err)
		}
		archiveName := uniqueArchiveName(containerPath, usedNames)
		if err := h.writeArchiveEntry(ctx, client, tw, containerPath, archiveName, entry.GetIsDir()); err != nil {
			return err
		}
	}
	return nil
}

func (h *ContainerdHandler) writeArchiveEntry(ctx context.Context, client *bridge.Client, tw *tar.Writer, containerPath, archivePath string, isDir bool) error {
	archivePath, err := safeArchiveEntryPath(archivePath)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if archivePath == "" {
		return nil
	}

	entry, err := client.Stat(ctx, containerPath)
	if err != nil {
		return fsHTTPError(err)
	}

	header := &tar.Header{
		Name: archivePath,
		Mode: 0o755,
		Size: entry.GetSize(),
	}
	if isDir {
		header.Typeflag = tar.TypeDir
		header.Name = strings.TrimRight(archivePath, "/") + "/"
		header.Size = 0
		if err := tw.WriteHeader(header); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("archive directory: %v", err))
		}
		entries, err := client.ListDirAll(ctx, containerPath, false)
		if err != nil {
			return fsHTTPError(err)
		}
		for _, child := range entries {
			name := path.Base(child.GetPath())
			if name == "." || name == "/" || name == "" {
				continue
			}
			childContainerPath := path.Join(containerPath, name)
			childArchivePath := path.Join(archivePath, name)
			if err := h.writeArchiveEntry(ctx, client, tw, childContainerPath, childArchivePath, child.GetIsDir()); err != nil {
				return err
			}
		}
		return nil
	}

	header.Typeflag = tar.TypeReg
	header.Mode = 0o644
	if err := tw.WriteHeader(header); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("archive file: %v", err))
	}
	rc, err := client.ReadRaw(ctx, containerPath)
	if err != nil {
		return fsHTTPError(err)
	}
	defer func() { _ = rc.Close() }()
	if _, err := io.Copy(tw, rc); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("archive copy: %v", err))
	}
	return nil
}

// FSWrite godoc
// @Summary Write text content to a file
// @Description Creates or overwrites a file with the provided text content
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Param payload body FSWriteRequest true "Write request"
// @Success 200 {object} fsOpResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/container/fs/write [post].
func (h *ContainerdHandler) FSWrite(c echo.Context) error {
	botID, err := h.requireBotAccessWithPermission(c, bots.PermissionWorkspaceWrite)
	if err != nil {
		return err
	}
	var req FSWriteRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if strings.TrimSpace(req.Path) == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "path is required")
	}

	containerPath, err := resolveContainerPath(req.Path)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	ctx := c.Request().Context()
	client, err := h.getGRPCClient(ctx, botID)
	if err != nil {
		return workspaceUnavailableError(err)
	}

	if req.ExpectedRevision != nil {
		// Empty string is not a meaningful baseline — it collapses to the same
		// sentinel that readContainerFileRevision returns for a missing file,
		// which would let an "I don't know the revision" client silently
		// overwrite or create the file without an explicit unconditional
		// write. Reject the ambiguous form; clients should omit the field
		// instead.
		if *req.ExpectedRevision == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "expectedRevision must be non-empty; omit the field for an unconditional write")
		}
		currentRevision, err := readContainerFileRevision(ctx, client, containerPath)
		if err != nil {
			return fsHTTPError(err)
		}
		if currentRevision != *req.ExpectedRevision {
			return echo.NewHTTPError(http.StatusConflict, "file changed on disk")
		}
	}

	contentBytes := []byte(req.Content)
	if err := client.WriteFile(ctx, containerPath, contentBytes); err != nil {
		return fsHTTPError(err)
	}

	return c.JSON(http.StatusOK, fsOpResponse{OK: true, Revision: fsContentRevision(contentBytes)})
}

// FSUpload godoc
// @Summary Upload a file via multipart form
// @Description Uploads a binary file to the given workspace path
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Param path formData string true "Destination workspace path"
// @Param file formData file true "File to upload"
// @Accept multipart/form-data
// @Success 200 {object} FSUploadResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/container/fs/upload [post].
func (h *ContainerdHandler) FSUpload(c echo.Context) error {
	botID, err := h.requireBotAccessWithPermission(c, bots.PermissionWorkspaceWrite)
	if err != nil {
		return err
	}
	destPath := strings.TrimSpace(c.FormValue("path"))
	if destPath == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "path is required")
	}

	containerPath, err := resolveContainerPath(destPath)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	ctx := c.Request().Context()
	client, err := h.getGRPCClient(ctx, botID)
	if err != nil {
		return workspaceUnavailableError(err)
	}

	file, err := c.FormFile("file")
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "file is required")
	}
	src, err := file.Open()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	defer func() { _ = src.Close() }()

	written, err := client.WriteRaw(ctx, containerPath, src)
	if err != nil {
		return fsHTTPError(err)
	}

	return c.JSON(http.StatusOK, FSUploadResponse{
		Path: containerPath,
		Size: written,
	})
}

// FSMkdir godoc
// @Summary Create a directory
// @Description Creates a directory (and parents) at the given workspace path
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Param payload body FSMkdirRequest true "Mkdir request"
// @Success 200 {object} fsOpResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/container/fs/mkdir [post].
func (h *ContainerdHandler) FSMkdir(c echo.Context) error {
	botID, err := h.requireBotAccessWithPermission(c, bots.PermissionWorkspaceWrite)
	if err != nil {
		return err
	}
	var req FSMkdirRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if strings.TrimSpace(req.Path) == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "path is required")
	}

	containerPath, err := resolveContainerPath(req.Path)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	ctx := c.Request().Context()
	client, err := h.getGRPCClient(ctx, botID)
	if err != nil {
		return workspaceUnavailableError(err)
	}

	if err := client.Mkdir(ctx, containerPath); err != nil {
		return fsHTTPError(err)
	}

	return c.JSON(http.StatusOK, fsOpResponse{OK: true})
}

// FSDelete godoc
// @Summary Delete a file or directory
// @Description Deletes a file or directory at the given workspace path
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Param payload body FSDeleteRequest true "Delete request"
// @Success 200 {object} fsOpResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/container/fs/delete [post].
func (h *ContainerdHandler) FSDelete(c echo.Context) error {
	botID, err := h.requireBotAccessWithPermission(c, bots.PermissionWorkspaceWrite)
	if err != nil {
		return err
	}
	var req FSDeleteRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if strings.TrimSpace(req.Path) == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "path is required")
	}

	containerPath, err := resolveContainerPath(req.Path)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	if containerPath == "/" {
		return echo.NewHTTPError(http.StatusForbidden, "cannot delete root directory")
	}

	ctx := c.Request().Context()
	client, err := h.getGRPCClient(ctx, botID)
	if err != nil {
		return workspaceUnavailableError(err)
	}

	if err := client.DeleteFile(ctx, containerPath, req.Recursive); err != nil {
		return fsHTTPError(err)
	}

	return c.JSON(http.StatusOK, fsOpResponse{OK: true})
}

// FSRename godoc
// @Summary Rename or move a file/directory
// @Description Renames or moves a file/directory from oldPath to newPath
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Param payload body FSRenameRequest true "Rename request"
// @Success 200 {object} fsOpResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/container/fs/rename [post].
func (h *ContainerdHandler) FSRename(c echo.Context) error {
	botID, err := h.requireBotAccessWithPermission(c, bots.PermissionWorkspaceWrite)
	if err != nil {
		return err
	}
	var req FSRenameRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if strings.TrimSpace(req.OldPath) == "" || strings.TrimSpace(req.NewPath) == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "oldPath and newPath are required")
	}

	oldPath, err := resolveContainerPath(req.OldPath)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	newPath, err := resolveContainerPath(req.NewPath)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	ctx := c.Request().Context()
	client, err := h.getGRPCClient(ctx, botID)
	if err != nil {
		return workspaceUnavailableError(err)
	}

	if err := client.Rename(ctx, oldPath, newPath); err != nil {
		return fsHTTPError(err)
	}

	return c.JSON(http.StatusOK, fsOpResponse{OK: true})
}

// FSExtract godoc
// @Summary Extract an archive file
// @Description Extracts a .zip, .tar.gz, or .tgz file into a sibling directory named after the archive
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Param payload body FSExtractRequest true "Extract request"
// @Success 200 {object} FSExtractResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/container/fs/extract [post].
func (h *ContainerdHandler) FSExtract(c echo.Context) error {
	botID, err := h.requireBotAccessWithPermission(c, bots.PermissionWorkspaceWrite)
	if err != nil {
		return err
	}
	var req FSExtractRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if strings.TrimSpace(req.Path) == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "path is required")
	}
	containerPath, err := resolveContainerPath(req.Path)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	lower := strings.ToLower(containerPath)
	if !strings.HasSuffix(lower, ".zip") && !strings.HasSuffix(lower, ".tar.gz") && !strings.HasSuffix(lower, ".tgz") {
		return echo.NewHTTPError(http.StatusBadRequest, "unsupported archive format")
	}

	ctx := c.Request().Context()
	client, err := h.getGRPCClient(ctx, botID)
	if err != nil {
		return workspaceUnavailableError(err)
	}
	entry, err := client.Stat(ctx, containerPath)
	if err != nil {
		return fsHTTPError(err)
	}
	if entry.GetIsDir() {
		return echo.NewHTTPError(http.StatusBadRequest, "path must be an archive file")
	}

	destination := defaultExtractDestination(containerPath)
	if _, err := client.Stat(ctx, destination); err == nil {
		return echo.NewHTTPError(http.StatusConflict, "destination already exists")
	} else if !errors.Is(err, bridge.ErrNotFound) {
		return fsHTTPError(err)
	}
	if err := client.Mkdir(ctx, destination); err != nil {
		return fsHTTPError(err)
	}

	rc, err := client.ReadRaw(ctx, containerPath)
	if err != nil {
		return fsHTTPError(err)
	}
	defer func() { _ = rc.Close() }()

	var resp FSExtractResponse
	resp.Destination = destination
	switch {
	case strings.HasSuffix(lower, ".zip"):
		resp.Files, resp.Directories, err = extractZip(ctx, client, rc, destination)
	default:
		resp.Files, resp.Directories, err = extractTarGz(ctx, client, rc, destination)
	}
	if err != nil {
		_ = client.DeleteFile(ctx, destination, true)
		return err
	}
	return c.JSON(http.StatusOK, resp)
}

func extractZip(ctx context.Context, client *bridge.Client, r io.Reader, destination string) (int, int, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return 0, 0, echo.NewHTTPError(http.StatusInternalServerError, "failed to read archive")
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return 0, 0, echo.NewHTTPError(http.StatusBadRequest, "invalid zip archive")
	}
	files, dirs := 0, 0
	for _, file := range zr.File {
		entryPath, err := safeArchiveEntryPath(file.Name)
		if err != nil {
			return files, dirs, echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if entryPath == "" {
			continue
		}
		targetPath := path.Join(destination, entryPath)
		if file.FileInfo().IsDir() {
			if err := client.Mkdir(ctx, targetPath); err != nil {
				return files, dirs, fsHTTPError(err)
			}
			dirs++
			continue
		}
		src, err := file.Open()
		if err != nil {
			return files, dirs, echo.NewHTTPError(http.StatusBadRequest, "invalid zip entry")
		}
		written, writeErr := client.WriteRaw(ctx, targetPath, src)
		closeErr := src.Close()
		if writeErr != nil {
			return files, dirs, fsHTTPError(writeErr)
		}
		if closeErr != nil {
			return files, dirs, echo.NewHTTPError(http.StatusInternalServerError, closeErr.Error())
		}
		if written >= 0 {
			files++
		}
	}
	return files, dirs, nil
}

func extractTarGz(ctx context.Context, client *bridge.Client, r io.Reader, destination string) (int, int, error) {
	gr, err := gzip.NewReader(r)
	if err != nil {
		return 0, 0, echo.NewHTTPError(http.StatusBadRequest, "invalid gzip archive")
	}
	defer func() { _ = gr.Close() }()

	tr := tar.NewReader(gr)
	files, dirs := 0, 0
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return files, dirs, echo.NewHTTPError(http.StatusBadRequest, "invalid tar archive")
		}
		entryPath, err := safeArchiveEntryPath(header.Name)
		if err != nil {
			return files, dirs, echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if entryPath == "" {
			continue
		}
		targetPath := path.Join(destination, entryPath)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := client.Mkdir(ctx, targetPath); err != nil {
				return files, dirs, fsHTTPError(err)
			}
			dirs++
		case tar.TypeReg:
			if _, err := client.WriteRaw(ctx, targetPath, tr); err != nil {
				return files, dirs, fsHTTPError(err)
			}
			files++
		default:
			continue
		}
	}
	return files, dirs, nil
}
