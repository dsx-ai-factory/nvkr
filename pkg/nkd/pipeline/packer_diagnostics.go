package pipeline

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/NVIDIA/nke-recipes/internal/securepath"
	"github.com/NVIDIA/nke-recipes/internal/transportarchive"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
	"golang.org/x/sys/unix"
)

const (
	packerDiagnosticDirectory       = "diagnostics"
	packerDiagnosticProducerDir     = "packer"
	packerDiagnosticManifestName    = "material-manifest.json"
	packerDiagnosticLogsName        = "logs"
	maxPackerDiagnosticEntries      = 128
	maxPackerDiagnosticFiles        = 64
	maxPackerDiagnosticFileBytes    = int64(16 << 20)
	maxPackerDiagnosticPayloadBytes = int64(64 << 20)
	maxPackerDiagnosticArchiveBytes = maxPackerDiagnosticPayloadBytes + (1 << 20)
)

type packerDiagnosticLimits struct {
	entries, files int
	fileBytes      int64
	payloadBytes   int64
}

var defaultPackerDiagnosticLimits = packerDiagnosticLimits{
	entries: maxPackerDiagnosticEntries, files: maxPackerDiagnosticFiles,
	fileBytes: maxPackerDiagnosticFileBytes, payloadBytes: maxPackerDiagnosticPayloadBytes,
}

var errPackerDiagnosticsIncomplete = errors.New("packer diagnostics are incomplete")

// PackerDiagnosticSink is a process-local capability for retaining bounded,
// non-authoritative Packer diagnostics beneath one already-held lifecycle
// attempt directory. It accepts no path and cannot be recreated from serialized
// data. The lifecycle owner closes it before committing the immutable Run record.
type PackerDiagnosticSink struct {
	mu            sync.Mutex
	attemptRoot   *os.File
	diagnostics   *os.File
	packer        *os.File
	closed        bool
	sealed        bool
	retainedPaths map[string]*packerDiagnosticTarget
}

type packerDiagnosticTarget struct {
	mu     sync.Mutex
	root   *os.File
	closed bool
	sealed bool
}

// NewPackerDiagnosticSink retains the exact held attempt-directory inode. It is
// intentionally lazy and creates no path until an admitted direct-Packer run has
// an exact manifest to retain.
func NewPackerDiagnosticSink(ctx context.Context, attemptRoot *os.File) (*PackerDiagnosticSink, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "Packer diagnostic retention requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "opening Packer diagnostic retention")
	}
	if attemptRoot == nil {
		return nil, e.New(e.CodeEngineCapability,
			"Packer diagnostic retention requires a held attempt directory")
	}
	info, err := attemptRoot.Stat()
	if err != nil || !info.IsDir() {
		if err == nil {
			err = errors.New("held attempt root is not a directory")
		}
		return nil, e.Wrap(e.CodeEngineCapability, err,
			"validating held Packer diagnostic attempt directory")
	}
	fd, err := unix.Dup(int(attemptRoot.Fd()))
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err,
			"retaining held Packer diagnostic attempt directory")
	}
	held := os.NewFile(uintptr(fd), "packer-diagnostic-attempt")
	if held == nil {
		_ = unix.Close(fd)
		return nil, e.New(e.CodeEngineCapability,
			"retaining held Packer diagnostic attempt directory")
	}
	return &PackerDiagnosticSink{attemptRoot: held}, nil
}

