package bridgesvc

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

var errNoFollowUnsupported = errors.New("descriptor-anchored nofollow I/O is unsupported on this platform")

func validateNoFollowPath(root, relativePath string) (string, string, error) {
	root = strings.TrimSpace(root)
	relativePath = strings.TrimSpace(relativePath)
	if root == "" || !filepath.IsAbs(root) {
		return "", "", errors.New("root must be an absolute path")
	}
	if relativePath == "" || filepath.IsAbs(relativePath) || filepath.VolumeName(relativePath) != "" {
		return "", "", errors.New("relative_path must be a non-empty relative path")
	}
	if strings.IndexByte(root, 0) >= 0 || strings.IndexByte(relativePath, 0) >= 0 {
		return "", "", errors.New("path contains a NUL byte")
	}
	cleanRoot := filepath.Clean(root)
	cleanRelative := filepath.Clean(relativePath)
	if cleanRelative == "." || cleanRelative != relativePath || cleanRelative == ".." || strings.HasPrefix(cleanRelative, ".."+string(filepath.Separator)) {
		return "", "", errors.New("relative_path must be clean and cannot traverse")
	}
	return cleanRoot, cleanRelative, nil
}

func (s *Server) resolveNoFollowPath(root, relativePath string) (string, string, error) {
	root, relativePath, err := validateNoFollowPath(root, relativePath)
	if err != nil {
		return "", "", err
	}
	return validateNoFollowPath(s.resolvePath(root), relativePath)
}

func noFollowOpenStatus(operation string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errNoFollowUnsupported):
		return status.Errorf(codes.Unimplemented, "%s: %v", operation, err)
	case errors.Is(err, os.ErrNotExist):
		return status.Errorf(codes.NotFound, "%s: %v", operation, err)
	case errors.Is(err, os.ErrExist):
		return status.Errorf(codes.AlreadyExists, "%s: target already exists", operation)
	default:
		// ELOOP, EXDEV, and rename-race failures intentionally remain a
		// fail-closed precondition error instead of being disguised as absence.
		return status.Errorf(codes.FailedPrecondition, "%s: unsafe or unsupported path: %v", operation, err)
	}
}

func (s *Server) ReadRawNoFollow(req *pb.ReadRawNoFollowRequest, stream pb.ContainerService_ReadRawNoFollowServer) error {
	root, relativePath, err := s.resolveNoFollowPath(req.GetRoot(), req.GetRelativePath())
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "invalid anchored read path: %v", err)
	}
	f, err := openRegularNoFollow(root, relativePath)
	if err != nil {
		return noFollowOpenStatus("open anchored read", err)
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, rawChunkSize)
	for {
		n, readErr := f.Read(buf)
		if n > 0 {
			if sendErr := stream.Send(&pb.DataChunk{Data: buf[:n]}); sendErr != nil {
				return sendErr
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return status.Errorf(codes.Internal, "read anchored file: %v", readErr)
		}
	}
}

func (s *Server) WriteRawNoFollow(stream pb.ContainerService_WriteRawNoFollowServer) error {
	var (
		f       *os.File
		written int64
	)
	defer func() {
		if f != nil {
			_ = f.Close()
		}
	}()

	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if chunk.GetAbort() {
			return status.Error(codes.Aborted, "anchored write aborted by sender")
		}
		if f == nil {
			root, relativePath, validateErr := s.resolveNoFollowPath(chunk.GetRoot(), chunk.GetRelativePath())
			if validateErr != nil {
				return status.Errorf(codes.InvalidArgument, "invalid anchored write path: %v", validateErr)
			}
			f, err = createRegularNoFollow(root, relativePath)
			if err != nil {
				return noFollowOpenStatus("create anchored file", err)
			}
		} else if chunk.GetRoot() != "" || chunk.GetRelativePath() != "" {
			return status.Error(codes.InvalidArgument, "root and relative_path may appear only in the first chunk")
		}
		if len(chunk.GetData()) == 0 {
			continue
		}
		n, writeErr := f.Write(chunk.GetData())
		written += int64(n)
		if writeErr != nil {
			return status.Errorf(codes.Internal, "write anchored file: %v", writeErr)
		}
	}
	if f == nil {
		return status.Error(codes.InvalidArgument, "first chunk must include root and relative_path")
	}
	if err := f.Close(); err != nil {
		f = nil
		return status.Errorf(codes.Internal, "close anchored file: %v", err)
	}
	f = nil
	if err := contextStatusError(stream.Context()); err != nil {
		return err
	}
	return stream.SendAndClose(&pb.WriteRawResponse{BytesWritten: written})
}
