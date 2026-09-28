package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/NVIDIA/nke-recipes/internal/securepath"
	"github.com/NVIDIA/nke-recipes/internal/transportarchive"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/safetext"
	"golang.org/x/sys/unix"
)

const packerArtifactCollectionCopies int64 = 3

func preparePackerContainerIdentity(ctx context.Context, root, runtimeName string) (packerContainerIdentity, error) {
	if runtimeName != "docker" {
		return packerContainerIdentity{}, nil
	}
	if err := ctx.Err(); err != nil {
		return packerContainerIdentity{}, e.Wrap(e.CodeTimeout, err, "preparing Docker identity projection")
	}
	identityRoot := filepath.Join(root, "identity")
	if err := os.Mkdir(identityRoot, 0o700); err != nil {
		return packerContainerIdentity{}, e.Wrap(e.CodeEngineCapability, err,
			"creating private Docker identity projection")
	}
	uid, gid := os.Getuid(), os.Getgid()
	identity := packerContainerIdentity{
		passwdPath: filepath.Join(identityRoot, "passwd"),
		groupPath:  filepath.Join(identityRoot, "group"),
	}
	if err := writePackerIdentityFile(ctx, identity.passwdPath,
		fmt.Sprintf("nkd:x:%d:%d:NKD Builder:%s/home:/bin/sh\n", uid, gid, packerContainerWorkRoot)); err != nil {
		return packerContainerIdentity{}, err
	}
	if err := writePackerIdentityFile(ctx, identity.groupPath, fmt.Sprintf("nkd:x:%d:\n", gid)); err != nil {
		return packerContainerIdentity{}, err
	}
	return identity, nil
}

func writePackerIdentityFile(ctx context.Context, name, content string) error {
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "creating Docker identity projection")
	}
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "creating Docker identity projection")
	}
	_, writeErr := io.WriteString(file, content)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "persisting Docker identity projection")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "persisting Docker identity projection")
	}
	return nil
}

func validatePackerContainerRequest(request packerContainerRunRequest, kvmEnabled bool) error {
	if request.workspace == nil {
		return e.New(e.CodeEngineCapability, "containerized Packer requires the admitted workspace capability")
	}
	info, err := request.workspace.Stat()
	if err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "validating admitted Packer workspace")
	}
	if !info.IsDir() {
		return e.New(e.CodeEngineCapability, "admitted Packer workspace is not a directory")
	}
	if !canonical.IsDigest(request.source.InputDigest) {
		return e.New(e.CodeEngineCapability, "authenticated Packer source has no input identity")
	}
	if !validPackerTransport(request.bindings.packerVersion, 64, false) {
		return e.New(e.CodeInvalidCatalog, "containerized Packer requires an exact declared Packer version")
	}
	if err := validatePackerContainerManifest(request.manifest, request.digest); err != nil {
		return err
	}
	accelerator := "tcg"
	if kvmEnabled {
		accelerator = "kvm"
	}
	contract := packerBuildContract{
		projectPath: request.node.Packer.ProjectPath, buildName: request.node.Packer.BuildName,
		architectureVariable: request.node.Packer.ArchitectureVariable,
		architecture:         packerArchitecture(request.node.Platform),
		acceleratorVariable:  request.bindings.acceleratorVariable, accelerator: accelerator,
		materialManifestVariable: request.node.Packer.MaterialManifestVariable,
		materialManifestPath:     packerContainerManifestPath, materialManifestSHA256: request.digest,
		materials: request.materials, outputDirectoryVariable: request.node.Packer.OutputDirectoryVariable,
		buildLogDirectoryVariable: request.bindings.buildLogDirectoryVariable,
		httpDirectoryVariable:     request.bindings.httpDirectoryVariable,
		sshPrivateKeyVariable:     request.bindings.sshPrivateKeyVariable,
		sshPublicKeyVariable:      request.bindings.sshPublicKeyVariable,
		artifactPath:              request.node.ArtifactPath,
	}
	for _, variable := range request.node.Packer.Variables {
		contract.variables = append(contract.variables, PackerVariable{Name: variable.Name, Value: variable.Value})
	}
	return validatePackerBuildContract(contract, true)
}

type packerContainerWorkspace struct {
	parent *os.File
	root   *os.File
	name   string
}

