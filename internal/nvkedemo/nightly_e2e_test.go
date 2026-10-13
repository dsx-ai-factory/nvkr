// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/sourcegit"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/releaseview"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"go.yaml.in/yaml/v3"
	"golang.org/x/sys/unix"
)

// This is the scheduled workflow's integration test, not a product command.
// It consumes a completed, authenticated publication and executes NVKE's own
// suite. Ordinary repository tests allocate no runtime resources.
func TestNightlyPublishedCandidate(t *testing.T) {
	if os.Getenv("NVKR_NIGHTLY_ENABLE") != "1" {
		t.Skip("nightly integration is explicitly enabled by its protected GitHub job")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 305*time.Minute)
	defer cancel()
	if err := runNightlyPublishedCandidate(ctx); err != nil {
		t.Fatal(err)
	}
}

type nightlyImageInput struct {
	Name           string `json:"name"`
	Reference      string `json:"reference"`
	ManifestDigest string `json:"manifestDigest"`
	Archive        string `json:"archive"`
	ArchiveDigest  string `json:"archiveDigest"`
}

func runNightlyPublishedCandidate(ctx context.Context) (resultErr error) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" || os.Getenv("GITHUB_REF") != "refs/heads/main" || os.Getenv("GITHUB_SERVER_URL") != "https://github.com" {
		return e.New(e.CodeEngineCapability, "nightly runtime requires the protected main GitHub workflow on native Linux ARM64")
	}
	profile, err := loadNightlyProfile(ctx, os.Getenv("NVKR_NIGHTLY_PROFILES"), os.Getenv("NVKR_NIGHTLY_PROFILE"))
	if err != nil {
		return err
	}
	build, err := loadNightlyBuild(ctx, os.Getenv("NVKR_NIGHTLY_BUILD"))
	if err != nil {
		return err
	}
	if err = validateNightlyCycle(profile, build, os.Getenv("GITHUB_REPOSITORY"), os.Getenv("GITHUB_REPOSITORY_ID"), os.Getenv("GITHUB_SHA"), os.Getenv("GITHUB_RUN_ID"), os.Getenv("GITHUB_RUN_ATTEMPT")); err != nil {
		return err
	}
	if err = nightlySecurityAdmission(profile); err != nil {
		return err
	}
	public, err := freshDestination(os.Getenv("NVKR_NIGHTLY_REPORTS"))
	if err != nil || !filepath.IsAbs(os.Getenv("NVKR_NIGHTLY_REPORTS")) {
		return e.New(e.CodeUsage, "nightly requires a fresh absolute public report directory")
	}
	if err = os.Mkdir(public, 0o700); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "creating public nightly reports")
	}
	phase, subject, suite := "preparation", "", ""
	defer func() {
		// Registered before publication/network/process owners. Their deferred
		// cleanup errors reach resultErr before this final verdict is written.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		if suite != "" {
			resultErr = errors.Join(resultErr, nightlyPublicDiagnostics(cleanup, suite, public))
		}
		resultErr = errors.Join(resultErr, privateJSON(filepath.Join(public, "cycle.json"), map[string]any{
			"profile": profile.Name, "releaseSubject": subject, "phase": phase,
			"runtimePassed": phase == "runtime" && resultErr == nil, "qualificationAuthority": "none",
		}))
	}()
	root := os.Getenv("GITHUB_WORKSPACE")
	if !filepath.IsAbs(root) {
		return e.New(e.CodeUsage, "require an absolute trusted repository workspace")
	}
	for _, path := range []string{profile.Definition, profile.Environment, profile.Plan} {
		if !securepath.WorkspaceRelative(path) {
			return e.New(e.CodeUsage, "nightly input must remain within the trusted repository")
		}
	}
	definition, err := lifecycle.LoadDefinition(ctx, filepath.Join(root, profile.Definition))
	if err != nil {
		return err
	}
	environment, err := lifecycle.LoadEnvironment(ctx, filepath.Join(root, profile.Environment))
	if err != nil {
		return err
	}
	plan, err := factory.LoadPlanContext(ctx, filepath.Join(root, profile.Plan))
	if err != nil {
		return err
	}
	expectation, err := lifecycle.PrepareExactInputs(definition, environment, plan)
	if err != nil {
		return err
	}
	workspace, err := freshDestination(os.Getenv("NVKR_NIGHTLY_WORKSPACE"))
	if err != nil {
		return e.Wrap(e.CodeUsage, err, "nightly requires a fresh private workspace")
	}
	if err = os.Mkdir(workspace, 0o700); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "creating nightly workspace")
	}
	runner := Runner{Work: workspace, Env: NativeEnvironment(), Progress: os.Stdout}
	binding, err := admitLibvirtEnvironment(ctx, runner)
	if err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "admitting native account/runtime environment")
	}
	runner.Env, err = nightlyPreparationEnvironment(runner.Env, binding, workspace)
	if err != nil {
		return err
	}
	source := os.Getenv("NVKR_NIGHTLY_SOURCE")
	if err = nightlySource(ctx, runner, source, profile.HarnessCommit); err != nil {
		return err
	}
	// Refuse missing runtime capabilities before fetching large publication data.
	for _, tool := range []string{"bash", "docker", "virsh", "qemu-img", "qemu-system-aarch64", "cloud-localds", "swtpm", "dnsmasq", "ip", "iptables", "sudo", "setcap", "getcap", "jq", "curl", "timeout", "flock", "setsid", "openssl", "go", "ss", "make", "awk", "df", "sort", "seq"} {
		if _, err = commandPath(tool, runner.Env); err != nil {
			return e.Wrap(e.CodeEngineCapability, err, "admitting nightly host")
		}
	}
	device, openErr := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if openErr != nil {
		return e.Wrap(e.CodeEngineCapability, openErr, "native KVM is unavailable")
	}
	_ = device.Close()
	inputs, err := newNightlyInputs(ctx, profile, build, plan, workspace)
	if err != nil {
		return err
	}
	consumer, err := lifecycle.NewConsumer(lifecycle.ConsumerOptions{
		CosignBinary: os.Getenv("NVKR_NIGHTLY_COSIGN"), CosignDigest: os.Getenv("NVKR_NIGHTLY_COSIGN_DIGEST"),
		WorkspaceRoot: filepath.Join(workspace, "verified"),
		Registry:      delivery.RegistryOptions{Username: os.Getenv("GITHUB_ACTOR"), Password: os.Getenv("GH_TOKEN")},
		Credential:    sign.Credential{Registry: "ghcr.io", Username: os.Getenv("GITHUB_ACTOR"), Password: os.Getenv("GH_TOKEN")},
	})
	if err != nil {
		return err
	}
	verifyCtx, verifyCancel := context.WithTimeout(ctx, 45*time.Minute)
	defer verifyCancel()
	policy := lifecycle.VerificationPolicy{Lane: lifecycle.LanePipeline, Scope: lifecycle.ScopeArtifactPublication, IdentityRegexp: environment.Signing.Identity,
		Issuer: environment.Signing.Issuer, MinimumDiskEvidence: lifecycle.DiskEvidenceBuildProvenance}
	verified, receipt, err := consumer.VerifyExactInputs(verifyCtx, inputs.Reference, policy, expectation)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, verified.Close()) }()
	description, err := verified.Description()
	if err != nil {
		return err
	}
	subject = description.Subject
	publicationRoot := filepath.Join(workspace, "publication")
	if err = verified.ExportForInvocation(verifyCtx, receipt, inputs.ExpectedInvocation, publicationRoot); err != nil {
		return err
	}
	_, rootDigest, found := strings.Cut(description.Subject, "@")
	if !found {
		return e.New(e.CodeVerifyFailed, "verified publication omits its immutable root")
	}
	publication, err := delivery.OpenPublication(verifyCtx, publicationRoot, rootDigest)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, publication.Close()) }()
	bindings, err := nightlyMaterials(ctx, runner, profile, plan, description, publication, publicationRoot, source)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "staging the authenticated nightly candidate")
	}
	// Verification credentials never reach a runtime command or simulator.
	runner.Env = SetEnv(runner.Env, bindings)
	phase, suite = "runtime", filepath.Join(workspace, "suite")
	return nightlyRuntime(ctx, runner, source, profile, description)
}

