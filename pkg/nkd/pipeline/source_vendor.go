package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/securepath"
	"github.com/NVIDIA/nke-recipes/internal/transportarchive"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// sourceVendor is private, call-owned runtime admission, not build authority.
// Its runtime environment is independent of the Git credential environment.
type sourceVendor struct {
	held     *heldRuntimeExecutable
	runtime  packerContainerRuntime
	image    string
	platform string
	network  string
	command  []string
}

func admitSourceVendor(ctx context.Context, node ir.Node, runtimeName string) (_ *sourceVendor, returnErr error) {
	if err := requireRunContext(ctx, "source vendor admission"); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "source vendor admission canceled")
	}
	if runtime.GOOS != "linux" {
		return nil, e.New(e.CodeEngineCapability, "held source vendoring requires Linux")
	}
	if _, ok := ctx.Deadline(); !ok {
		return nil, e.New(e.CodeEngineCapability, "held source vendoring requires a bounded fetch deadline")
	}
	if node.Network != ir.NetworkDefault && node.Network != ir.NetworkNone {
		return nil, e.New(e.CodeEngineCapability, "held source vendoring requires the declared fetch network")
	}
	path, err := resolveExecutionRuntime(runtimeName)
	if err != nil {
		return nil, err
	}
	if name := filepath.Base(path); name != "docker" && name != "podman" {
		return nil, e.New(e.CodeEngineCapability, "held source vendoring requires the selected Docker or Podman runtime")
	}
	held, err := snapshotRuntimeExecutableContext(ctx, path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, held.close())
		}
	}()
	held.identity, err = observeHeldRuntimeIdentity(ctx, held)
	if err != nil {
		return nil, err
	}
	platform := "linux/" + runtime.GOARCH
	if _, err := observeHeldContainerImage(ctx, held, ir.Node{ID: node.ID, Toolchain: node.VendorImage, Platform: platform}); err != nil {
		return nil, err
	}
	adapter := heldPackerContainerRuntime{runtime: held}
	image, err := sourceVendorImage(ctx, adapter, node.VendorImage, platform)
	if err != nil {
		return nil, err
	}
	return &sourceVendor{held: held, runtime: adapter, image: image, platform: platform,
		network: node.Network, command: append([]string(nil), node.VendorCmd...)}, nil
}