func (sink *PackerDiagnosticSink) start(ctx context.Context, subject string, manifest []byte,
	digest string,
) (*packerDiagnosticTarget, error) {
	if sink == nil {
		return nil, e.New(e.CodeEngineCapability, "Packer diagnostic sink is absent")
	}
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "retaining Packer diagnostics requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "retaining Packer material manifest")
	}
	if subject == "" {
		return nil, e.New(e.CodeEngineCapability,
			"Packer diagnostic retention requires the admitted Plan subject")
	}
	if err := validatePackerContainerManifest(manifest, digest); err != nil {
		return nil, err
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.closed || sink.attemptRoot == nil {
		return nil, e.New(e.CodeEngineCapability, "Packer diagnostic sink is closed")
	}
	if err := sink.ensureRoots(); err != nil {
		return nil, err
	}
	// This SHA-256 is only collision-resistant private path routing for an exact
	// already-admitted subject. It is not a typed identity or authority receipt.
	route := sha256.Sum256([]byte(subject))
	name := "run-" + hex.EncodeToString(route[:16])
	if err := unix.Mkdirat(int(sink.packer.Fd()), name, 0o700); err != nil {
		return nil, e.Wrap(e.CodeImmutableViolation, err,
			"reserving no-replace Packer diagnostic route")
	}
	root, _, err := securepath.OpenChild(sink.packer, name, true)
	if err != nil {
		_ = unix.Unlinkat(int(sink.packer.Fd()), name, unix.AT_REMOVEDIR)
		return nil, e.Wrap(e.CodeImmutableViolation, err,
			"holding Packer diagnostic route")
	}
	if err := writePackerDiagnosticManifest(ctx, root, manifest, digest); err != nil {
		_ = root.Close()
		_ = removePackerDiagnosticTree(sink.packer, name)
		return nil, err
	}
	if err := sink.packer.Sync(); err != nil {
		_ = root.Close()
		_ = removePackerDiagnosticTree(sink.packer, name)
		return nil, e.Wrap(e.CodeImmutableViolation, err,
			"persisting Packer diagnostic route")
	}
	target := &packerDiagnosticTarget{root: root}
	if sink.retainedPaths == nil {
		sink.retainedPaths = make(map[string]*packerDiagnosticTarget)
	}
	sink.retainedPaths[path.Join(packerDiagnosticDirectory, packerDiagnosticProducerDir, name)] = target
	return target, nil
}

// RetainedPath describes one successfully sealed, sink-issued diagnostic route,
// never an artifact or receipt. It is absent before Close, after failed retention
// or sealing, and when several producers would make the route ambiguous.
func (sink *PackerDiagnosticSink) RetainedPath() string {
	if sink == nil {
		return ""
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if !sink.closed || !sink.sealed || len(sink.retainedPaths) != 1 {
		return ""
	}
	for route, target := range sink.retainedPaths {
		target.mu.Lock()
		sealed := target.closed && target.sealed
		target.mu.Unlock()
		if sealed {
			return route
		}
	}
	return ""
}

func (sink *PackerDiagnosticSink) ensureRoots() error {
	if sink.diagnostics != nil && sink.packer != nil {
		return nil
	}
	if err := unix.Mkdirat(int(sink.attemptRoot.Fd()), packerDiagnosticDirectory, 0o700); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err,
			"reserving no-replace Packer diagnostic directory")
	}
	diagnostics, _, err := securepath.OpenChild(sink.attemptRoot, packerDiagnosticDirectory, true)
	if err != nil {
		_ = unix.Unlinkat(int(sink.attemptRoot.Fd()), packerDiagnosticDirectory, unix.AT_REMOVEDIR)
		return e.Wrap(e.CodeImmutableViolation, err,
			"holding Packer diagnostic directory")
	}
	if err := unix.Mkdirat(int(diagnostics.Fd()), packerDiagnosticProducerDir, 0o700); err != nil {
		_ = diagnostics.Close()
		_ = unix.Unlinkat(int(sink.attemptRoot.Fd()), packerDiagnosticDirectory, unix.AT_REMOVEDIR)
		return e.Wrap(e.CodeImmutableViolation, err,
			"reserving no-replace Packer producer diagnostics")
	}
	packer, _, err := securepath.OpenChild(diagnostics, packerDiagnosticProducerDir, true)
	if err != nil {
		_ = unix.Unlinkat(int(diagnostics.Fd()), packerDiagnosticProducerDir, unix.AT_REMOVEDIR)
		_ = diagnostics.Close()
		_ = unix.Unlinkat(int(sink.attemptRoot.Fd()), packerDiagnosticDirectory, unix.AT_REMOVEDIR)
		return e.Wrap(e.CodeImmutableViolation, err,
			"holding Packer producer diagnostics")
	}
	sink.diagnostics, sink.packer = diagnostics, packer
	return nil
}

