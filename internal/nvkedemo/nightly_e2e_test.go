// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/sourcegit"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/releaseview"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
	"github.com/google/go-containerregistry/pkg/name"
	"golang.org/x/sys/unix"
)

// This is the scheduled workflow's integration test, not a product command.
// It consumes a completed, authenticated publication and executes NVKE's own
// suite. Ordinary repository tests allocate no runtime resources.
func TestNightlyPublishedCandidate(t *testing.T) {
	enabled, err := nightlyTestAdmission(os.Getenv("NVKR_NIGHTLY_ENABLE"), os.Getenv("GITHUB_ACTIONS"), os.Getenv("NVKR_NIGHTLY_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Skip("nightly integration is explicitly enabled by its protected GitHub job")
	}
	deadline, _ := t.Deadline()
	timing, err := nightlyTimeBudget(time.Now(), deadline)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := nightlySignalContext(t.Context(), timing.WorkDeadline)
	defer cancel()
	if err := runNightlyPublishedCandidate(ctx, timing); err != nil {
		t.Fatal(err)
	}
}

const (
	nightlyMaximumWork    = 305 * time.Minute
	nightlySuiteGrace     = 20 * time.Minute
	nightlyVerdictGrace   = time.Minute
	nightlyDiskGrace      = time.Minute
	nightlyNetworkGrace   = 8 * time.Minute
	nightlyReportGrace    = time.Minute
	nightlyDeadlineMargin = 4 * time.Minute
	nightlyCleanupReserve = nightlySuiteGrace + nightlyVerdictGrace + nightlyDiskGrace + nightlyNetworkGrace + nightlyReportGrace + nightlyDeadlineMargin
)

type nightlyTiming struct{ WorkDeadline, CleanupDeadline time.Time }

func nightlyTimeBudget(now, testDeadline time.Time) (nightlyTiming, error) {
	if testDeadline.IsZero() {
		testDeadline = now.Add(nightlyMaximumWork + nightlyCleanupReserve)
	}
	work := now.Add(nightlyMaximumWork)
	if admitted := testDeadline.Add(-nightlyCleanupReserve); admitted.Before(work) {
		work = admitted
	}
	if !work.After(now) {
		return nightlyTiming{}, e.New(e.CodeEngineCapability, "nightly test deadline leaves no work time after bounded cleanup")
	}
	return nightlyTiming{WorkDeadline: work, CleanupDeadline: testDeadline.Add(-nightlyDeadlineMargin)}, nil
}

func nightlySignalContext(parent context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	work, cancel := context.WithDeadline(parent, deadline)
	ctx, stop := signal.NotifyContext(work, syscall.SIGINT, syscall.SIGTERM)
	return ctx, func() { stop(); cancel() }
}

func (timing nightlyTiming) cleanupContext(parent context.Context, maximum time.Duration) (context.Context, context.CancelFunc) {
	deadline := time.Now().Add(maximum)
	if timing.CleanupDeadline.Before(deadline) {
		deadline = timing.CleanupDeadline
	}
	return context.WithDeadline(context.WithoutCancel(parent), deadline)
}

func nightlyTestAdmission(enable, actions, profile string) (bool, error) {
	if enable == "1" {
		return true, nil
	}
	if actions == "true" && profile != "" {
		return false, e.New(e.CodeUsage, "nightly GitHub runtime requires explicit enablement; refusing a successful skip")
	}
	return false, nil
}

type nightlyImageInput struct {
	Name           string `json:"name"`
	Reference      string `json:"reference"`
	ManifestDigest string `json:"manifestDigest"`
	Archive        string `json:"archive"`
	ArchiveDigest  string `json:"archiveDigest"`
}

func runNightlyPublishedCandidate(ctx context.Context, timing nightlyTiming) (resultErr error) {
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
		cleanup, cancel := timing.cleanupContext(ctx, nightlyReportGrace)
		defer cancel()
		if suite != "" {
			resultErr = errors.Join(resultErr, nightlyPublicDiagnostics(cleanup, suite, public))
		}
		if ctx.Err() != nil {
			resultErr = errors.Join(resultErr, e.Wrap(e.CodeTimeout, ctx.Err(), "nightly cycle canceled before its final verdict"))
		}
		resultErr = errors.Join(resultErr, writeNightlyCycle(cleanup, public, profile, build, subject, phase, resultErr))
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
	if !environment.GitHubRuntimePlan {
		if err = nightlySource(ctx, runner, source, profile.HarnessCommit); err != nil {
			return err
		}
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
	inputs, err := newNightlyInputs(ctx, profile, build, plan, definition, environment, workspace)
	if err != nil {
		return err
	}
	plan = inputs.Plan
	expectation, err := lifecycle.PrepareExactInputs(definition, environment, plan)
	if err != nil {
		return err
	}
	if environment.GitHubRuntimePlan {
		if err = nightlySource(ctx, runner, source, inputs.HarnessCommit); err != nil {
			return err
		}
	}
	consumer, err := lifecycle.NewConsumer(lifecycle.ConsumerOptions{
		CosignBinary: os.Getenv("NVKR_NIGHTLY_COSIGN"), CosignDigest: os.Getenv("NVKR_NIGHTLY_COSIGN_DIGEST"),
		WorkspaceRoot: filepath.Join(workspace, "verified"),
		Registry:      delivery.RegistryOptions{Username: os.Getenv("NVKR_REGISTRY_READ_USER"), Password: os.Getenv("NVKR_REGISTRY_READ")},
		Credential:    sign.Credential{Registry: "ghcr.io", Username: os.Getenv("NVKR_REGISTRY_READ_USER"), Password: os.Getenv("NVKR_REGISTRY_READ")},
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
	bindings, err := nightlyMaterials(ctx, runner, profile, plan, description, publication, publicationRoot)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "staging the authenticated nightly candidate")
	}
	// Verification credentials never reach a runtime command or simulator.
	runner.Env = SetEnv(runner.Env, bindings)
	phase, suite = "runtime", filepath.Join(workspace, "suite")
	runtimeProfile := profile
	runtimeProfile.HarnessCommit = inputs.HarnessCommit
	return nightlyRuntime(ctx, timing, runner, source, runtimeProfile, description)
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
			return e.New(e.CodeVerifyFailed, "suite source differs from its configured pin or contains additional source files")
		}
	}
	if err = sourcegit.CheckWorktreeIndex(ctx, baseline, source, 100000); err != nil {
		return err
	}
	for _, entrypoint := range []string{"controller/hack/ci/nico-vms-candidate.sh", "controller/hack/ci/nico-vms-e2e.sh"} {
		if _, err = boundedRegularRead(filepath.Join(source, entrypoint), diagnosticLimit); err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "admitting pinned suite entrypoint %s", entrypoint)
		}
	}
	return nil
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

