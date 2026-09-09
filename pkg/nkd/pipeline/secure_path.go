package pipeline

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

const pipelineCopyBufferSize = 128 << 10

var pipelineCopyBufferPool = sync.Pool{New: func() any {
	buffer := make([]byte, pipelineCopyBufferSize)
	return &buffer
}}

// openOrCreateParentBeneath holds a descriptor for the final path's parent while
// every intermediate directory is resolved with O_NOFOLLOW. Creation uses mkdirat
// relative to the already-authenticated parent, so a swapped workspace path cannot
// redirect a download outside the runner-owned root.
func openOrCreateParentBeneath(root, relative string) (*os.File, string, error) {
	rootFile, _, err := openDirectoryNoFollow(root)
	if err != nil {
		return nil, "", fmt.Errorf("opening trusted artifact root %s: %w", root, err)
	}
	defer rootFile.Close()
	return openOrCreateParentAt(rootFile, relative)
}

func openOrCreateParentAt(root *os.File, relative string) (*os.File, string, error) {
	if relative == "" || filepath.IsAbs(relative) || filepath.Clean(relative) != relative {
		return nil, "", fmt.Errorf("unsafe artifact path %q", relative)
	}
	parts := strings.Split(filepath.Clean(relative), string(filepath.Separator))
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, "", fmt.Errorf("unsafe artifact path %q", relative)
		}
	}
	if root == nil {
		return nil, "", fmt.Errorf("artifact root is nil")
	}
	rootFD, err := unix.Dup(int(root.Fd()))
	if err != nil {
		return nil, "", fmt.Errorf("duplicating trusted artifact root: %w", err)
	}
	current := os.NewFile(uintptr(rootFD), root.Name())
	if current == nil {
		_ = unix.Close(rootFD)
		return nil, "", fmt.Errorf("holding trusted artifact root")
	}
	for _, part := range parts[:len(parts)-1] {
		next, _, openErr := openChildNoFollow(current, part, true)
		if openErr != nil && os.IsNotExist(openErr) {
			mkdirErr := unix.Mkdirat(int(current.Fd()), part, 0o755)
			if mkdirErr != nil && mkdirErr != unix.EEXIST {
				_ = current.Close()
				return nil, "", fmt.Errorf("creating artifact directory %s: %w", part, mkdirErr)
			}
			if mkdirErr == nil {
				if syncErr := current.Sync(); syncErr != nil {
					_ = current.Close()
					return nil, "", fmt.Errorf("persisting artifact directory %s: %w", part, syncErr)
				}
			}
			next, _, openErr = openChildNoFollow(current, part, true)
		}
		if openErr != nil {
			_ = current.Close()
			return nil, "", fmt.Errorf("opening artifact directory %s: %w", part, openErr)
		}
		_ = current.Close()
		current = next
	}
	return current, parts[len(parts)-1], nil
}

func createTemporarySibling(parent *os.File) (*os.File, string, error) {
	if parent == nil {
		return nil, "", fmt.Errorf("artifact parent directory is nil")
	}
	for attempt := 0; attempt < 32; attempt++ {
		var token [12]byte
		if _, err := rand.Read(token[:]); err != nil {
			return nil, "", err
		}
		name := ".nkd-download-" + hex.EncodeToString(token[:])
		fd, err := unix.Openat(int(parent.Fd()), name,
			unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
		if err == unix.EEXIST {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		file := os.NewFile(uintptr(fd), name)
		if file == nil {
			_ = unix.Close(fd)
			_ = unix.Unlinkat(int(parent.Fd()), name, 0)
			return nil, "", fmt.Errorf("opening private artifact download")
		}
		return file, name, nil
	}
	return nil, "", fmt.Errorf("could not allocate a private artifact download name")
}

// openRegularBeneath resolves every untrusted path component relative to an
// already-open directory descriptor. O_NOFOLLOW on each hop prevents a producer
// from replacing an output directory or the final artifact with a host symlink.
func openRegularBeneath(root, relative string) (*os.File, os.FileInfo, error) {
	if relative == "" || filepath.IsAbs(relative) || filepath.Clean(relative) != relative {
		return nil, nil, fmt.Errorf("unsafe artifact path %q", relative)
	}
	parts := strings.Split(filepath.Clean(relative), string(filepath.Separator))
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, nil, fmt.Errorf("unsafe artifact path %q", relative)
		}
	}
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("opening trusted artifact root %s: %w", root, err)
	}
	currentFD := rootFD
	for i, part := range parts {
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
		if i != len(parts)-1 {
			flags |= unix.O_DIRECTORY
		}
		nextFD, openErr := unix.Openat(currentFD, part, flags, 0)
		if currentFD != rootFD {
			_ = unix.Close(currentFD)
		}
		if openErr != nil {
			_ = unix.Close(rootFD)
			return nil, nil, fmt.Errorf("opening artifact %s beneath %s: %w", relative, root, openErr)
		}
		currentFD = nextFD
	}
	_ = unix.Close(rootFD)
	f := os.NewFile(uintptr(currentFD), filepath.Join(root, relative))
	if f == nil {
		_ = unix.Close(currentFD)
		return nil, nil, fmt.Errorf("opening artifact %s beneath %s", relative, root)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, fmt.Errorf("artifact %s beneath %s is not a regular file", relative, root)
	}
	return f, info, nil
}