func writePackerDiagnosticManifest(ctx context.Context, root *os.File, manifest []byte,
	digest string,
) (returnErr error) {
	held, err := heldfile.FromBytesContext(ctx, manifest, heldfile.Options{
		Label: "packer-diagnostic-material-manifest", MaxBytes: maxBuildMaterialManifestBytes,
		ExpectedDigest: digest,
	})
	if err != nil {
		return e.Wrap(e.CodeUnverifiedRef, err,
			"holding exact Packer diagnostic material manifest")
	}
	defer func() {
		if err := held.Close(); err != nil {
			returnErr = errors.Join(returnErr,
				e.Wrap(e.CodeInternal, err, "closing held Packer diagnostic material manifest"))
		}
	}()
	fd, err := unix.Openat(int(root.Fd()), packerDiagnosticManifestName,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err,
			"creating no-replace Packer diagnostic material manifest")
	}
	file := os.NewFile(uintptr(fd), packerDiagnosticManifestName)
	if file == nil {
		_ = unix.Close(fd)
		_ = unix.Unlinkat(int(root.Fd()), packerDiagnosticManifestName, 0)
		return e.New(e.CodeInternal, "holding Packer diagnostic material manifest output")
	}
	written, writeErr := held.WriteToContext(ctx, file)
	if writeErr == nil && written != int64(len(manifest)) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if writeErr == nil {
		writeErr = file.Chmod(0o444)
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		_ = unix.Unlinkat(int(root.Fd()), packerDiagnosticManifestName, 0)
		return packerDiagnosticError(ctx, e.CodeImmutableViolation, writeErr,
			"writing Packer diagnostic material manifest")
	}
	if err := held.AssertUnchangedContext(ctx); err != nil {
		_ = unix.Unlinkat(int(root.Fd()), packerDiagnosticManifestName, 0)
		return e.Wrap(e.CodeUnverifiedRef, err,
			"rechecking held Packer diagnostic material manifest")
	}
	retained, info, err := securepath.OpenChild(root, packerDiagnosticManifestName, false)
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err,
			"holding retained Packer diagnostic material manifest")
	}
	gotDigest, gotSize, _, hashErr := hashOpenRegularContext(ctx, retained, info)
	closeErr = retained.Close()
	if hashErr != nil || closeErr != nil {
		return e.Wrap(e.CodeUnverifiedRef, errors.Join(hashErr, closeErr),
			"rechecking retained Packer diagnostic material manifest")
	}
	if gotDigest != digest || gotSize != int64(len(manifest)) {
		return e.New(e.CodeUnverifiedRef,
			"retained Packer diagnostic material manifest changed after write")
	}
	if err := root.Sync(); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err,
			"persisting Packer diagnostic material manifest")
	}
	return nil
}

func collectPackerContainerBuildLogs(ctx context.Context, runtime packerContainerRuntime,
	container string, target *packerDiagnosticTarget,
) (returnErr error) {
	if ctx == nil {
		return e.New(e.CodeUsage, "collecting Packer build logs requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "collecting Packer build logs")
	}
	if runtime == nil || container == "" || target == nil {
		return e.New(e.CodeEngineCapability,
			"collecting Packer build logs requires held runtime, container, workspace and diagnostic capabilities")
	}
	archive, err := heldfile.NewOutput("packer-build-log-transport", maxPackerDiagnosticArchiveBytes)
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err,
			"creating private Packer build-log transport")
	}
	defer func() {
		if err := archive.Close(); err != nil {
			returnErr = errors.Join(returnErr, e.Wrap(e.CodeInternal, err, "closing private Packer build-log transport"))
		}
	}()
	limited := &packerContainerLimitWriter{writer: archive.OSFile(), remaining: maxPackerDiagnosticArchiveBytes}
	var stderr bytes.Buffer
	runErr := runtime.Run(ctx, nil, limited, &stderr, "cp",
		container+":"+packerContainerBuildLogPath+"/.", "-")
	if runErr != nil {
		return runtimeCommandError(ctx, errors.Join(runErr, errPackerDiagnosticsIncomplete), stderr.String(), e.CodeVerifyFailed,
			"collecting bounded Packer build logs")
	}
	held, err := archive.SealContext(ctx)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "holding Packer build-log transport")
	}
	defer func() {
		if err := held.Close(); err != nil {
			returnErr = errors.Join(returnErr,
				e.Wrap(e.CodeInternal, err, "closing held Packer build-log transport"))
		}
	}()
	return target.retainBuildLogs(ctx, held)
}