func nightlyMaterials(ctx context.Context, runner Runner, profile NightlyProfile, plan *factory.Plan, release lifecycle.Release, publication *delivery.Publication, publicationRoot string) (map[string]string, error) {
	for _, output := range []string{nightlyControllerOutput(profile), profile.ProfilesOutput} {
		if _, err := nightlyNamedImage(release, output); err != nil {
			return nil, err
		}
	}
	controllerVersion, err := nightlyControllerVersion(plan, nightlyControllerOutput(profile))
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
	profileDirectory, err := nightlyProfiles(ctx, publication, release, profile.ProfilesOutput, work)
	if err != nil {
		return nil, err
	}
	releases := filepath.Join(work, "releases")
	if err = nightlyProfileResources(ctx, profile, release, profileDirectory, releases); err != nil {
		return nil, err
	}
	values, err := nightlyRuntimeValues(plan, release)
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

func writeNightlyCycle(ctx context.Context, public string, profile NightlyProfile, build NightlyBuild, subject, phase string, resultErr error) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "nightly cycle report requires a context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "nightly cycle report canceled")
	}
	if err := privateJSON(filepath.Join(public, "cycle.json"), map[string]any{
		"profile": profile.Name, "releaseSubject": subject, "phase": phase,
		"runtimePassed": phase == "runtime" && resultErr == nil, "qualificationAuthority": "none",
		"producerRun": build.ProducerRun, "producerAttempt": build.ProducerAttempt, "sourceCommit": build.SourceCommit,
		"coordinatorRun": build.CoordinatorRun, "coordinatorAttempt": build.CoordinatorAttempt,
	}); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "writing private nightly cycle report")
	}
	return nil
}

// The publication owner reads and verifies the held native image, applies
// upstream whiteout semantics and admits bounded regular profile files.
func nightlyProfiles(ctx context.Context, publication *delivery.Publication, release lifecycle.Release, output, work string) (string, error) {
	if _, err := nightlyNamedImage(release, output); err != nil {
		return "", err
	}
	destination := filepath.Join(work, "profiles-assets")
	if err := os.Mkdir(destination, 0o700); err != nil {
		return "", e.Wrap(e.CodeImmutableViolation, err, "creating private profiles destination")
	}
	if err := publication.ExtractImageFiles(ctx, output, "linux/arm64", []string{"profiles"}, destination); err != nil {
		return "", err
	}
	if _, err := boundedRegularRead(filepath.Join(destination, "profiles/osProfiles/metadata.yaml"), diagnosticLimit); err != nil {
		return "", e.Wrap(e.CodeVerifyFailed, err, "profiles image omits bounded regular profile metadata")
	}
	return destination, nil
}