// Resolve the authenticated registry subject to its local config identity and
// reject implicit image volumes. The pinned image owns its entrypoint/environment;
// no caller or ambient environment is forwarded into the container.
func sourceVendorImage(ctx context.Context, runtime packerContainerRuntime, reference, platform string) (string, error) {
	var stdout, stderr bytes.Buffer
	if err := runtime.Run(ctx, nil, &stdout, &stderr, "image", "inspect", "--format", "{{json .}}", reference); err != nil {
		return "", runtimeCommandError(ctx, err, stderr.String(), e.CodeEngineCapability, "inspecting pinned source vendor image")
	}
	if err := canonical.ValidateJSONDocument(stdout.Bytes()); err != nil {
		return "", e.Wrap(e.CodeEngineCapability, err, "decoding source vendor image")
	}
	var image struct {
		ID           string `json:"Id"`
		OS           string `json:"Os"`
		Architecture string `json:"Architecture"`
		Config       struct {
			Volumes map[string]json.RawMessage `json:"Volumes"`
		} `json:"Config"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &image); err != nil {
		return "", e.Wrap(e.CodeEngineCapability, err, "decoding source vendor image")
	}
	if validateSHA256(image.ID) != nil || image.OS+"/"+image.Architecture != platform || len(image.Config.Volumes) != 0 {
		return "", e.New(e.CodeEngineCapability, "source vendor image has a mismatched native platform, invalid identity or implicit volumes")
	}
	return image.ID, nil
}

func (vendor *sourceVendor) createArgs(name, volume string) []string {
	args := []string{"create", "--name", name, "--pull=never", "--platform", vendor.platform,
		"--label", "nkd.dev/role=source-vendor",
		"--network=" + vendor.network, "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges",
		"--no-healthcheck", "--mount", "type=volume,source=" + volume + ",destination=/src,volume-nocopy",
		"--tmpfs", "/tmp:rw,nosuid,nodev,mode=1777", "--workdir", "/src/source",
		"--env", "HOME=/tmp/home", "--env", "TMPDIR=/tmp", "--env", "GOCACHE=/tmp/go-build",
		"--env", "GOPATH=/tmp/go", "--env", "GOMODCACHE=/tmp/go/pkg/mod", vendor.image}
	return append(args, vendor.command...)
}

func (vendor *sourceVendor) run(ctx context.Context, workspace, checkout *os.File, stdout, stderr io.Writer) (returnErr error) {
	if err := requireRunContext(ctx, "source vendoring"); err != nil {
		return err
	}
	defer func() { returnErr = mapCancellation(ctx, returnErr, "source vendoring") }()
	if vendor == nil || vendor.runtime == nil || workspace == nil || checkout == nil {
		return e.New(e.CodeEngineCapability, "source vendoring requires its admitted runtime and held checkout")
	}
	if err := observePackerRuntimeFreshness(ctx, vendor.runtime); err != nil {
		return err
	}
	image, err := sourceVendorImage(ctx, vendor.runtime, vendor.image, vendor.platform)
	if err != nil {
		return err
	}
	if image != vendor.image {
		return e.New(e.CodeEngineCapability, "source vendor image identity changed after admission")
	}
	attempt, err := packerAttemptID()
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "allocating source vendor attempt")
	}
	resources := &packerContainerResources{runtime: vendor.runtime}
	defer func() { returnErr = errors.Join(returnErr, resources.cleanup()) }()
	resources.workspace, err = createPackerWorkspaceRoot(workspace, attempt)
	if err != nil {
		return err
	}
	// Children of the held root have ordinary final components; the archive
	// owner's path normalization cannot turn the final component into a magic link.
	root := resources.workspace.path()
	input, output := filepath.Join(root, "input"), filepath.Join(root, "output")
	for _, directory := range []string{input, output, filepath.Join(input, "source")} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			return e.Wrap(e.CodeFetchFailed, err, "creating private source vendor transport")
		}
	}
	checkoutPath, err := sourceFetchDescriptorPath(checkout.Fd())
	if err != nil {
		return err
	}
	before, err := sourceTreeDigestContext(ctx, checkoutPath)
	if err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "authenticating source before vendoring")
	}
	if err := copySourceTreeContext(ctx, checkoutPath, filepath.Join(input, "source"), 0); err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "copying held vendor source")
	}
	copied, err := sourceTreeDigestContext(ctx, filepath.Join(input, "source"))
	if err != nil || copied != before {
		return e.Wrap(e.CodeFetchFailed, errors.Join(err, errors.New("source snapshot differs from held checkout")), "authenticating vendor input")
	}
	archivePath := filepath.Join(input, "source.tar")
	if err := transportarchive.Pack(ctx, archivePath, input, []string{"source"}); err != nil {
		return err
	}
	archive, _, err := securepath.OpenRegular(archivePath)
	if err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "holding vendor source carrier")
	}
	defer func() { returnErr = errors.Join(returnErr, archive.Close()) }()
	carrierDigest, err := sourceVendorCarrierDigest(ctx, archive)
	if err != nil {
		return err
	}
	available, err := securepath.AvailableDiskBackedBytes(ctx, resources.workspace.root)
	if err != nil || available/4 == 0 {
		return e.Wrap(e.CodeEngineCapability, err, "source vendor return transport requires observed disk-backed scratch headroom")
	}
	// Returned carrier, decoder snapshot, decoded closure and checkout copy can
	// coexist. This is bounded point-in-time headroom, not a storage reservation.
	returnLimit := int64(min(available/4, uint64(transportarchive.MaxArchiveBytes)))
	volume, container := "nkd-source-"+attempt, "nkd-source-"+attempt+"-vendor"
	resources.volumes = append(resources.volumes, volume)
	if err := createPackerVolume(ctx, vendor.runtime, volume, []string{"nkd.dev/role=source-vendor", "nkd.dev/attempt=" + attempt}); err != nil {
		return err
	}
	resources.containers = append(resources.containers, container)
	var created bytes.Buffer
	if err := vendor.runtime.Run(ctx, nil, &created, stderr, vendor.createArgs(container, volume)...); err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "creating private source vendor container")
	}
	if strings.TrimSpace(created.String()) == "" {
		return e.New(e.CodeFetchFailed, "runtime did not confirm source vendor container creation")
	}
	if err := vendor.runtime.Run(ctx, archive, io.Discard, stderr, "cp", "-", container+":/src"); err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "transporting source to private vendor volume")
	}
	if after, err := sourceVendorCarrierDigest(ctx, archive); err != nil || after != carrierDigest {
		return e.Wrap(e.CodeFetchFailed, errors.Join(err, errors.New("source vendor carrier changed during transfer")), "rechecking vendor input carrier")
	}
	if err := vendor.runtime.Run(ctx, nil, stdout, stderr, "start", "--attach", container); err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "running source vendor command")
	}
	returned, err := os.OpenFile(filepath.Join(output, "result.tar"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "creating held source vendor result carrier")
	}
	defer func() { returnErr = errors.Join(returnErr, returned.Close()) }()
	limit := &packerContainerLimitWriter{writer: returned, remaining: returnLimit}
	if err := vendor.runtime.Run(ctx, nil, limit, stderr, "cp", container+":/src/source", "-"); err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "collecting bounded source vendor closure")
	}
	if err := transportarchive.UnpackOpen(ctx, returned, output, []string{"source"}); err != nil {
		return err
	}
	if err := observePackerRuntimeFreshness(ctx, vendor.runtime); err != nil {
		return err
	}
	return replaceVendoredSource(ctx, checkout, filepath.Join(output, "source"), before)
}

func sourceVendorCarrierDigest(ctx context.Context, file *os.File) (string, error) {
	info, err := file.Stat()
	if err != nil {
		return "", e.Wrap(e.CodeFetchFailed, err, "stating source vendor carrier")
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > transportarchive.MaxArchiveBytes {
		return "", e.New(e.CodeFetchFailed, "source vendor carrier exceeds its regular-file byte bound")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", e.Wrap(e.CodeFetchFailed, err, "rewinding source vendor carrier")
	}
	digest, _, _, err := hashOpenRegularContext(ctx, file, info)
	if err != nil {
		return "", e.Wrap(e.CodeFetchFailed, err, "authenticating source vendor carrier")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", e.Wrap(e.CodeFetchFailed, err, "rewinding authenticated source vendor carrier")
	}
	return digest, nil
}

// A failed copy-back cannot issue a receipt: ensureSourceClosure commits only
// after this succeeds. The original Git directory is never copied or removed.
func replaceVendoredSource(ctx context.Context, checkout *os.File, source, before string) (returnErr error) {
	returned, _, err := securepath.OpenDirectory(source)
	if err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "opening validated vendor closure")
	}
	defer returned.Close()
	git, _, err := securepath.OpenChild(returned, ".git", true)
	if err == nil {
		_ = git.Close()
		return e.New(e.CodeFetchFailed, "vendor result contains a forbidden Git directory")
	}
	if !os.IsNotExist(err) {
		return e.Wrap(e.CodeFetchFailed, err, "vendor result contains an unsafe Git member")
	}
	want, err := sourceTreeDigestContext(ctx, source)
	if err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "validating complete vendor source before copy-back")
	}
	path, err := sourceFetchDescriptorPath(checkout.Fd())
	if err != nil {
		return err
	}
	after, err := sourceTreeDigestContext(ctx, path)
	if err != nil || after != before {
		return e.Wrap(e.CodeFetchFailed, errors.Join(err, errors.New("held source changed during vendoring")), "revalidating original vendor source")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "opening confined vendor copy-back root")
	}
	defer func() {
		if err := root.Close(); err != nil {
			returnErr = errors.Join(returnErr, e.Wrap(e.CodeFetchFailed, err, "closing confined vendor copy-back root"))
		}
	}()
	names, err := sourceChildNames(checkout)
	if err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "enumerating original vendor source")
	}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return e.Wrap(e.CodeTimeout, err, "vendor copy-back canceled")
		}
		if name != ".git" {
			if err := root.RemoveAll(name); err != nil {
				return e.Wrap(e.CodeFetchFailed, err, "replacing vendor source member %s", strconv.Quote(name))
			}
		}
	}
	entries := 0
	if err := copySourceDirectoryContext(ctx, returned, checkout, "", time.Unix(0, 0).UTC(), &entries); err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "copying validated vendor closure to held checkout")
	}
	got, err := sourceTreeDigestContext(ctx, path)
	if err != nil || got != want {
		return e.Wrap(e.CodeFetchFailed, errors.Join(err, errors.New("vendor copy-back differs from validated closure")), "authenticating vendor copy-back")
	}
	return nil
}