func (workspace *packerContainerWorkspace) path() string {
	return fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), workspace.root.Fd())
}

// dockerBindPath projects the held workspace as an ordinary host pathname.
// Docker's daemon cannot bind a client's /proc/<pid>/fd magic link reliably.
// Keep the descriptor for NKD's own I/O and authenticate the projected path
// against it before handing that path to the daemon.
func (workspace *packerContainerWorkspace) dockerBindPath(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", e.Wrap(e.CodeTimeout, err, "resolving Docker host workspace path")
	}
	if workspace == nil || workspace.root == nil {
		return "", e.New(e.CodeEngineCapability, "Docker identity projection requires a held Packer workspace")
	}
	root, err := os.Readlink(workspace.path())
	if err != nil {
		return "", e.Wrap(e.CodeEngineCapability, err, "resolving held Packer workspace for Docker")
	}
	if !filepath.IsAbs(root) {
		return "", e.New(e.CodeEngineCapability, "held Packer workspace has no absolute Docker host path")
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", e.Wrap(e.CodeEngineCapability, err, "resolving Docker host workspace path")
	}
	if !safetext.Valid(root, 4096, false) || strings.ContainsAny(root, ",\"") {
		return "", e.New(e.CodeEngineCapability, "Docker host workspace path is unsafe for a bind mount")
	}
	heldInfo, err := workspace.root.Stat()
	if err != nil {
		return "", e.Wrap(e.CodeEngineCapability, err, "statting held Packer workspace")
	}
	pathInfo, err := os.Stat(root)
	if err != nil {
		return "", e.Wrap(e.CodeEngineCapability, err, "statting Docker host workspace path")
	}
	if !heldInfo.IsDir() || !os.SameFile(heldInfo, pathInfo) {
		return "", e.New(e.CodeEngineCapability, "Docker host workspace path differs from held Packer workspace")
	}
	if err := ctx.Err(); err != nil {
		return "", e.Wrap(e.CodeTimeout, err, "resolving Docker host workspace path")
	}
	return root, nil
}

// prepareDockerIdentity writes through the held workspace and projects only
// verified host paths to Docker. No path-controlled write follows the host
// path check.
func (workspace *packerContainerWorkspace) prepareDockerIdentity(ctx context.Context) (packerContainerIdentity, error) {
	if _, err := workspace.dockerBindPath(ctx); err != nil {
		return packerContainerIdentity{}, err
	}
	heldIdentity, err := preparePackerContainerIdentity(ctx, workspace.path(), "docker")
	if err != nil {
		return packerContainerIdentity{}, err
	}
	hostRoot, err := workspace.dockerBindPath(ctx)
	if err != nil {
		return packerContainerIdentity{}, err
	}
	hostIdentity := packerContainerIdentity{
		passwdPath: filepath.Join(hostRoot, "identity", "passwd"),
		groupPath:  filepath.Join(hostRoot, "identity", "group"),
	}
	for _, pair := range [][2]string{
		{heldIdentity.passwdPath, hostIdentity.passwdPath},
		{heldIdentity.groupPath, hostIdentity.groupPath},
	} {
		heldInfo, err := os.Stat(pair[0])
		if err != nil {
			return packerContainerIdentity{}, e.Wrap(e.CodeEngineCapability, err, "statting held Docker identity file")
		}
		hostInfo, err := os.Stat(pair[1])
		if err != nil {
			return packerContainerIdentity{}, e.Wrap(e.CodeEngineCapability, err, "statting Docker host identity file")
		}
		if !heldInfo.Mode().IsRegular() || !os.SameFile(heldInfo, hostInfo) {
			return packerContainerIdentity{}, e.New(e.CodeEngineCapability, "Docker host identity file differs from held projection")
		}
	}
	if err := ctx.Err(); err != nil {
		return packerContainerIdentity{}, e.Wrap(e.CodeTimeout, err, "preparing Docker identity projection")
	}
	return hostIdentity, nil
}

func (workspace *packerContainerWorkspace) cleanup() error {
	if workspace == nil {
		return nil
	}
	rootPath := workspace.path()
	names, readErr := workspace.root.Readdirnames(-1)
	var cleanupErrors []error
	if readErr != nil {
		cleanupErrors = append(cleanupErrors, readErr)
	}
	for _, name := range names {
		if err := removePrivateTree(filepath.Join(rootPath, name)); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	if err := workspace.root.Close(); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	}
	if err := unix.Unlinkat(int(workspace.parent.Fd()), workspace.name, unix.AT_REMOVEDIR); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	} else if err := workspace.parent.Sync(); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	}
	if err := workspace.parent.Close(); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	}
	return errors.Join(cleanupErrors...)
}