func nightlyRuntimeValues(plan *factory.Plan, release lifecycle.Release) (map[string]any, error) {
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
			output := SelectedOutput{Observation: artifact}
			if strings.HasPrefix(key, "temporal-") {
				output.imageTag, err = runtimeImageTag(plan, artifact)
				if err != nil {
					return nil, err
				}
			}
			outputs["runtime-"+key] = output
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

func nightlyRuntime(ctx context.Context, timing nightlyTiming, runner Runner, source string, profile NightlyProfile, release lifecycle.Release) (resultErr error) {
	controller, err := nightlyNamedImage(release, nightlyControllerOutput(profile))
	if err != nil {
		return err
	}
	profiles, err := nightlyNamedImage(release, profile.ProfilesOutput)
	if err != nil {
		return err
	}
	if err = nightlySource(ctx, runner, source, profile.HarnessCommit); err != nil {
		return err
	}
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
		cleanup, cancel := timing.cleanupContext(ctx, nightlyNetworkGrace)
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
	service, err := startNightlyDiskService(context.WithoutCancel(ctx), disk, GuestGateway+":18080", profile)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := timing.cleanupContext(ctx, nightlyDiskGrace)
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
	if profile.DiskOutput != DiskOutputName {
		profileURL := strings.TrimSuffix(service.url, "/"+filepath.Base(disk)) + "/" + profile.DiskOutput + ".qcow2"
		if err = verifyHTTPDisk(ctx, profileURL, image); err != nil {
			return err
		}
	}
	if err = bootstrapCA(ctx, ManagementSession{Work: runner.Work, Runner: runner}); err != nil {
		return err
	}
	siteFile, err := nightlySiteSpec(ctx, runner, source, network.Gateway, controller.Reference, profiles.Reference)
	if err != nil {
		return err
	}
	runner, err = nightlyStackEnvironment(runner, siteFile)
	if err != nil {
		return err
	}
	command, err := nightlySuiteCommand(ctx, runner, source)
	if err != nil {
		return err
	}
	command.Env = runner.Env
	command.Dir = source
	command.ExtraFiles = []*os.File{nil, nil, nil, nil, nil, nil, lock} // inherited FD9 holds the same lease
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err = ownedcmd.ConfigureCancellation(command, nightlySuiteGrace); err != nil {
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
	cleanup, cancel := timing.cleanupContext(ctx, nightlyVerdictGrace)
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

func nightlyControllerOutput(profile NightlyProfile) string {
	if profile.ControllerOutput == "" {
		return "cwe"
	}
	return profile.ControllerOutput
}

func nightlyNamedImage(release lifecycle.Release, output string) (lifecycle.Artifact, error) {
	var selected lifecycle.Artifact
	count := 0
	for _, artifact := range release.Artifacts {
		if artifact.Name == output {
			selected = artifact
			count++
		}
	}
	if output == "" || count != 1 || selected.Kind != lifecycle.OutputOCI {
		return lifecycle.Artifact{}, e.New(e.CodeVerifyFailed, "runtime requires one exact named OCI image %q", output)
	}
	reference, err := name.NewDigest(selected.Reference, name.StrictValidation)
	if err != nil || reference.Name() != selected.Reference || reference.DigestStr() != selected.Digest {
		return lifecycle.Artifact{}, e.New(e.CodeVerifyFailed, "runtime image %q has no canonical immutable reference bound to its digest", output)
	}
	return selected, nil
}

func nightlySuiteCommand(ctx context.Context, runner Runner, source string) (*exec.Cmd, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "nightly suite requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "nightly suite was canceled before admission")
	}
	bash, err := commandPath("bash", runner.Env)
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "admitting nightly suite interpreter")
	}
	entrypoint := filepath.Join(source, "controller/hack/ci/nico-vms-e2e.sh")
	if _, err := boundedRegularRead(entrypoint, diagnosticLimit); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "admitting nightly suite entrypoint")
	}
	command := exec.CommandContext(ctx, bash, entrypoint)
	command.Env, command.Dir = runner.Env, source
	return command, nil
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

func nightlyControllerVersion(plan *factory.Plan, output string) (string, error) {
	if plan == nil {
		return "", e.New(e.CodeVerifyFailed, "candidate omits its exact producer plan")
	}
	builds, nodes, bindings := 0, 0, 0
	version := ""
	for _, build := range plan.Builds {
		if build.Name != output {
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