func openDirectoryNoFollow(path string) (*os.File, os.FileInfo, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	if f == nil {
		_ = unix.Close(fd)
		return nil, nil, fmt.Errorf("opening directory %s", path)
	}
	info, err := f.Stat()
	if err != nil || !info.IsDir() {
		_ = f.Close()
		return nil, nil, fmt.Errorf("path %s is not a real directory", path)
	}
	return f, info, nil
}

// openRegularNoFollow holds one exact pathname inode without allowing a final
// symlink or a FIFO/device substitution to block the caller during admission.
func openRegularNoFollow(path string) (*os.File, os.FileInfo, error) {
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if !pathInfo.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("path %s is not a regular file", path)
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, nil, fmt.Errorf("holding regular file %s", path)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(pathInfo, info) {
		_ = file.Close()
		return nil, nil, fmt.Errorf("regular file %s changed while opening", path)
	}
	return file, info, nil
}

func openChildNoFollow(parent *os.File, name string, directory bool) (*os.File, os.FileInfo, error) {
	if parent == nil || name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return nil, nil, fmt.Errorf("unsafe child path %q", name)
	}
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	if directory {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Openat(int(parent.Fd()), name, flags, 0)
	if err != nil {
		return nil, nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	if f == nil {
		_ = unix.Close(fd)
		return nil, nil, fmt.Errorf("opening child %q", name)
	}
	info, err := f.Stat()
	if err != nil || (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		_ = f.Close()
		return nil, nil, fmt.Errorf("child %q has an unexpected file type", name)
	}
	return f, info, nil
}

func hashOpenRegularContext(ctx context.Context, f *os.File, before os.FileInfo) (string, int64, os.FileInfo, error) {
	if ctx == nil {
		return "", 0, nil, fmt.Errorf("hashing a regular file requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return "", 0, nil, err
	}
	if f == nil || before == nil || !before.Mode().IsRegular() {
		return "", 0, nil, fmt.Errorf("cannot hash a non-regular file")
	}
	h := sha256.New()
	size, copyErr := copyPipelineContext(ctx, h, f)
	after, statErr := f.Stat()
	if copyErr != nil {
		return "", 0, nil, copyErr
	}
	if statErr != nil {
		return "", 0, nil, statErr
	}
	if !os.SameFile(before, after) || size != before.Size() || after.Size() != before.Size() ||
		!after.ModTime().Equal(before.ModTime()) || after.Mode() != before.Mode() {
		return "", 0, nil, fmt.Errorf("file changed while hashing")
	}
	if err := ctx.Err(); err != nil {
		return "", 0, nil, err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), size, after, nil
}

func hashRegularBeneathContext(ctx context.Context, root, relative string) (string, int64, os.FileInfo, error) {
	if ctx == nil {
		return "", 0, nil, fmt.Errorf("hashing an artifact requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return "", 0, nil, err
	}
	f, before, err := openRegularBeneath(root, relative)
	if err != nil {
		return "", 0, nil, err
	}
	digest, size, after, hashErr := hashOpenRegularContext(ctx, f, before)
	closeErr := f.Close()
	if hashErr != nil {
		return "", 0, nil, hashErr
	}
	if closeErr != nil {
		return "", 0, nil, closeErr
	}
	return digest, size, after, nil
}

type pipelineContextReader struct {
	ctx    context.Context
	source io.Reader
}

func copyPipelineContext(ctx context.Context, destination io.Writer, source io.Reader) (int64, error) {
	if ctx == nil {
		return 0, fmt.Errorf("copy requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	buffer := pipelineCopyBufferPool.Get().(*[]byte)
	defer pipelineCopyBufferPool.Put(buffer)
	return io.CopyBuffer(destination, pipelineContextReader{ctx: ctx, source: source}, *buffer)
}

func (reader pipelineContextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.source.Read(buffer)
}

func relativeBeneath(root, target string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(absRoot, absTarget)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("artifact path %q is not beneath trusted root %q", target, root)
	}
	if filepath.Clean(relative) != relative {
		return "", fmt.Errorf("artifact path %q is not canonical beneath trusted root", target)
	}
	return relative, nil
}
