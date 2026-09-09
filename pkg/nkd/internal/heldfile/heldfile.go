// Package heldfile owns the narrow OS boundary for qualified subprocess inputs.
// It turns exact bytes into anonymous file capabilities and exposes them to a
// child only through inherited descriptors. Callers never need to publish a
// temporary pathname that a same-UID process can rename or replace. Immutable
// input/output holding supports Linux and Darwin. Executable capabilities are
// Linux-only because Darwin's /dev/fd is noexec; unsupported execution fails closed.
package heldfile

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nhensley/nkdctl/pkg/nkd/canonical"
)

var (
	ErrUnsupported = errors.New("anonymous held files are unsupported on this platform")
)

// Options fixes the admission policy for one held file. ExpectedDigest is an
// external authority pin; when non-empty it must be exact lowercase SHA-256.
type Options struct {
	Label          string
	Executable     bool
	MaxBytes       int64
	ExpectedDigest string
}

// Descriptor is an anonymous regular file that may be inherited by a child.
// Only File and Output from this package implement it.
type Descriptor interface {
	OSFile() *os.File
	heldDescriptor()
	commandIdentity() (uint64, *sync.Mutex)
}

// File is an immutable anonymous regular-file capability.
type File struct {
	mu         sync.Mutex
	runMu      sync.Mutex
	id         uint64
	file       *os.File
	size       int64
	digest     string
	label      string
	executable bool
}

// Open snapshots one opened regular file, validates its bounds and optional
// external digest pin, and returns an anonymous immutable capability.
func Open(path string, options Options) (*File, error) {
	return OpenContext(context.Background(), path, options)
}