func (target *packerDiagnosticTarget) retainBuildLogs(ctx context.Context, archive *heldfile.File) error {
	return target.retainBuildLogsWithLimits(ctx, archive, defaultPackerDiagnosticLimits)
}

// recordIncomplete records only a stable code, never subprocess text or secrets.
// Missing/oversized logs do not invalidate a separately verified disk payload.
func (target *packerDiagnosticTarget) recordIncomplete(ctx context.Context, cause error) error {
	target.mu.Lock()
	defer target.mu.Unlock()
	if target.closed || target.root == nil {
		return e.New(e.CodeEngineCapability, "Packer diagnostic target is closed")
	}
	code := e.CodeVerifyFailed
	var coded *e.Error
	if errors.As(cause, &coded) {
		code = coded.Code
	}
	raw, err := json.Marshal(struct {
		Status string `json:"status"`
		Code   e.Code `json:"code"`
	}{Status: "incomplete", Code: code})
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "encoding incomplete diagnostic marker")
	}
	return writePackerDiagnosticFile(ctx, target.root, "logs-incomplete.json", bytes.NewReader(raw), int64(len(raw)))
}

func (target *packerDiagnosticTarget) retainBuildLogsWithLimits(ctx context.Context, archive *heldfile.File,
	limits packerDiagnosticLimits,
) error {
	if target == nil || target.root == nil || archive == nil {
		return e.New(e.CodeEngineCapability,
			"Packer build-log retention requires held diagnostic and archive capabilities")
	}
	if ctx == nil {
		return e.New(e.CodeUsage, "retaining Packer build logs requires a non-nil context")
	}
	if limits.entries <= 0 || limits.files <= 0 || limits.fileBytes <= 0 || limits.payloadBytes <= 0 {
		return e.New(e.CodeInternal, "Packer diagnostic bounds are invalid")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "retaining Packer build logs")
	}

	target.mu.Lock()
	defer target.mu.Unlock()
	if target.closed {
		return e.New(e.CodeEngineCapability, "Packer diagnostic target is closed")
	}
	stageName, err := newPackerDiagnosticStageName()
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "allocating Packer diagnostic staging name")
	}
	if err := unix.Mkdirat(int(target.root.Fd()), stageName, 0o700); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err,
			"creating private Packer diagnostic staging directory")
	}
	stage, _, err := securepath.OpenChild(target.root, stageName, true)
	if err != nil {
		_ = unix.Unlinkat(int(target.root.Fd()), stageName, unix.AT_REMOVEDIR)
		return e.Wrap(e.CodeImmutableViolation, err,
			"holding private Packer diagnostic staging directory")
	}
	published := false
	defer func() {
		if !published {
			_ = stage.Chmod(0o700)
		}
		_ = stage.Close()
		if !published {
			_ = removePackerDiagnosticTree(target.root, stageName)
		}
	}()
	if _, err := archive.OSFile().Seek(0, io.SeekStart); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "rewinding held Packer build-log archive")
	}
	files, err := extractPackerDiagnosticArchive(ctx, tar.NewReader(archive.OSFile()), stage, limits)
	if err != nil {
		return err
	}
	if err := archive.AssertUnchangedContext(ctx); err != nil {
		return e.Wrap(e.CodeUnverifiedRef, err, "rechecking held Packer build-log archive")
	}
	if files == 0 {
		return nil
	}
	if err := sealPackerDiagnosticDirectories(stage); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "protecting retained Packer build logs")
	}
	if err := stage.Sync(); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "persisting retained Packer build logs")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "retaining Packer build logs")
	}
	if err := transportarchive.PublishAtNoReplace(int(target.root.Fd()), stageName,
		int(target.root.Fd()), packerDiagnosticLogsName); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err,
			"publishing no-replace Packer build logs")
	}
	published = true
	if err := target.root.Sync(); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "persisting published Packer build logs")
	}
	return nil
}