func createPackerWorkspaceRoot(workspace *os.File, attempt string) (*packerContainerWorkspace, error) {
	parentFD, err := unix.Dup(int(workspace.Fd()))
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "retaining admitted Packer workspace")
	}
	parent := os.NewFile(uintptr(parentFD), "packer-workspace")
	if parent == nil {
		_ = unix.Close(parentFD)
		return nil, e.New(e.CodeEngineCapability, "retaining admitted Packer workspace")
	}
	name := ".nkd-packer-" + attempt
	if err := unix.Mkdirat(int(parent.Fd()), name, 0o700); err != nil {
		_ = parent.Close()
		return nil, e.Wrap(e.CodeBuildFailed, err, "creating private Packer workspace")
	}
	if err := parent.Sync(); err != nil {
		_ = unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR)
		_ = parent.Close()
		return nil, e.Wrap(e.CodeBuildFailed, err, "persisting private Packer workspace")
	}
	directory, _, err := securepath.OpenChild(parent, name, true)
	if err != nil {
		_ = unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR)
		_ = parent.Close()
		return nil, e.Wrap(e.CodeBuildFailed, err, "holding private Packer workspace")
	}
	return &packerContainerWorkspace{parent: parent, root: directory, name: name}, nil
}

func preparePackerContainerTransport(ctx context.Context, root string, request packerContainerRunRequest) (*heldfile.File, *heldfile.File, *stagedPackerInputs, error) {
	inputRoot := filepath.Join(root, "input")
	workRoot := filepath.Join(root, "work")
	for _, directory := range []string{
		filepath.Join(inputRoot, "source"), filepath.Join(inputRoot, "inputs"), filepath.Join(inputRoot, "config"),
		filepath.Join(workRoot, "output"), filepath.Join(workRoot, "cache"), filepath.Join(workRoot, "tmp"),
		filepath.Join(workRoot, "home"), filepath.Join(workRoot, "build-logs"), filepath.Join(workRoot, "http-data"),
		filepath.Join(workRoot, "ssh"), filepath.Join(workRoot, "stage"),
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, nil, nil, e.Wrap(e.CodeBuildFailed, err, "creating private Packer transport state")
		}
	}
	before, err := sourceTreeDigestContext(ctx, request.source.Dir)
	if err != nil {
		return nil, nil, nil, e.Wrap(e.CodeUnverifiedRef, err, "rechecking authenticated Packer source")
	}
	if err := copySourceTreeContext(ctx, request.source.Dir, filepath.Join(inputRoot, "source"), 0); err != nil {
		return nil, nil, nil, e.Wrap(e.CodeBuildFailed, err, "copying authenticated Packer source into transport")
	}
	copied, err := sourceTreeDigestContext(ctx, filepath.Join(inputRoot, "source"))
	if err != nil {
		return nil, nil, nil, e.Wrap(e.CodeUnverifiedRef, err, "checking transported Packer source")
	}
	after, err := sourceTreeDigestContext(ctx, request.source.Dir)
	if err != nil {
		return nil, nil, nil, e.Wrap(e.CodeUnverifiedRef, err, "rechecking authenticated Packer source")
	}
	if before != after || copied != before {
		return nil, nil, nil, e.New(e.CodeUnverifiedRef, "authenticated Packer source changed during transport")
	}
	manifestPath := filepath.Join(inputRoot, "inputs", "material-manifest.json")
	manifestHeld, err := heldfile.FromBytesContext(ctx, request.manifest, heldfile.Options{
		Label: "packer-material-manifest", MaxBytes: maxBuildMaterialManifestBytes, ExpectedDigest: request.digest,
	})
	if err != nil {
		return nil, nil, nil, e.Wrap(e.CodeBuildFailed, err, "staging Packer material manifest")
	}
	if err := materializeHeldInput(ctx, filepath.Dir(manifestPath), filepath.Base(manifestPath), manifestHeld); err != nil {
		_ = manifestHeld.Close()
		return nil, nil, nil, e.Wrap(e.CodeBuildFailed, err, "staging Packer material manifest")
	}
	_ = manifestHeld.Close()
	buildInputs, err := stageBuildMaterialInputs(ctx, filepath.Join(inputRoot, "inputs"), request.digest, request.materials)
	if err != nil {
		return nil, nil, nil, err
	}
	staged := &stagedPackerInputs{stagedBuildMaterialInputs: buildInputs}
	if err := materializePackerContainerBytes(ctx, filepath.Join(inputRoot, "config"), "packer.json", []byte("{}\n")); err != nil {
		return nil, nil, nil, e.Wrap(e.CodeBuildFailed, err, "staging private Packer configuration")
	}
	if err := sealPrivateInputDirectories(inputRoot); err != nil {
		return nil, nil, nil, e.Wrap(e.CodeInternal, err, "sealing private Packer consumer-input closure")
	}
	// The transport archive is created beside the selected config/inputs/source
	// roots. Reopen only their unarchived parent for that private publication;
	// the selected directory modes remain sealed and are what Pack records.
	if err := os.Chmod(inputRoot, 0o700); err != nil {
		return nil, nil, nil, e.Wrap(e.CodeInternal, err, "opening private Packer transport parent")
	}
	staged.root = filepath.Join(inputRoot, "inputs")
	if err := staged.verify(ctx); err != nil {
		return nil, nil, nil, err
	}

	// The deterministic inner tar is the complete consumer-input authority. It
	// preserves the already-validated source tree, directory membership, modes,
	// symlinks, manifest and material bytes. Docker transports only a one-file
	// wrapper; the pinned toolchain authenticates this inner blob before it
	// extracts anything into the input volume.
	inputArchive := filepath.Join(inputRoot, "input.tar")
	if err := transportarchive.Pack(ctx, inputArchive, inputRoot, []string{"config", "inputs", "source"}); err != nil {
		return nil, nil, nil, e.Wrap(e.CodeBuildFailed, err,
			"packing bounded Packer input closure")
	}
	innerHeld, err := heldfile.OpenContext(ctx, inputArchive, heldfile.Options{
		Label: "packer-input-closure", MaxBytes: maxPackerInputClosureBytes,
	})
	if err != nil {
		return nil, nil, nil, e.Wrap(e.CodeUnverifiedRef, err, "holding Packer input transport")
	}
	if err := os.Remove(inputArchive); err != nil {
		_ = innerHeld.Close()
		return nil, nil, nil, e.Wrap(e.CodeInternal, err, "unlinking Packer input transport path")
	}
	staged.inputArchiveDigest = innerHeld.Digest()

	carrierRoot := filepath.Join(root, "carrier")
	if err := os.MkdirAll(carrierRoot, 0o700); err != nil {
		_ = innerHeld.Close()
		return nil, nil, nil, e.Wrap(e.CodeBuildFailed, err, "creating private Packer carrier state")
	}
	if err := materializeHeldInput(ctx, carrierRoot, "input.tar", innerHeld); err != nil {
		_ = innerHeld.Close()
		return nil, nil, nil, e.Wrap(e.CodeBuildFailed, err, "materializing held Packer input closure")
	}
	carrierArchive := filepath.Join(carrierRoot, "carrier.tar")
	if err := transportarchive.Pack(ctx, carrierArchive, carrierRoot, []string{"input.tar"}); err != nil {
		_ = innerHeld.Close()
		return nil, nil, nil, e.Wrap(e.CodeBuildFailed, err, "packing Packer input carrier")
	}
	inputHeld, err := heldfile.OpenContext(ctx, carrierArchive, heldfile.Options{
		Label: "packer-input-carrier", MaxBytes: transportarchive.MaxArchiveBytes,
	})
	if err != nil {
		_ = innerHeld.Close()
		return nil, nil, nil, e.Wrap(e.CodeUnverifiedRef, err, "holding Packer input carrier")
	}
	if err := os.Remove(carrierArchive); err != nil {
		_ = innerHeld.Close()
		_ = inputHeld.Close()
		return nil, nil, nil, e.Wrap(e.CodeInternal, err, "unlinking Packer input carrier path")
	}
	if err := validatePackerInputCarrierBounds(innerHeld.Size(), inputHeld.Size()); err != nil {
		_ = innerHeld.Close()
		_ = inputHeld.Close()
		return nil, nil, nil, err
	}
	if err := innerHeld.Close(); err != nil {
		_ = inputHeld.Close()
		return nil, nil, nil, e.Wrap(e.CodeInternal, err, "closing held Packer input closure")
	}
	workArchive := filepath.Join(workRoot, "work.tar")
	if err := transportarchive.Pack(ctx, workArchive, workRoot,
		[]string{"build-logs", "cache", "home", "http-data", "output", "ssh", "stage", "tmp"}); err != nil {
		_ = inputHeld.Close()
		return nil, nil, nil, e.Wrap(e.CodeBuildFailed, err,
			"packing bounded Packer work closure")
	}
	workHeld, err := heldfile.OpenContext(ctx, workArchive, heldfile.Options{
		Label: "packer-work-transport", MaxBytes: transportarchive.MaxArchiveBytes,
	})
	if err != nil {
		_ = inputHeld.Close()
		return nil, nil, nil, e.Wrap(e.CodeUnverifiedRef, err, "holding Packer work transport")
	}
	if err := os.Remove(workArchive); err != nil {
		_ = inputHeld.Close()
		_ = workHeld.Close()
		return nil, nil, nil, e.Wrap(e.CodeInternal, err, "unlinking Packer work transport path")
	}
	return inputHeld, workHeld, staged, nil
}