func nightlySource(ctx context.Context, runner Runner, source, commit string) error {
	if !filepath.IsAbs(source) || source == runner.Work || insidePath(runner.Work, source) || insidePath(source, runner.Work) {
		return e.New(e.CodeUsage, "require an independent absolute private source checkout")
	}
	info, err := os.Lstat(source)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !currentOwner(info) {
		return e.New(e.CodeVerifyFailed, "source checkout must be a regular owned directory")
	}
	baseline, err := sourcegit.Baseline(environmentValue(runner.Env, "PATH"))
	if err != nil {
		return err
	}
	for _, check := range [][]string{{"rev-parse", "HEAD"}, {"status", "--porcelain", "--ignored", "--untracked-files=all"}} {
		command, commandErr := sourcegit.LocalCommand(ctx, baseline, source, check...)
		if commandErr != nil {
			return commandErr
		}
		var output strings.Builder
		command.Stdout = &output
		if commandErr = ownedcmd.Run(ctx, command); commandErr != nil {
			return e.Wrap(e.CodeVerifyFailed, commandErr, "checking trusted suite source")
		}
		if check[0] == "rev-parse" && strings.TrimSpace(output.String()) != commit || check[0] == "status" && output.Len() != 0 {
			return e.New(e.CodeVerifyFailed, "suite source differs from its reviewed pin or contains additional source files")
		}
	}
	if err = sourcegit.CheckWorktreeIndex(ctx, baseline, source, 100000); err != nil {
		return err
	}
	_, err = boundedRegularRead(filepath.Join(source, "controller/hack/ci/nico-vms-candidate.sh"), diagnosticLimit)
	return err
}