// OpenContext is Open with cancellation observed throughout the snapshot copy.
func OpenContext(ctx context.Context, path string, options Options) (*File, error) {
	if ctx == nil {
		return nil, errors.New("held input requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Reject unsupported executable authority before resolving or opening the
	// caller's path. Authority adapters can use RequireExecutableSupport directly
	// when they must fail before touching any other request input.
	if options.Executable {
		if err := RequireExecutableSupport(); err != nil {
			return nil, err
		}
	}
	if options.MaxBytes <= 0 {
		return nil, fmt.Errorf("held input %q has no positive size bound", path)
	}
	source, err := platformOpenSource(path)
	if err != nil {
		return nil, fmt.Errorf("open held input %q: %w", path, err)
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat held input %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("held input %q is not a regular file", path)
	}
	if options.Executable && info.Mode().Perm()&0o111 == 0 {
		return nil, fmt.Errorf("held executable %q has no execute bit", path)
	}
	if info.Size() < 0 || info.Size() > options.MaxBytes {
		return nil, fmt.Errorf("held input %q exceeds its %d-byte bound", path, options.MaxBytes)
	}
	return fromReader(ctx, source, options)
}

// FromBytes creates an immutable anonymous capability containing a defensive copy.
func FromBytes(raw []byte, options Options) (*File, error) {
	return FromBytesContext(context.Background(), raw, options)
}

// FromBytesContext creates an immutable capability while observing cancellation
// during the defensive copy and seal.
func FromBytesContext(ctx context.Context, raw []byte, options Options) (*File, error) {
	if options.MaxBytes <= 0 || int64(len(raw)) > options.MaxBytes {
		return nil, fmt.Errorf("held %s exceeds its %d-byte bound", cleanLabel(options.Label), options.MaxBytes)
	}
	return fromReader(ctx, bytes.NewReader(raw), options)
}

// FromReader streams the caller's already-opened capability into an anonymous
// immutable file. It never resolves a pathname and does not close source.
func FromReader(source io.Reader, options Options) (*File, error) {
	return FromReaderContext(context.Background(), source, options)
}

// FromReaderContext is FromReader with cancellation observed between chunks.
func FromReaderContext(ctx context.Context, source io.Reader, options Options) (*File, error) {
	if ctx == nil {
		return nil, errors.New("held input reader requires a non-nil context")
	}
	if source == nil {
		return nil, errors.New("held input reader is nil")
	}
	return fromReader(ctx, source, options)
}

// RequireExecutableSupport reports whether this platform can execute an exact
// anonymous held capability without publishing a mutable pathname. Linux is the
// only supported executable-authority platform; other platforms fail closed.
func RequireExecutableSupport() error {
	if !platformSupportsExecutable() {
		return ErrUnsupported
	}
	return nil
}

func fromReader(ctx context.Context, source io.Reader, options Options) (*File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	label := cleanLabel(options.Label)
	if options.Executable {
		if err := RequireExecutableSupport(); err != nil {
			return nil, err
		}
	}
	if options.MaxBytes <= 0 {
		return nil, fmt.Errorf("held %s has no positive size bound", label)
	}
	if options.ExpectedDigest != "" && !canonical.IsDigest(options.ExpectedDigest) {
		return nil, fmt.Errorf("held %s expected digest is not exact lowercase SHA-256", label)
	}
	staging, err := newMutable(label)
	if err != nil {
		return nil, err
	}
	size, digest, err := copySparseHash(ctx, staging, source, options.MaxBytes)
	if err != nil {
		_ = staging.Close()
		return nil, fmt.Errorf("snapshot held %s: %w", label, err)
	}
	if options.ExpectedDigest != "" && digest != options.ExpectedDigest {
		_ = staging.Close()
		return nil, fmt.Errorf("held %s digest %s does not match pinned %s", label, digest, options.ExpectedDigest)
	}
	frozen, err := freezeMutable(ctx, staging, label, options.Executable, size)
	if err != nil {
		_ = staging.Close()
		return nil, err
	}
	return &File{id: nextID(), file: frozen, size: size, digest: digest,
		label: label, executable: options.Executable}, nil
}

func (f *File) heldDescriptor() {}
func (f *File) commandIdentity() (uint64, *sync.Mutex) {
	if f == nil {
		return 0, nil
	}
	return f.id, &f.runMu
}

// OSFile returns the read-only descriptor for inheritance. It does not transfer
// ownership; Close remains the caller's responsibility.
func (f *File) OSFile() *os.File {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.file
}

func (f *File) Digest() string {
	if f == nil {
		return ""
	}
	return f.digest
}

func (f *File) Size() int64 {
	if f == nil {
		return 0
	}
	return f.size
}

// ReadAll returns the exact held bytes only when they fit the caller's explicit
// memory bound. Large disk callers should use ReadAt or WriteTo.
func (f *File) ReadAll(maxBytes int64) ([]byte, error) {
	return f.ReadAllContext(context.Background(), maxBytes)
}

// ReadAllContext returns the exact held bytes within an explicit memory bound,
// observing cancellation while waiting for and reading the capability.
func (f *File) ReadAllContext(ctx context.Context, maxBytes int64) ([]byte, error) {
	if ctx == nil {
		return nil, errors.New("reading held file requires a non-nil context")
	}
	if f == nil || maxBytes < 0 || f.size > maxBytes {
		return nil, fmt.Errorf("held file exceeds the %d-byte read bound", maxBytes)
	}
	if err := lockMutexContext(ctx, &f.mu); err != nil {
		return nil, err
	}
	defer f.mu.Unlock()
	if f.file == nil {
		return nil, errors.New("held file is closed")
	}
	var destination bytes.Buffer
	if f.size <= int64(^uint(0)>>1) {
		destination.Grow(int(f.size))
	}
	if _, err := copyContext(ctx, &destination, io.NewSectionReader(f.file, 0, f.size)); err != nil {
		return nil, err
	}
	return destination.Bytes(), nil
}

func (f *File) ReadAt(p []byte, offset int64) (int, error) {
	if f == nil {
		return 0, errors.New("nil held file")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.file == nil {
		return 0, errors.New("held file is closed")
	}
	return f.file.ReadAt(p, offset)
}

func (f *File) WriteTo(destination io.Writer) (int64, error) {
	return f.WriteToContext(context.Background(), destination)
}

// WriteToContext writes the exact held bytes and observes cancellation between
// bounded chunks.
func (f *File) WriteToContext(ctx context.Context, destination io.Writer) (int64, error) {
	if ctx == nil {
		return 0, errors.New("writing held file requires a non-nil context")
	}
	if f == nil || destination == nil {
		return 0, errors.New("held file and destination are required")
	}
	if err := lockMutexContext(ctx, &f.mu); err != nil {
		return 0, err
	}
	defer f.mu.Unlock()
	if f.file == nil {
		return 0, errors.New("held file is closed")
	}
	return copyContext(ctx, destination, io.NewSectionReader(f.file, 0, f.size))
}

// AssertUnchanged re-reads the held inode and compares it with the captured bytes.
func (f *File) AssertUnchanged() error {
	return f.AssertUnchangedContext(context.Background())
}

// AssertUnchangedContext re-hashes the held inode with bounded cancellation.
func (f *File) AssertUnchangedContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("verifying held file requires a non-nil context")
	}
	if f == nil {
		return errors.New("nil held file")
	}
	if err := lockMutexContext(ctx, &f.mu); err != nil {
		return err
	}
	defer f.mu.Unlock()
	if f.file == nil {
		return errors.New("held file is closed")
	}
	info, err := f.file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != f.size {
		return fmt.Errorf("held %s changed", f.label)
	}
	hash := sha256.New()
	if _, err := copyContext(ctx, hash, io.NewSectionReader(f.file, 0, info.Size())); err != nil {
		return err
	}
	if "sha256:"+hex.EncodeToString(hash.Sum(nil)) != f.digest {
		return fmt.Errorf("held %s changed", f.label)
	}
	return nil
}

func (f *File) Close() error {
	if f == nil {
		return nil
	}
	f.runMu.Lock()
	defer f.runMu.Unlock()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.file == nil {
		return nil
	}
	err := f.file.Close()
	f.file = nil
	return err
}

// Output is an anonymous mutable child output. Seal must be called after the child
// exits; it checks the bound and transfers ownership to an immutable File.
type Output struct {
	mu       sync.Mutex
	runMu    sync.Mutex
	id       uint64
	file     *os.File
	label    string
	maxBytes int64
}

func NewOutput(label string, maxBytes int64) (*Output, error) {
	if maxBytes <= 0 {
		return nil, errors.New("held output requires a positive size bound")
	}
	f, err := newMutable(cleanLabel(label))
	if err != nil {
		return nil, err
	}
	return &Output{id: nextID(), file: f, label: cleanLabel(label), maxBytes: maxBytes}, nil
}

func (o *Output) heldDescriptor() {}
func (o *Output) commandIdentity() (uint64, *sync.Mutex) {
	if o == nil {
		return 0, nil
	}
	return o.id, &o.runMu
}

func (o *Output) OSFile() *os.File {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.file
}

func (o *Output) Seal() (*File, error) {
	return o.SealContext(context.Background())
}

// SealContext freezes and hashes a held output while observing cancellation.
func (o *Output) SealContext(ctx context.Context) (*File, error) {
	if ctx == nil {
		return nil, errors.New("sealing held output requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if o == nil {
		return nil, errors.New("nil held output")
	}
	if err := lockMutexContext(ctx, &o.runMu); err != nil {
		return nil, err
	}
	defer o.runMu.Unlock()
	if err := lockMutexContext(ctx, &o.mu); err != nil {
		return nil, err
	}
	defer o.mu.Unlock()
	if o.file == nil {
		return nil, errors.New("held output is already sealed or closed")
	}
	info, err := o.file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > o.maxBytes {
		return nil, fmt.Errorf("held output %s is not a bounded regular file", o.label)
	}
	sealed, err := freezeMutable(ctx, o.file, o.label, false, info.Size())
	if err != nil {
		return nil, err
	}
	o.file = nil
	sealedInfo, err := sealed.Stat()
	if err != nil || !sealedInfo.Mode().IsRegular() || sealedInfo.Size() < 0 || sealedInfo.Size() > o.maxBytes {
		_ = sealed.Close()
		return nil, fmt.Errorf("sealed output %s is not a bounded regular file", o.label)
	}
	size := sealedInfo.Size()
	hash := sha256.New()
	if _, err := copyContext(ctx, hash, io.NewSectionReader(sealed, 0, size)); err != nil {
		_ = sealed.Close()
		return nil, fmt.Errorf("hash sealed output %s: %w", o.label, err)
	}
	return &File{id: nextID(), file: sealed, size: size,
		digest: "sha256:" + hex.EncodeToString(hash.Sum(nil)), label: o.label}, nil
}

func (o *Output) Close() error {
	if o == nil {
		return nil
	}
	o.runMu.Lock()
	defer o.runMu.Unlock()
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.file == nil {
		return nil
	}
	err := o.file.Close()
	o.file = nil
	return err
}

// ChildPath returns the path visible in the child for an ExtraFiles slice index.
// Index zero is child descriptor 3.
func ChildPath(extraFilesIndex int) (string, error) {
	if extraFilesIndex < 0 {
		return "", errors.New("negative inherited descriptor index")
	}
	return platformChildPath(3 + extraFilesIndex)
}

// commandContext constructs a held command after RunContext has locked and rewound
// every descriptor. Keeping this private prevents callers from bypassing loader
// environment sanitization or descriptor serialization.
func commandContext(ctx context.Context, executable *File, args []string, stdin io.Reader, inputs ...Descriptor) (*exec.Cmd, error) {
	if ctx == nil {
		return nil, errors.New("held command requires a non-nil context")
	}
	if executable == nil || !executable.executable || executable.OSFile() == nil {
		return nil, errors.New("held command requires an open executable capability")
	}
	path, commandArgs, err := platformCommand(args)
	if err != nil {
		return nil, err
	}
	extra := make([]*os.File, 0, 1+len(inputs))
	extra = append(extra, executable.OSFile())
	for _, input := range inputs {
		if input == nil || input.OSFile() == nil {
			return nil, errors.New("held command has a nil or closed inherited descriptor")
		}
		extra = append(extra, input.OSFile())
	}
	cmd := exec.CommandContext(ctx, path, commandArgs...)
	cmd.Stdin = stdin
	cmd.ExtraFiles = extra
	cmd.Env = SanitizedEnvironment(os.Environ())
	return cmd, nil
}

// RunOptions configures held descriptor execution without exposing an opportunity
// to replace the sanitized environment.
type RunOptions struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Env    []string
	Dir    string
	// TerminationGrace bounds the interval between SIGTERM and SIGKILL after
	// context cancellation. Zero selects the package default.
	TerminationGrace time.Duration
}

const defaultTerminationGrace = 10 * time.Second

// RunContext serializes every shared input/output descriptor for the duration of
// execution, rewinds it before and after use, and strips loader-injection
// variables. The executable is serialized only through Start, after which the
// kernel has consumed its capability. This is the concurrency-safe execution
// entrypoint for reusable held capabilities.
func RunContext(ctx context.Context, executable *File, args []string, options RunOptions, inputs ...Descriptor) error {
	if ctx == nil {
		return errors.New("held command requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	terminationGrace := options.TerminationGrace
	if terminationGrace == 0 {
		terminationGrace = defaultTerminationGrace
	}
	if terminationGrace < 0 {
		return errors.New("held command termination grace cannot be negative")
	}
	descriptors := make([]Descriptor, 0, 1+len(inputs))
	descriptors = append(descriptors, executable)
	descriptors = append(descriptors, inputs...)
	type lockRef struct {
		id uint64
		mu *sync.Mutex
	}
	locks := make([]lockRef, 0, len(descriptors))
	seen := make(map[uint64]bool, len(descriptors))
	inputIDs := make(map[uint64]bool, len(inputs))
	for _, input := range inputs {
		if input != nil {
			id, _ := input.commandIdentity()
			inputIDs[id] = true
		}
	}
	for _, descriptor := range descriptors {
		if descriptor == nil {
			return errors.New("held command has a nil descriptor")
		}
		id, mu := descriptor.commandIdentity()
		if id == 0 || mu == nil || seen[id] {
			continue
		}
		seen[id] = true
		locks = append(locks, lockRef{id: id, mu: mu})
	}
	sort.Slice(locks, func(i, j int) bool { return locks[i].id < locks[j].id })
	for index, lock := range locks {
		if err := lockMutexContext(ctx, lock.mu); err != nil {
			for acquired := index - 1; acquired >= 0; acquired-- {
				locks[acquired].mu.Unlock()
			}
			return err
		}
	}
	locked := make(map[uint64]bool, len(locks))
	for _, lock := range locks {
		locked[lock.id] = true
	}
	defer func() {
		for i := len(locks) - 1; i >= 0; i-- {
			if locked[locks[i].id] {
				locks[i].mu.Unlock()
			}
		}
	}()
	for _, descriptor := range descriptors {
		if file := descriptor.OSFile(); file == nil {
			return errors.New("held command has a closed descriptor")
		} else if _, err := file.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("rewind held command descriptor: %w", err)
		}
	}
	cmd, err := commandContext(ctx, executable, args, options.Stdin, inputs...)
	if err != nil {
		return err
	}
	configureCommandCancellation(cmd, terminationGrace)
	cmd.Stdout = options.Stdout
	cmd.Stderr = options.Stderr
	cmd.Dir = options.Dir
	if options.Env != nil {
		cmd.Env = SanitizedEnvironment(options.Env)
	}
	if err := cmd.Start(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return err
	}
	// The kernel consumed the executable capability during Start. Release that
	// lock while the child runs unless the same File is also an input; this keeps
	// concurrent DAG jobs from serializing for the duration of a long build.
	executableID, _ := executable.commandIdentity()
	if !inputIDs[executableID] {
		for _, lock := range locks {
			if lock.id == executableID && locked[lock.id] {
				lock.mu.Unlock()
				locked[lock.id] = false
				break
			}
		}
	}
	err = cmd.Wait()
	// A held command owns its entire private process group. Descendants must not
	// survive even when the direct child exits successfully after daemonizing.
	cleanupErr := killCommandGroup(cmd)
	for _, descriptor := range inputs {
		if file := descriptor.OSFile(); file != nil {
			_, _ = file.Seek(0, io.SeekStart)
		}
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if err != nil {
		return err
	}
	if cleanupErr != nil && !errors.Is(cleanupErr, os.ErrProcessDone) {
		return cleanupErr
	}
	return nil
}

// SanitizedEnvironment removes dynamic-loader injection controls. It cannot pin a
// platform dynamic loader or shared-library closure; authority callers must use a
// static executable where that stronger property is required.
func SanitizedEnvironment(base []string) []string {
	out := make([]string, 0, len(base))
	for _, entry := range base {
		name := entry
		if i := strings.IndexByte(entry, '='); i >= 0 {
			name = entry[:i]
		}
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "LD_") || strings.HasPrefix(upper, "DYLD_") ||
			upper == "GLIBC_TUNABLES" || upper == "LIBPATH" || upper == "SHLIB_PATH" {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func cleanLabel(label string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return "input"
	}
	var b strings.Builder
	for _, r := range label {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
		if b.Len() >= 48 {
			break
		}
	}
	return b.String()
}

var heldID atomic.Uint64

func nextID() uint64 { return heldID.Add(1) }

const sparseCopyBlock = 1 << 20

// copySparseHash streams through bounded memory and represents all-zero blocks as
// holes while hashing the exact logical byte stream.
func copySparseHash(ctx context.Context, destination *os.File, source io.Reader, maxBytes int64) (int64, string, error) {
	buffer := make([]byte, sparseCopyBlock)
	hash := sha256.New()
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return 0, "", err
		}
		n, readErr := source.Read(buffer)
		if n > 0 {
			total += int64(n)
			if total > maxBytes {
				return 0, "", fmt.Errorf("input exceeds %d-byte bound", maxBytes)
			}
			chunk := buffer[:n]
			if _, err := hash.Write(chunk); err != nil {
				return 0, "", err
			}
			if allZero(chunk) {
				if _, err := destination.Seek(int64(n), io.SeekCurrent); err != nil {
					return 0, "", err
				}
			} else if _, err := destination.Write(chunk); err != nil {
				return 0, "", err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return 0, "", readErr
		}
	}
	if err := destination.Truncate(total); err != nil {
		return 0, "", err
	}
	if _, err := destination.Seek(0, io.SeekStart); err != nil {
		return 0, "", err
	}
	return total, "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func copyContext(ctx context.Context, destination io.Writer, source io.Reader) (int64, error) {
	if ctx == nil {
		return 0, errors.New("copy requires a non-nil context")
	}
	return io.CopyBuffer(destination, contextReader{ctx: ctx, source: source}, make([]byte, sparseCopyBlock))
}

func lockMutexContext(ctx context.Context, mutex *sync.Mutex) error {
	if ctx == nil {
		return errors.New("held operation requires a non-nil context")
	}
	if mutex == nil {
		return errors.New("held operation has no lock")
	}
	for {
		if mutex.TryLock() {
			return nil
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

type contextReader struct {
	ctx    context.Context
	source io.Reader
}

func (r contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(buffer)
}

func allZero(raw []byte) bool {
	for _, value := range raw {
		if value != 0 {
			return false
		}
	}
	return true
}