func validatePackerInputCarrierBounds(innerBytes, carrierBytes int64) error {
	if innerBytes <= 0 || innerBytes > maxPackerInputClosureBytes {
		return e.New(e.CodeBuildFailed,
			"authenticated Packer input closure exceeds its %d-byte bound", maxPackerInputClosureBytes)
	}
	if carrierBytes <= innerBytes {
		return e.New(e.CodeBuildFailed,
			"authenticated Packer input carrier does not wrap its %d-byte inner closure", innerBytes)
	}
	if carrierBytes > MaxVMImagePackerInputBytes {
		return e.New(e.CodeBuildFailed,
			"authenticated Packer input carrier exceeds its %d-byte aggregate bound", MaxVMImagePackerInputBytes)
	}
	return nil
}

func materializePackerContainerBytes(ctx context.Context, root, name string, value []byte) error {
	held, err := heldfile.FromBytesContext(ctx, value, heldfile.Options{
		Label: name, MaxBytes: maxBuildMaterialManifestBytes,
	})
	if err != nil {
		return err
	}
	materializeErr := materializeHeldInput(ctx, root, name, held)
	return errors.Join(materializeErr, held.Close())
}

func copyArchiveToContainer(ctx context.Context, runtime packerContainerRuntime, archive *heldfile.File, container, destination string) error {
	reader, writer := io.Pipe()
	copyDone := make(chan error, 1)
	go func() {
		_, err := archive.WriteToContext(ctx, writer)
		_ = writer.CloseWithError(err)
		copyDone <- err
	}()
	var stderr bytes.Buffer
	runErr := runtime.Run(ctx, reader, io.Discard, &stderr, "cp", "-", container+":"+destination)
	_ = reader.Close()
	copyErr := <-copyDone
	if err := errors.Join(runErr, copyErr); err != nil {
		return runtimeCommandError(ctx, err, stderr.String(), e.CodeBuildFailed,
			"transporting authenticated Packer inputs")
	}
	if err := archive.AssertUnchangedContext(ctx); err != nil {
		return e.Wrap(e.CodeUnverifiedRef, err, "rechecking held Packer transport")
	}
	return nil
}