func nightlyBundle(ctx context.Context, publication *delivery.Publication, work, output string) (map[string]string, error) {
	directory := filepath.Join(work, output)
	bundle, err := publication.ExtractBinaryBundle(ctx, output, directory)
	if err != nil {
		return nil, err
	}
	if bundle.OS != "linux" || bundle.Arch != "arm64" {
		return nil, e.New(e.CodeEngineCapability, "nightly requires native ARM64 tools")
	}
	return nightlyBundleEntries(bundle, directory)
}

func nightlyBundleEntries(bundle delivery.BinaryBundle, directory string) (map[string]string, error) {
	entries := make(map[string]string, len(bundle.Entries))
	for _, entry := range bundle.Entries {
		if !filepath.IsLocal(entry.Name) || filepath.Base(entry.Name) != entry.Name {
			return nil, e.New(e.CodeVerifyFailed, "bundle entry is not a flat local filename")
		}
		role := filepath.Base(entry.InstallPath)
		if _, duplicate := entries[role]; duplicate {
			return nil, e.New(e.CodeVerifyFailed, "bundle has ambiguous installation role: %s", role)
		}
		entries[role] = filepath.Join(directory, entry.Name)
	}
	return entries, nil
}

func nightlyMaterials(ctx context.Context, runner Runner, profile NightlyProfile, plan *factory.Plan, release lifecycle.Release, publication *delivery.Publication, publicationRoot, source string) (map[string]string, error) {
	controllerVersion, err := nightlyControllerVersion(plan)
	if err != nil {
		return nil, err
	}
	work := runner.Work
	cli, err := nightlyBundle(ctx, publication, work, "nvkectl")
	if err != nil {
		return nil, err
	}
	tools, err := nightlyBundle(ctx, publication, work, "installer-tools-arm64")
	if err != nil {
		return nil, err
	}
	simulator, err := nightlyBundle(ctx, publication, work, "nicosim-tools-arm64")
	if err != nil {
		return nil, err
	}
	for _, key := range []string{"kind", "kubectl", "helm", "clusterctl", "oras"} {
		if tools[key] == "" {
			return nil, fmt.Errorf("authenticated installer bundle omits %s", key)
		}
	}
	if cli["nvkectl"] == "" || simulator["nico-sim"] == "" || simulator["nicosim-runtime-assets.tar.gz"] == "" {
		return nil, errors.New("authenticated runtime bundle is incomplete")
	}
	assets := filepath.Join(work, "sim-assets")
	if err = os.Mkdir(assets, 0o700); err != nil {
		return nil, err
	}
	if err = SafeExtractAssets(ctx, simulator["nicosim-runtime-assets.tar.gz"], assets); err != nil {
		return nil, err
	}
	if _, err = projectSeed(ctx, assets); err != nil {
		return nil, err
	}
	disk := filepath.Join(work, "nvke-node-image-arm64.qcow2")
	file, err := os.OpenFile(disk, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	_, writeErr := publication.WriteDiskPayload(ctx, profile.DiskOutput, file)
	if err = errors.Join(writeErr, file.Close()); err != nil {
		return nil, err
	}
	images := []nightlyImageInput{}
	for _, artifact := range release.Artifacts {
		if artifact.Kind != lifecycle.OutputOCI {
			continue
		}
		archive := filepath.Join(work, artifact.Name+".tar")
		exported, exportErr := publication.ExportImage(ctx, artifact.Name, artifact.Reference, "linux/arm64", archive)
		if exportErr != nil {
			return nil, exportErr
		}
		images = append(images, nightlyImageInput{artifact.Name, exported.Reference, exported.ManifestDigest, archive, exported.ArchiveDigest})
	}
	imageInputs := filepath.Join(work, "image-inputs.json")
	if err = privateJSON(imageInputs, images); err != nil {
		return nil, err
	}
	profileDirectory, err := nightlyProfiles(ctx, publicationRoot, release, profile.ProfilesOutput, work)
	if err != nil {
		return nil, err
	}
	releases := filepath.Join(work, "releases")
	crRunner := runner
	crRunner.directory = filepath.Join(source, "controller")
	if err = crRunner.Run(ctx, []string{"go", "run", "./hack/osprofiles-to-crs", "--profiles", filepath.Join(profileDirectory, "profiles"), "--output", releases,
		"--crds", "../management-engine/config/crd/bases", "--gitops=false", "--license", "Apache-2.0"}, "candidate-profile-resources", 5*time.Minute); err != nil {
		return nil, err
	}
	values, err := nightlyRuntimeValues(release)
	if err != nil {
		return nil, err
	}
	valuesDirectory := filepath.Join(work, "values")
	if err = os.Mkdir(valuesDirectory, 0o700); err != nil {
		return nil, err
	}
	for _, item := range []string{"spire", "postgresql", "temporal"} {
		if err = privateJSON(filepath.Join(valuesDirectory, item+".json"), values[item]); err != nil {
			return nil, err
		}
	}
	releaseFile := filepath.Join(work, "release.json")
	view, err := publication.Description(release.Subject)
	if err != nil {
		return nil, err
	}
	releaseJSON, err := releaseview.Render(view, releaseview.Options{Format: "json"})
	if err != nil {
		return nil, err
	}
	if err = privateWrite(releaseFile, releaseJSON); err != nil {
		return nil, err
	}
	bindings := map[string]string{
		"PATH":                      filepath.Dir(tools["kind"]) + ":" + environmentValue(runner.Env, "PATH"),
		"NKE_E2E_CANDIDATE_RELEASE": releaseFile, "NKE_E2E_CANDIDATE_LAYOUT": publicationRoot,
		"NKE_E2E_CANDIDATE_NVKECTL": cli["nvkectl"], "NKE_E2E_CANDIDATE_SIMULATOR": simulator["nico-sim"],
		"NKE_E2E_CANDIDATE_SEED": filepath.Join(assets, "laptop-seed.yaml"), "NKE_E2E_CANDIDATE_QCOW2": disk,
		"NKE_E2E_CANDIDATE_RELEASES": releases, "NKE_E2E_CANDIDATE_PROFILES": filepath.Join(profileDirectory, "profiles"),
		"NKE_E2E_CANDIDATE_OS_PROFILE": profile.OSProfile, "NKE_E2E_CANDIDATE_KUBERNETES_VERSION": profile.KubernetesVersion,
		"NKE_E2E_CANDIDATE_CONTROLLER_VERSION": controllerVersion,
		"NKE_E2E_CANDIDATE_IMAGE_INPUTS":       imageInputs, "NKE_E2E_CANDIDATE_VALUES": valuesDirectory,
		"NKE_E2E_CANDIDATE_MIN_AVAILABLE_MIB": "24576", "NKE_E2E_CANDIDATE_MIN_DISK_MIB": "92160",
		"NKE_E2E_LABEL_FILTER": profile.LabelFilter, "NKE_E2E_STATE_DIR": filepath.Join(work, "suite"), "NKE_E2E_KEEP": "",
		"NICO_VMS_LIBVIRT_URI": "qemu:///session", "NICO_VMS_BRIDGE_NAME": BridgeName, "NKE_CI_HOST_EXTERNAL_DHCP": "true",
	}
	for _, key := range []string{"NVKECTL", "SIMULATOR", "SEED", "QCOW2"} {
		digest, digestErr := DigestFile(ctx, bindings["NKE_E2E_CANDIDATE_"+key])
		if digestErr != nil {
			return nil, digestErr
		}
		bindings["NKE_E2E_CANDIDATE_"+key+"_SHA256"] = digest
	}
	return bindings, nil
}

// Image layer interpretation belongs to go-containerregistry's existing
// whiteout-aware extractor. The existing bounded asset owner admits filesystem
// output; this code supplies no competing OCI or archive parser.
func nightlyProfiles(ctx context.Context, root string, release lifecycle.Release, output, work string) (string, error) {
	var selected *lifecycle.Artifact
	for i := range release.Artifacts {
		if release.Artifacts[i].Name == output && release.Artifacts[i].Kind == lifecycle.OutputOCI {
			selected = &release.Artifacts[i]
		}
	}
	if selected == nil {
		return "", errors.New("authenticated release omits its profiles image")
	}
	store, err := layout.FromPath(root)
	if err != nil {
		return "", err
	}
	hash, err := v1.NewHash(selected.Digest)
	if err != nil {
		return "", err
	}
	image, err := store.Image(hash)
	if err != nil {
		return "", err
	}
	extracted := mutate.Extract(image)
	defer func() { _ = extracted.Close() }()
	archive := filepath.Join(work, "profiles-assets.tar.gz")
	file, err := os.OpenFile(archive, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	zipped := gzip.NewWriter(file)
	copyErr := nightlyProfileArchive(ctx, extracted, zipped)
	err = errors.Join(copyErr, zipped.Close(), file.Close())
	if err != nil {
		return "", err
	}
	destination := filepath.Join(work, "profiles-assets")
	if err = os.Mkdir(destination, 0o700); err != nil {
		return "", err
	}
	if err = safeExtractAssets(ctx, archive, destination, []string{"profiles/osProfiles/metadata.yaml"}); err != nil {
		return "", err
	}
	return destination, nil
}

func nightlyProfileArchive(ctx context.Context, input io.Reader, output io.Writer) (resultErr error) {
	if ctx == nil {
		return e.New(e.CodeUsage, "profile projection requires a context")
	}
	defer func() {
		if resultErr == nil {
			return
		}
		if ctx.Err() != nil {
			resultErr = e.Wrap(e.CodeTimeout, ctx.Err(), "profile projection canceled")
			return
		}
		var coded *e.Error
		if !errors.As(resultErr, &coded) {
			resultErr = e.Wrap(e.CodeVerifyFailed, resultErr, "projecting bounded profile archive")
		}
	}()
	limited := &io.LimitedReader{R: input, N: 128<<20 + 1}
	reader := tar.NewReader(limited)
	writer := tar.NewWriter(output)
	defer func() { resultErr = errors.Join(resultErr, writer.Close()) }()
	var total int64
	seen := map[string]bool{}
	for count := 0; ; count++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := reader.Next()
		if err == io.EOF {
			if limited.N <= 0 {
				return errors.New("profiles image filesystem exceeds byte bound")
			}
			return nil
		}
		if err != nil {
			return err
		}
		member := strings.TrimSuffix(header.Name, "/")
		if count >= 4096 || !filepath.IsLocal(member) || filepath.Clean(member) != member {
			return errors.New("profiles image contains an unsafe or excessive member list")
		}
		if member != "profiles" && !strings.HasPrefix(member, "profiles/") && member != "runbooks" && !strings.HasPrefix(member, "runbooks/") {
			continue
		}
		if header.Typeflag != tar.TypeDir && header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > (16<<20)-total || header.Mode&0o7000 != 0 || seen[member] || header.Typeflag == tar.TypeDir && header.Size != 0 {
			return errors.New("profiles image content is not bounded regular data")
		}
		seen[member] = true
		total += header.Size
		if err = writer.WriteHeader(header); err != nil {
			return err
		}
		if _, err = transportarchive.CopyExact(ctx, writer, reader, header.Size); err != nil {
			return err
		}
	}
}

func nightlyRuntimeValues(release lifecycle.Release) (map[string]any, error) {
	outputs := make(map[string]SelectedOutput)
	references := make(map[string]name.Digest)
	for _, key := range runtimeImages {
		for _, artifact := range release.Artifacts {
			if artifact.Name != key+"-image-arm64" || artifact.Kind != lifecycle.OutputOCI {
				continue
			}
			if _, duplicate := references[key]; duplicate {
				return nil, e.New(e.CodeVerifyFailed, "runtime role has duplicated outputs: %s", key)
			}
			reference, err := name.NewDigest(artifact.Reference, name.StrictValidation)
			if err != nil || reference.DigestStr() != artifact.Digest {
				return nil, e.New(e.CodeVerifyFailed, "runtime image has no exact immutable reference")
			}
			outputs["runtime-"+key] = SelectedOutput{Observation: artifact}
			references[key] = reference
		}
		if _, ok := references[key]; !ok {
			return nil, e.New(e.CodeVerifyFailed, "signed runtime image is missing: %s", key)
		}
	}
	// Preserve the existing chart-role owner. JSON is descriptive Helm input;
	// only registry/repository transport fields are projected here.
	raw, err := json.Marshal(runtimeValues("nightly.invalid", outputs))
	if err != nil {
		return nil, err
	}
	var mapped map[string]any
	if err = json.Unmarshal(raw, &mapped); err != nil {
		return nil, err
	}
	var project func(map[string]any)
	project = func(object map[string]any) {
		if repository, ok := object["repository"].(string); ok {
			role := strings.TrimPrefix(strings.TrimPrefix(repository, "nightly.invalid/"), "nvkr-local/")
			if reference, selected := references[role]; selected {
				if registry, split := object["registry"].(string); split && registry != "" {
					object["registry"] = reference.RegistryStr()
					object["repository"] = reference.RepositoryStr()
				} else {
					object["repository"] = reference.Context().Name()
				}
			}
		}
		for _, member := range object {
			if child, ok := member.(map[string]any); ok {
				project(child)
			}
		}
	}
	project(mapped)
	return mapped, nil
}

func nightlyRuntime(ctx context.Context, runner Runner, source string, profile NightlyProfile, release lifecycle.Release) (resultErr error) {
	lockPath := filepath.Join(os.TempDir(), "nvkr-nightly-runtime.lock")
	fd, err := unix.Open(lockPath, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	lock := os.NewFile(uintptr(fd), lockPath)
	defer func() { _ = lock.Close() }()
	info, err := lock.Stat()
	if err != nil || !info.Mode().IsRegular() || !currentOwner(info) || info.Mode().Perm() != 0o600 {
		return e.New(e.CodeEngineCapability, "runtime lock is not a private owned file")
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "another nightly attempt owns this runtime")
	}
	network, err := admitKindNetwork(ctx, runner)
	if err != nil {
		return err
	}
	guard, err := NewNetworkGuard(ctx, runner, network.CIDR, os.Getenv("NVKR_NIGHTLY_NETWORK_HELPER"))
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Minute)
		defer cancel()
		resultErr = errors.Join(resultErr, guard.Close(cleanup))
	}()
	if err = guard.Start(ctx); err != nil {
		return err
	}
	if err = guard.AllowKindSimulator(ctx, network.Gateway); err != nil {
		return err
	}
	simulatorPath := filepath.Join(guard.Work, "nico-sim")
	if err = copyPrivate(ctx, environmentValue(runner.Env, "NKE_E2E_CANDIDATE_SIMULATOR"), simulatorPath, 0o700); err != nil {
		return err
	}
	copiedDigest, err := DigestFile(ctx, simulatorPath)
	if err != nil || copiedDigest != environmentValue(runner.Env, "NKE_E2E_CANDIDATE_SIMULATOR_SHA256") {
		return e.New(e.CodeVerifyFailed, "owned simulator copy differs from its captured candidate")
	}
	runner.Env = SetEnv(runner.Env, map[string]string{"NKE_E2E_CANDIDATE_SIMULATOR": simulatorPath})
	if err = guard.Grant(ctx, simulatorPath, "cap_net_admin=ep"); err != nil {
		return err
	}
	runner.Env = SetEnv(runner.Env, map[string]string{
		"NKE_E2E_CANDIDATE_RUNTIME_LOCK": lockPath, "NKE_E2E_CANDIDATE_NETWORK_OWNER": "nvkr-network-guard",
		"NKE_E2E_CANDIDATE_DHCP_HOSTS_DIR": filepath.Join(guard.Work, "dhcp-hosts"), "NKE_E2E_CANDIDATE_DHCP_LEASE_FILE": filepath.Join(guard.Work, "leases"),
		"NKE_E2E_CANDIDATE_DHCP_PID":          fmt.Sprint(guard.dns.PID()),
		"NKE_E2E_CANDIDATE_SIMULATOR_ADDRESS": network.Gateway, "NKE_E2E_CANDIDATE_SIMULATOR_ENDPOINT": "http://" + network.Gateway + ":18089",
		"KIND_CLUSTER_NAME": "nvkr-nightly-" + profile.Bundle,
	})
	disk := environmentValue(runner.Env, "NKE_E2E_CANDIDATE_QCOW2")
	service, err := startDiskService(context.WithoutCancel(ctx), disk, GuestGateway+":18080")
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		resultErr = errors.Join(resultErr, service.Stop(cleanup))
	}()
	var image lifecycle.Artifact
	for _, item := range release.Artifacts {
		if item.Name == profile.DiskOutput {
			image = item
		}
	}
	if err = verifyHTTPDisk(ctx, service.url, image); err != nil {
		return err
	}
	if err = bootstrapCA(ctx, ManagementSession{Work: runner.Work, Runner: runner}); err != nil {
		return err
	}
	var site siteConfiguration
	site.Site.Name = environmentValue(runner.Env, "KIND_CLUSTER_NAME")
	site.Site.Arch = "arm64"
	// NVKE's management kube context derives this same domain for its OIDC
	// assertions. Keep the rendered site identity bound to that context.
	site.Site.TrustDomain = site.Site.Name + ".nke.local"
	site.Site.VIP = GuestGateway
	site.Spire.CACertFile = filepath.Join(runner.Work, "ca.crt")
	site.Spire.CAKeyFile = filepath.Join(runner.Work, "ca.key")
	site.Nico.Endpoint = "http://" + network.Gateway + ":18089"
	site.Nico.OrgID = "novxwn552ssc"
	site.Nico.Token = "nico-vmsctl-token"
	site.Nico.APIName = "forge"
	for _, item := range release.Artifacts {
		if item.Name == "cwe" {
			site.Images.Controller = item.Reference
		}
		if item.Name == profile.ProfilesOutput {
			site.Images.Profiles = item.Reference
		}
	}
	site.Registry.Server = "ghcr.io"
	site.Registry.Username = "local-nightly"
	site.Registry.Token, err = randomScalar()
	if err != nil {
		return err
	}
	site.Tenant.User = "nvkr-nightly"
	site.Tenant.Password, err = randomScalar()
	if err != nil {
		return err
	}
	siteRaw, err := yaml.Marshal(site)
	if err != nil {
		return err
	}
	siteFile := filepath.Join(runner.Work, "sitespec.yaml")
	if err = privateWrite(siteFile, siteRaw); err != nil {
		return err
	}
	runner.Env = SetEnv(runner.Env, map[string]string{"NKE_E2E_CANDIDATE_SITESPEC": siteFile})
	command := exec.CommandContext(ctx, "bash", filepath.Join(source, "controller/hack/ci/nico-vms-e2e.sh"))
	command.Env = runner.Env
	command.Dir = source
	command.ExtraFiles = []*os.File{nil, nil, nil, nil, nil, nil, lock} // inherited FD9 holds the same lease
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err = ownedcmd.ConfigureCancellation(command, 20*time.Minute); err != nil {
		return err
	}
	if err = command.Start(); err != nil {
		return err
	}
	waitErr := command.Wait()
	killErr := ownedcmd.KillDescendants(command)
	if errors.Is(killErr, os.ErrProcessDone) {
		killErr = nil
	}
	// The product suite owns assertions, KVM observations and its cleanup report.
	// Read its terminal record even after cancellation; a failed test can still
	// have joined cleanup. Missing cleanup evidence retains the network journal.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	raw, readErr := readRunFile(cleanup, filepath.Join(runner.Work, "suite"), "candidate-outcome.json", diagnosticLimit)
	clean, verdictErr := nightlyCandidateVerdict(raw, profile)
	forced := command.ProcessState == nil
	if command.ProcessState != nil {
		status, ok := command.ProcessState.Sys().(unix.WaitStatus)
		forced = ok && status.Signaled() && status.Signal() == unix.SIGKILL
	}
	if readErr != nil || !clean || forced || killErr != nil {
		guard.unjoined.Store(true)
		resultErr = e.Wrap(e.CodeGateUnmet, errors.Join(readErr, verdictErr, ErrUnjoined), "candidate cleanup is unknown; retained controls require recovery from private journal %s", guard.Work)
	} else if err = guard.RequireIdle(cleanup); err != nil {
		guard.unjoined.Store(true)
		resultErr = e.Wrap(e.CodeGateUnmet, err, "candidate resources remain active; retained controls require recovery from private journal %s", guard.Work)
	}
	if err = errors.Join(waitErr, killErr, ctx.Err()); err != nil {
		resultErr = errors.Join(resultErr, e.Wrap(e.CodeBuildFailed, err, "NVKE runtime suite or cleanup failed"))
	}
	return errors.Join(resultErr, verdictErr)
}