// Seal children through their held directory capabilities before publishing the
// root. The extractor has already rejected links, devices and unsafe paths.
func sealPackerDiagnosticDirectories(root *os.File) error {
	names, err := root.Readdirnames(-1)
	if err != nil {
		return err
	}
	slices.Sort(names)
	for _, name := range names {
		entry, err := securepath.InspectChild(root, name, name)
		if err != nil {
			return err
		}
		if entry.Kind == "directory" {
			err = sealPackerDiagnosticDirectories(entry.File)
		} else if entry.Kind != "file" {
			err = errors.New("retained diagnostic entry is not a regular file or directory")
		}
		if entry.File != nil {
			err = errors.Join(err, entry.File.Close())
		}
		if err != nil {
			return err
		}
	}
	return errors.Join(root.Chmod(0o555), root.Sync())
}

func extractPackerDiagnosticArchive(ctx context.Context, reader *tar.Reader, root *os.File,
	limits packerDiagnosticLimits,
) (int, error) {
	seen := make(map[string]struct{})
	entries, files := 0, 0
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return 0, e.Wrap(e.CodeTimeout, err, "retaining Packer build logs")
		}
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, e.Wrap(e.CodeVerifyFailed, err, "reading Packer build-log archive")
		}
		entries++
		if entries > limits.entries {
			return 0, e.Wrap(e.CodeVerifyFailed, errPackerDiagnosticsIncomplete,
				"Packer build-log archive exceeds its %d-entry bound", limits.entries)
		}
		name, rootEntry, ok := cleanPackerDiagnosticArchiveName(header.Name, header.Typeflag == tar.TypeDir)
		if !ok {
			return 0, e.New(e.CodeVerifyFailed,
				"Packer build-log archive contains unsafe path %q", header.Name)
		}
		if rootEntry {
			if header.Typeflag != tar.TypeDir {
				return 0, e.New(e.CodeVerifyFailed,
					"Packer build-log archive has a non-directory root entry")
			}
			continue
		}
		if _, duplicate := seen[name]; duplicate {
			return 0, e.New(e.CodeVerifyFailed,
				"Packer build-log archive repeats path %q", name)
		}
		seen[name] = struct{}{}
		switch header.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg, 0:
		default:
			return 0, e.New(e.CodeVerifyFailed,
				"Packer build-log archive path %q is not a regular file", name)
		}
		files++
		if files > limits.files {
			return 0, e.Wrap(e.CodeVerifyFailed, errPackerDiagnosticsIncomplete,
				"Packer build-log archive exceeds its %d-file bound", limits.files)
		}
		if header.Size < 0 {
			return 0, e.New(e.CodeVerifyFailed, "Packer build-log archive has a negative file size")
		}
		if header.Size > limits.fileBytes || total > limits.payloadBytes-header.Size {
			return 0, e.Wrap(e.CodeVerifyFailed, errPackerDiagnosticsIncomplete,
				"Packer build-log archive path %q exceeds diagnostic byte bounds", name)
		}
		total += header.Size
		if err := writePackerDiagnosticFile(ctx, root, filepath.FromSlash(name), reader, header.Size); err != nil {
			return 0, err
		}
	}
	return files, nil
}

func cleanPackerDiagnosticArchiveName(raw string, directory bool) (name string, root bool, ok bool) {
	if !utf8.ValidString(raw) || strings.ContainsRune(raw, '\x00') || strings.ContainsRune(raw, '\\') {
		return "", false, false
	}
	for strings.HasPrefix(raw, "./") {
		raw = strings.TrimPrefix(raw, "./")
	}
	if directory {
		raw = strings.TrimSuffix(raw, "/")
	}
	if raw == "" || raw == "." {
		return "", true, true
	}
	clean := path.Clean(raw)
	if clean != raw || !fs.ValidPath(clean) || !filepath.IsLocal(filepath.FromSlash(clean)) {
		return "", false, false
	}
	return clean, false, true
}

