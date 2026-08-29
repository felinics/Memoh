//go:build linux

package bridgesvc

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const noFollowResolve = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_SYMLINKS

func openNoFollowRoot(root string) (int, error) {
	root, _, err := validateNoFollowPath(root, "placeholder")
	if err != nil {
		return -1, err
	}
	fsRoot, err := unix.Open(string(filepath.Separator), unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("open filesystem root: %w", err)
	}
	relativeRoot := strings.TrimPrefix(root, string(filepath.Separator))
	if relativeRoot == "" {
		return fsRoot, nil
	}
	defer func() { _ = unix.Close(fsRoot) }()
	fd, err := unix.Openat2(fsRoot, relativeRoot, &unix.OpenHow{
		Flags:   unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: noFollowResolve,
	})
	if err != nil {
		return -1, fmt.Errorf("open nofollow root: %w", err)
	}
	return fd, nil
}

func openRegularNoFollow(root, relativePath string) (*os.File, error) {
	rootFD, err := openNoFollowRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(rootFD) }()
	// Resolve the complete path in one syscall. In particular, do not walk to
	// a parent descriptor first: openat2 can then detect concurrent ancestor
	// renames while enforcing BENEATH and NO_SYMLINKS for the whole lookup.
	fd, err := unix.Openat2(rootFD, relativePath, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK,
		Resolve: noFollowResolve,
	})
	if err != nil {
		return nil, fmt.Errorf("open nofollow file: %w", err)
	}
	if err := requireRegularFD(fd); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), relativePath), nil //nolint:gosec // successful openat2 returns a non-negative file descriptor.
}

func createRegularNoFollow(root, relativePath string) (*os.File, error) {
	// Preparing missing parents may race with a same-UID rename. Never create
	// the final file relative to the resulting parent descriptor: that
	// descriptor could have been moved outside root after it was opened.
	// Instead, discard it and resolve the complete final path again from a
	// freshly opened trusted root descriptor in one openat2 call. A raced parent
	// preparation can at worst leave an empty directory in the moved tree; the
	// JSONL payload itself is created beneath root or the operation fails closed.
	parentFD, _, err := openNoFollowParent(root, relativePath, true)
	if err != nil {
		return nil, err
	}
	_ = unix.Close(parentFD)

	rootFD, err := openNoFollowRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(rootFD) }()
	fd, err := unix.Openat2(rootFD, relativePath, &unix.OpenHow{
		Flags:   unix.O_WRONLY | unix.O_CREAT | unix.O_EXCL | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Mode:    0o600,
		Resolve: noFollowResolve,
	})
	if err != nil {
		return nil, fmt.Errorf("create nofollow file: %w", err)
	}
	if err := requireRegularFD(fd); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), relativePath), nil //nolint:gosec // successful openat2 returns a non-negative file descriptor.
}

func openNoFollowParent(root, relativePath string, create bool) (int, string, error) {
	fd, err := openNoFollowRoot(root)
	if err != nil {
		return -1, "", err
	}
	components := strings.Split(relativePath, string(filepath.Separator))
	for _, component := range components[:len(components)-1] {
		next, openErr := unix.Openat2(fd, component, &unix.OpenHow{
			Flags:   unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC,
			Resolve: noFollowResolve,
		})
		if errors.Is(openErr, unix.ENOENT) && create {
			mkdirErr := unix.Mkdirat(fd, component, 0o750)
			if mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				_ = unix.Close(fd)
				return -1, "", fmt.Errorf("create nofollow parent component %q: %w", component, mkdirErr)
			}
			next, openErr = unix.Openat2(fd, component, &unix.OpenHow{
				Flags:   unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC,
				Resolve: noFollowResolve,
			})
		}
		_ = unix.Close(fd)
		if openErr != nil {
			return -1, "", fmt.Errorf("open nofollow parent component %q: %w", component, openErr)
		}
		fd = next
	}
	return fd, components[len(components)-1], nil
}

func requireRegularFD(fd int) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return fmt.Errorf("fstat opened file: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return errors.New("opened path is not a regular file")
	}
	return nil
}