func nightlySecurityAdmission(profile NightlyProfile) error {
	if profile.SecurityStatus != "admitted" {
		return e.New(e.CodeEngineCapability, "nightly profile security admission is blocked: %s", profile.BlockedReason)
	}
	return nil
}

func nightlyPreparationEnvironment(native []string, binding map[string]string, work string) ([]string, error) {
	if !filepath.IsAbs(binding["HOME"]) || !filepath.IsAbs(binding["XDG_RUNTIME_DIR"]) {
		return nil, e.New(e.CodeEngineCapability, "native account environment omits home or runtime directory")
	}
	for _, name := range []string{"go-cache", "go-modules", "go-path"} {
		if err := os.Mkdir(filepath.Join(work, name), 0o700); err != nil {
			return nil, e.Wrap(e.CodeImmutableViolation, err, "creating private preparation cache")
		}
	}
	return SetEnv(SetEnv(native, binding), map[string]string{
		"GOTOOLCHAIN": "local", "GOENV": "off", "GOWORK": "off", "GOFLAGS": "-mod=readonly",
		"GOCACHE": filepath.Join(work, "go-cache"), "GOMODCACHE": filepath.Join(work, "go-modules"), "GOPATH": filepath.Join(work, "go-path"),
	}), nil
}

func nightlyPublicDiagnostics(ctx context.Context, suite, public string) error {
	marker, info, err := securepath.OpenRegular(filepath.Join(suite, "SAFE_TO_UPLOAD"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "diagnostic marker is not a regular file")
	}
	defer marker.Close()
	if !currentOwner(info) || info.Mode().Perm() != 0o600 || info.Size() != 0 {
		return e.New(e.CodeVerifyFailed, "diagnostic marker is not an empty private owned file")
	}
	for _, name := range []string{"junit.xml", "candidate-outcome.json"} {
		raw, err := readRunFile(ctx, suite, name, diagnosticLimit)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "reading allowlisted nightly report")
		}
		if err := privateWrite(filepath.Join(public, name), raw); err != nil {
			return e.Wrap(e.CodeImmutableViolation, err, "writing allowlisted nightly report")
		}
	}
	return nil
}

