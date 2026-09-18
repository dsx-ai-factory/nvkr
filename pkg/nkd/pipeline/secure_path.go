package pipeline

import (
	"context"
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