func collectPackerContainerArtifact(ctx context.Context, runtime packerContainerRuntime, container, artifactPath, root string, storageMiB uint64) (artifact *PackerArtifact, returnErr error) {
	archiveLimit := packerArtifactArchiveLimit(storageMiB)
	if archiveLimit <= 0 {
		return nil, e.New(e.CodeEngineCapability,
			"Packer artifact collection requires positive admitted workspace capacity")
	}
	archivePath := filepath.Join(root, "artifact.tar")
	archive, err := os.OpenFile(archivePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "creating private QCOW2 transport")
	}
	limited := &packerContainerLimitWriter{writer: archive, remaining: archiveLimit}
	var stderr bytes.Buffer
	runErr := runtime.Run(ctx, nil, limited, &stderr, "cp", container+":"+artifactPath, "-")
	syncErr := archive.Sync()
	closeErr := archive.Close()
	if err := errors.Join(runErr, syncErr, closeErr); err != nil {
		return nil, runtimeCommandError(ctx, err, stderr.String(), e.CodeVerifyFailed,
			"collecting declared Packer artifact")
	}
	heldArchive, err := heldfile.OpenContext(ctx, archivePath, heldfile.Options{
		Label: "packer-artifact-transport", MaxBytes: archiveLimit,
	})
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "holding Packer artifact transport")
	}
	defer func() {
		closeErr := heldArchive.Close()
		if closeErr == nil {
			return
		}
		if artifact != nil {
			closeErr = errors.Join(closeErr, artifact.Close())
			artifact = nil
		}
		code := e.CodeInternal
		var coded *e.Error
		if errors.As(returnErr, &coded) {
			code = coded.Code
		}
		returnErr = e.Wrap(code, errors.Join(returnErr, closeErr),
			"closing held Packer artifact transport")
	}()
	if err := os.Remove(archivePath); err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "unlinking Packer artifact transport path")
	}
	extractRoot := filepath.Join(root, "artifact")
	expected := path.Base(artifactPath)
	if err := transportarchive.UnpackOpen(ctx, heldArchive.OSFile(), extractRoot, []string{expected}); err != nil {
		return nil, err
	}
	if err := heldArchive.AssertUnchangedContext(ctx); err != nil {
		return nil, e.Wrap(e.CodeUnverifiedRef, err, "rechecking Packer artifact transport")
	}
	artifactFile, before, err := securepath.OpenRegular(filepath.Join(extractRoot, expected))
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "holding collected Packer artifact")
	}
	cleanup := func() { _ = artifactFile.Close() }
	if before.Size() <= 4 || before.Size() > archiveLimit {
		cleanup()
		return nil, e.New(e.CodeVerifyFailed, "declared Packer artifact has invalid size %d", before.Size())
	}
	magic := make([]byte, 4)
	if _, err := io.ReadFull(artifactFile, magic); err != nil || !bytes.Equal(magic, []byte{'Q', 'F', 'I', 0xfb}) {
		cleanup()
		return nil, e.New(e.CodeVerifyFailed, "declared Packer artifact is not a QCOW2 image")
	}
	if _, err := artifactFile.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, e.Wrap(e.CodeVerifyFailed, err, "rewinding collected QCOW2")
	}
	digest, size, _, err := hashOpenRegularContext(ctx, artifactFile, before)
	if err != nil {
		cleanup()
		return nil, e.Wrap(e.CodeVerifyFailed, err, "hashing collected QCOW2")
	}
	if _, err := artifactFile.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, e.Wrap(e.CodeVerifyFailed, err, "rewinding collected QCOW2")
	}
	if err := os.Remove(filepath.Join(extractRoot, expected)); err != nil {
		cleanup()
		return nil, e.Wrap(e.CodeInternal, err, "unlinking private QCOW2 collection path")
	}
	return &PackerArtifact{file: artifactFile, digest: digest, size: size}, nil
}

// packerArtifactArchiveLimit reserves capacity for the held Docker tar, its
// immutable snapshot, and the extracted QCOW2, which coexist during unpacking.
// The authored ephemeral-storage request must also cover every other source,
// build, package, and verification temporary that can coexist in the workspace.
func packerArtifactArchiveLimit(storageMiB uint64) int64 {
	if storageMiB == 0 {
		return 0
	}
	if storageMiB > uint64(math.MaxInt64>>20) {
		return transportarchive.MaxArchiveBytes
	}
	limit := (int64(storageMiB) << 20) / packerArtifactCollectionCopies
	if limit > transportarchive.MaxArchiveBytes {
		return transportarchive.MaxArchiveBytes
	}
	return limit
}

type packerContainerLimitWriter struct {
	writer    io.Writer
	remaining int64
}

func (writer *packerContainerLimitWriter) Write(value []byte) (int, error) {
	if int64(len(value)) > writer.remaining {
		return 0, e.New(e.CodeVerifyFailed, "Packer artifact transport exceeds its byte bound")
	}
	n, err := writer.writer.Write(value)
	writer.remaining -= int64(n)
	return n, err
}