func nightlyCandidateVerdict(raw []byte, profile NightlyProfile) (bool, error) {
	var verdict struct {
		CleanupExit *int   `json:"cleanup_exit"`
		TestExit    *int   `json:"test_exit"`
		EntryExit   *int   `json:"entry_exit"`
		Profile     string `json:"profile"`
		Kubernetes  string `json:"kubernetes"`
	}
	if err := nightlyJSON(raw, &verdict, false); err != nil {
		return false, err
	}
	clean := verdict.CleanupExit != nil && *verdict.CleanupExit == 0 && verdict.Profile == profile.OSProfile && verdict.Kubernetes == profile.KubernetesVersion
	if verdict.CleanupExit == nil || verdict.TestExit == nil || verdict.EntryExit == nil || *verdict.CleanupExit != 0 || *verdict.TestExit != 0 || *verdict.EntryExit != 0 || verdict.Profile != profile.OSProfile || verdict.Kubernetes != profile.KubernetesVersion {
		return clean, e.New(e.CodeGateUnmet, "NVKE omitted a passed test or cleanup verdict")
	}
	return clean, nil
}

func nightlyControllerVersion(plan *factory.Plan) (string, error) {
	if plan == nil {
		return "", e.New(e.CodeVerifyFailed, "candidate omits its exact producer plan")
	}
	builds, nodes, bindings := 0, 0, 0
	version := ""
	for _, build := range plan.Builds {
		if build.Name != "cwe" {
			continue
		}
		builds++
		if build.Graph == nil {
			continue
		}
		for _, node := range build.Graph.Nodes {
			if node.Kind != ir.KindBuild || node.Component != "nvke-controller" {
				continue
			}
			nodes++
			for _, binding := range node.Env {
				if binding.Name == "BUILD_VERSION" {
					bindings++
					version = binding.Value
				}
			}
		}
	}
	if builds != 1 || nodes != 1 || bindings != 1 || strings.TrimSpace(version) == "" || strings.Contains(version, "{{") {
		return "", e.New(e.CodeVerifyFailed, "CWE producer must bind one resolved BUILD_VERSION")
	}
	return version, nil
}