func writePackerDiagnosticFile(ctx context.Context, root *os.File, relative string, reader io.Reader,
	size int64,
) error {
	parent, name, err := securepath.CreateParent(root, relative)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err,
			"resolving Packer build-log path beneath diagnostic root")
	}
	defer parent.Close()
	fd, err := unix.Openat(int(parent.Fd()), name,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err,
			"creating no-replace Packer build-log file")
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		_ = unix.Unlinkat(int(parent.Fd()), name, 0)
		return e.New(e.CodeInternal, "holding Packer build-log output")
	}
	_, copyErr := transportarchive.CopyExact(ctx, file, reader, size)
	if copyErr == nil {
		copyErr = file.Sync()
	}
	if copyErr == nil {
		copyErr = file.Chmod(0o444)
	}
	if copyErr == nil {
		copyErr = file.Sync()
	}
	closeErr := file.Close()
	if copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		_ = unix.Unlinkat(int(parent.Fd()), name, 0)
		return packerDiagnosticError(ctx, e.CodeVerifyFailed, copyErr,
			"writing Packer build-log file")
	}
	if err := parent.Sync(); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err,
			"persisting Packer build-log file")
	}
	return nil
}

func newPackerDiagnosticStageName() (string, error) {
	var token [12]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return ".logs-" + hex.EncodeToString(token[:]), nil
}

func removePackerDiagnosticTree(parent *os.File, name string) error {
	child, _, err := securepath.OpenChild(parent, name, true)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	// Failed publication may leave fully sealed descendants to roll back.
	if err := child.Chmod(0o700); err != nil {
		_ = child.Close()
		return err
	}
	names, readErr := child.Readdirnames(-1)
	var cleanupErr error
	if readErr != nil {
		cleanupErr = readErr
	}
	for _, childName := range names {
		var stat unix.Stat_t
		if err := unix.Fstatat(int(child.Fd()), childName, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
			continue
		}
		if stat.Mode&unix.S_IFMT == unix.S_IFDIR {
			cleanupErr = errors.Join(cleanupErr, removePackerDiagnosticTree(child, childName))
			continue
		}
		cleanupErr = errors.Join(cleanupErr, unix.Unlinkat(int(child.Fd()), childName, 0))
	}
	cleanupErr = errors.Join(cleanupErr, child.Close())
	if cleanupErr != nil {
		return cleanupErr
	}
	return unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR)
}

func (target *packerDiagnosticTarget) close() error {
	if target == nil {
		return nil
	}
	target.mu.Lock()
	defer target.mu.Unlock()
	if target.closed {
		return nil
	}
	target.closed = true
	if target.root == nil {
		return nil
	}
	var closeErr error
	if err := target.root.Chmod(0o555); err != nil {
		closeErr = errors.Join(closeErr, err)
	}
	if err := target.root.Sync(); err != nil {
		closeErr = errors.Join(closeErr, err)
	}
	closeErr = errors.Join(closeErr, target.root.Close())
	target.root = nil
	if closeErr != nil {
		return e.Wrap(e.CodeImmutableViolation, closeErr,
			"sealing Packer diagnostic route")
	}
	target.sealed = true
	return nil
}

// Close seals any created diagnostic directory and releases the held attempt
// capability. It is safe when no Packer execution reached manifest retention.
func (sink *PackerDiagnosticSink) Close() error {
	if sink == nil {
		return nil
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.closed {
		return nil
	}
	sink.closed = true
	var closeErr error
	for _, directory := range []*os.File{sink.packer, sink.diagnostics} {
		if directory == nil {
			continue
		}
		if err := directory.Chmod(0o555); err != nil {
			closeErr = errors.Join(closeErr, err)
		}
		if err := directory.Sync(); err != nil {
			closeErr = errors.Join(closeErr, err)
		}
		closeErr = errors.Join(closeErr, directory.Close())
	}
	sink.packer, sink.diagnostics = nil, nil
	if sink.attemptRoot != nil {
		if err := sink.attemptRoot.Sync(); err != nil {
			closeErr = errors.Join(closeErr, err)
		}
		closeErr = errors.Join(closeErr, sink.attemptRoot.Close())
		sink.attemptRoot = nil
	}
	if closeErr != nil {
		return e.Wrap(e.CodeImmutableViolation, closeErr,
			"closing Packer diagnostic retention")
	}
	sink.sealed = true
	return nil
}

func packerDiagnosticError(ctx context.Context, code e.Code, err error, message string) error {
	if ctx != nil && ctx.Err() != nil {
		return e.Wrap(e.CodeTimeout, errors.Join(err, ctx.Err()), "%s", message)
	}
	return e.Wrap(code, err, "%s", message)
}
