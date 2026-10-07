//go:build linux

package factory

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/material"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

// The ELF implements a controlled local-daemon protocol, not Docker, Packer,
// guestfish, Syft or a real NKE image. Opaque owners still come exclusively from
// the public pipeline/inspection APIs. Full issuance needs genuinely observed
// host headroom above the production composition allowance; ordinary private
// GitHub runners cannot supply that allowance. Requested runs never skip on
// capacity failure: NKD_TEST_DISK_EVIDENCE_RUNTIME=1 go test -race
// ./pkg/nkd/factory -run '^TestDiskEvidencePublicRuntimeIssuer$' -count=1 -v.
func TestDiskEvidencePublicRuntimeIssuer(t *testing.T) {
	if os.Getenv("NKD_TEST_DISK_EVIDENCE_RUNTIME") != "1" {
		t.Skip("explicit native issuer contract requires NKD_TEST_DISK_EVIDENCE_RUNTIME=1 and real composition capacity; ordinary CI is not issuance proof")
	}
	for _, test := range []struct {
		name string
		want e.Code
	}{
		{"issued", ""},
		{"closed inspector", e.CodeEngineCapability},
		{"closed original", e.CodeVerifyFailed},
		{"wrong capture architecture", e.CodeVerifyFailed},
		{"substituted report subject", e.CodeVerifyFailed},
		{"missing installed correspondence", e.CodeProvenanceMissing},
		{"cancel during root scan", e.CodeTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
			defer cancel()
			fixture := newPublicDiskEvidenceFixture(t, ctx, test.name)
			facts, err := execution.ObserveLocalHost(ctx, fixture.workspace)
			if err != nil {
				t.Fatalf("observing required native host: %v", err)
			}
			// This diagnostic is not admission. The concrete runtime below
			// independently applies its owning full resource gate before copying.
			if facts.Resources.MemoryMiB < fixture.profile.Resources.MemoryMiB+1 {
				t.Fatalf("requested positive issuer fixture lacks real host memory: required>=%dMiB observed=%dMiB", fixture.profile.Resources.MemoryMiB+1, facts.Resources.MemoryMiB)
			}
			t.Logf("observed native fixture host: platform=%s CPU=%dm RAM=%dMiB scratch=%dMiB", facts.Platform, facts.Resources.CPUMillis, facts.Resources.MemoryMiB, facts.Resources.EphemeralStorageMiB)
			owner, artifact := fixture.produce(t, ctx)
			inspection, err := OpenDiskEvidenceRuntime(ctx, fixture.plan, fixture.runtime, fixture.workspace)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = inspection.Close() })
			switch test.name {
			case "closed inspector":
				if err := inspection.Close(); err != nil {
					t.Fatal(err)
				}
			case "closed original":
				if err := artifact.Close(); err != nil {
					t.Fatal(err)
				}
			case "cancel during root scan":
				done := make(chan struct{})
				go func() {
					defer close(done)
					ticker := time.NewTicker(time.Millisecond)
					defer ticker.Stop()
					for {
						if _, err := os.Stat(filepath.Join(fixture.root, "scan-started")); err == nil {
							cancel()
							return
						}
						select {
						case <-ctx.Done():
							return
						case <-ticker.C:
						}
					}
				}()
				defer func() { cancel(); <-done }()
			}
			receipt, err := owner.ReconcileDiskEvidence(ctx, fixture.plan, fixture.plan.LifecycleOutputs[0], inspection)
			if test.want != "" {
				assertFactoryErrorCode(t, err, test.want)
				if receipt != nil {
					t.Fatal("rejected public join issued a receipt")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if receipt == nil {
				t.Fatal("public original-owner join returned no receipt")
			}
			digest, size := receipt.Subject()
			if digest != artifact.Digest() || size != artifact.Size() {
				t.Fatal("receipt lost its original Packer subject")
			}
			documents, err := receipt.ComponentDocuments()
			if err != nil || len(documents) != 3 {
				t.Fatalf("complete component documents=%v error=%v", documents, err)
			}
			var output bytes.Buffer
			observed, bom, err := owner.ExportReconciledDisk(ctx, fixture.plan, fixture.plan.LifecycleOutputs[0], inspection, receipt, &output)
			if err != nil || bom == nil || observed.Digest != digest || !bytes.Equal(output.Bytes(), testQCOW2HeaderPayload()) {
				t.Fatalf("original-owner public export: %v", err)
			}
			sibling := localDiskRunResult(t, fixture.plan)
			t.Cleanup(func() { _ = sibling.Close() })
			var rejected bytes.Buffer
			_, document, err := sibling.ExportReconciledDisk(ctx, fixture.plan, fixture.plan.LifecycleOutputs[0], inspection, receipt, &rejected)
			assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
			if document != nil || rejected.Len() != 0 {
				t.Fatal("sibling Run reused the original public receipt")
			}
			if err := owner.Close(); err != nil {
				t.Fatal(err)
			}
			rejected.Reset()
			_, document, err = owner.ExportReconciledDisk(ctx, fixture.plan, fixture.plan.LifecycleOutputs[0], inspection, receipt, &rejected)
			if err == nil || document != nil || rejected.Len() != 0 {
				t.Fatal("closed original owner exported public evidence")
			}
		})
	}
}

// This exercises the strict hosted completion with receipts issued by the real
// public owners over controlled bytes, not a real Docker/Packer/NKE bake.
func TestCompleteHostedDiskPublicationStrictPublicRuntime(t *testing.T) {
	if os.Getenv("NKD_TEST_DISK_EVIDENCE_RUNTIME") != "1" {
		t.Skip("explicit strict completion contract requires NKD_TEST_DISK_EVIDENCE_RUNTIME=1 and real composition capacity")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	fixture := newPublicDiskEvidenceFixture(t, ctx, "hosted publication")
	if diskBuildProvenance(fixture.plan) || fixture.plan.Qualification != TierPipelineAttested {
		t.Fatal("fixture does not select strict pipeline-attested completion")
	}
	owner, artifact := fixture.produce(t, ctx)
	inspection, err := OpenDiskEvidenceRuntime(ctx, fixture.plan, fixture.runtime, fixture.workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inspection.Close() })
	expected := fixture.plan.LifecycleOutputs[0]
	_, artifacts := planIndex(fixture.plan)
	observation, producer, err := owner.inspectDiskLifecycleMetadata(ctx, fixture.plan, expected)
	if err != nil {
		t.Fatal(err)
	}
	wantRecord, err := canonical.Marshal(producer)
	if err != nil {
		t.Fatal(err)
	}
	record := artifacts["result/"+expected.Name+"/"+observation.Component]
	// The held completion, not previously serialized caller bytes, owns the
	// root record that strict reconciliation and packaging consume.
	if err := writeFactoryFile(ctx, record.Path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CompleteHostedDiskPublication(ctx, fixture.plan, expected, owner, inspection); err != nil {
		t.Fatal(err)
	}
	gotRecord, err := os.ReadFile(record.Path)
	if err != nil || !bytes.Equal(gotRecord, wantRecord) {
		t.Fatalf("strict completion did not persist exact held producer bytes: %s, %v", gotRecord, err)
	}
	wrapped := artifacts["package/"+expected.Name]
	held, err := delivery.OpenSingleDiskArtifact(ctx, wrapped.Path, "linux/"+runtime.GOARCH, 64<<30)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	var payload bytes.Buffer
	if size, err := held.WriteToContext(ctx, &payload); err != nil || size != artifact.Size() || !bytes.Equal(payload.Bytes(), testQCOW2HeaderPayload()) {
		t.Fatalf("strict wrapper changed the original QCOW2 bytes: size=%d error=%v", size, err)
	}
	observed, err := ObserveDiskPublicationPayload(ctx, fixture.plan, expected, artifacts)
	if err != nil || observed.Disk == nil || observed.Disk.PayloadDigest != artifact.Digest() || observed.Disk.Size != artifact.Size() || observed.Disk.EvidenceMode != "" {
		t.Fatalf("strict publication lost exact subject or evidence mode: %+v, %v", observed, err)
	}
	for _, component := range expected.Components {
		raw, err := os.ReadFile(filepath.Join(artifacts["sbom/"+expected.Name].Path, component+".cdx.json"))
		if err != nil {
			t.Fatal(err)
		}
		var bom sbom.BOM
		if err := canonical.UnmarshalExact(bytes.TrimSuffix(raw, []byte("\n")), &bom); err != nil {
			t.Fatal(err)
		}
		result, err := readProducerResult(ctx, artifacts["result/"+expected.Name+"/"+component].Path)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateReconciledDiskDocument(fixture.plan, expected.Name, component, result.SHA256, &bom); err != nil {
			t.Fatalf("strict evidence slot %s: %v", component, err)
		}
	}
	for _, id := range []string{wrapped.Producer, artifacts["sbom/"+expected.Name].Producer} {
		count := 0
		for _, result := range owner.Nodes {
			if result.ID == id && result.Status == "ok" {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("strict completion recorded %d successful outcomes for %s", count, id)
		}
	}
	assertFactoryErrorCode(t, CompleteHostedDiskPublication(ctx, fixture.plan, expected, owner, inspection), e.CodeImmutableViolation)
}

// A real admitted inspection owner is required to reach the consumption guard;
// an inert runtime would fail earlier and not cover private-volume enforcement.
func TestDiskEvidencePublicRuntimeRejectsMemoryProfile(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	fixture := newPublicDiskEvidenceFixture(t, ctx, "memory profile")
	profile := fixture.profile
	selection := pipeline.DiskInspectionSelection{RuntimeExecutable: fixture.runtime, Workspace: fixture.workspace,
		Image: profile.Image, Platform: "linux/" + runtime.GOARCH, InspectorDigest: profile.InspectorDigest, CodecDigest: profile.CodecDigest,
		GuestfishDigest: profile.GuestfishDigest, ScannerDigest: profile.ScannerDigest, PackageReaderDigest: profile.PackageReaderDigest,
		Resources: profile.Resources, TimeoutSeconds: profile.TimeoutSeconds, MaxFilesystemBytes: profile.MaxFilesystemBytes, ScratchStorage: profile.ScratchStorage}
	inspection, err := sbom.OpenDiskInspectionRuntime(ctx, selection)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = inspection.Close() }()
	_, err = diskEvidenceProfile(ctx, fixture.plan, inspection)
	assertFactoryErrorCode(t, err, e.CodeEngineCapability)
	if !strings.Contains(err.Error(), "private disk-backed scratch") {
		t.Fatalf("wrong consumption rejection: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.root, "scan-started")); !os.IsNotExist(err) {
		t.Fatal("memory consumption guard reached subject scan")
	}
}

type publicDiskEvidenceFixture struct {
	root, runtime string
	workspace     *os.File
	plan          *Plan
	node          Node
	profile       definition.DiskInspectionProfile
}

func newPublicDiskEvidenceFixture(t *testing.T, ctx context.Context, mode string) publicDiskEvidenceFixture {
	t.Helper()
	root := t.TempDir()
	// Retained diagnostics are deliberately sealed read-only by their owners.
	// Run after those later-registered owner cleanups, before TempDir deletion.
	t.Cleanup(func() {
		if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return os.Chmod(path, 0o700)
			}
			return nil
		}); err != nil {
			t.Errorf("restoring fixture directory permissions for cleanup: %v", err)
		}
	})
	t.Chdir(root)
	for name, raw := range map[string][]byte{"node.qcow2": testQCOW2HeaderPayload(), "agent.deb": []byte("controlled staged package"), "installer.iso": []byte("controlled installer"), "mode": []byte(mode)} {
		if err := os.WriteFile(filepath.Join(root, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	arch := runtime.GOARCH
	captureArch := arch
	if mode == "wrong capture architecture" {
		if arch == "amd64" {
			captureArch = "arm64"
		} else {
			captureArch = "amd64"
		}
	}
	packageBytes := []byte("controlled capture package bytes, not real Debian")
	packageDigest := pipeline.FileDigest(packageBytes)
	manifest := material.AcquisitionManifest{SchemaVersion: material.AcquisitionSchemaVersion, Scope: "apt-pre-install-pkgs", Architecture: captureArch,
		Packages: []material.AcquiredPackage{{Name: "captured", Version: "1", Architecture: captureArch, Digest: packageDigest, Size: int64(len(packageBytes)), Path: "packages/" + strings.TrimPrefix(packageDigest, "sha256:") + ".deb", RepositoryURIs: []string{"https://example.invalid/captured.deb"}}}}
	manifestBytes, err := canonical.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var capture bytes.Buffer
	w := tar.NewWriter(&capture)
	for _, entry := range []struct {
		name string
		raw  []byte
	}{{"manifest.json", manifestBytes}, {manifest.Packages[0].Path, packageBytes}} {
		if err := w.WriteHeader(&tar.Header{Name: entry.name, Typeflag: tar.TypeReg, Mode: 0444, Size: int64(len(entry.raw)), Format: tar.FormatUSTAR}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(entry.raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "capture.tar"), capture.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "runtime.go")
	if err := os.WriteFile(source, []byte(publicDiskEvidenceRuntimeSource), 0600); err != nil {
		t.Fatal(err)
	}
	runtimePath := filepath.Join(root, "docker")
	command := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags", "-X main.fixtureRoot="+root, "-o", runtimePath, source)
	command.Env = append(os.Environ(), "CGO_ENABLED=0")
	if raw, err := command.CombinedOutput(); err != nil {
		t.Fatalf("building controlled native ELF: %v %s", err, raw)
	}
	runtimeBytes, err := os.ReadFile(runtimePath)
	if err != nil {
		t.Fatal(err)
	}
	pin := pipeline.FileDigest(runtimeBytes)
	profile := definition.DiskInspectionProfile{Image: "example.test/inspection@" + pin, InspectorDigest: pin, CodecDigest: pin, GuestfishDigest: pin, ScannerDigest: pin, PackageReaderDigest: pin,
		Resources: execution.ResourceRequest{CPUMillis: 1000, MemoryMiB: 9000, EphemeralStorageMiB: 4096}, TimeoutSeconds: 30, MaxFilesystemBytes: 1 << 20, ScratchStorage: "private-volume"}
	if mode == "memory profile" {
		profile.ScratchStorage, profile.Resources.MemoryMiB = "memory", 2048
	}
	if err := os.WriteFile(filepath.Join(root, "pin"), []byte(pin), 0600); err != nil {
		t.Fatal(err)
	}
	declared, environment, stack := vmFactoryInputs(t, "linux/"+arch, execution.CapabilityForbidden)
	environment.Runtime.Runtime = "docker"
	environment.DiskInspectionProfiles = map[string]definition.DiskInspectionProfile{"linux/" + arch: profile}
	image := stack.Catalog.Components["image"]
	image.BuildCommand = nil
	image.Packer = &catalog.PackerConfig{ProjectPath: "os-image/packer", PackerVersion: "1.14.2", BuildName: "image.qemu.node", ArchitectureVariable: "architecture", AcceleratorVariable: "accelerator", MaterialManifestVariable: "material_manifest", OutputDirectoryVariable: "output_directory", AcquisitionArchive: "capture.tar"}
	image.Materials = map[string]string{"installer-iso": "runtime", "image-inputs": "agent"}
	image.BuildRequirements = execution.Clone(image.BuildRequirements)
	// This controlled executable is compiled for the actual native host only;
	// do not inherit the two-host routes of the general Plan fixture.
	image.Platforms = []string{"linux/" + arch}
	image.BuildRequirements.AllowedHostPlatforms = []execution.HostPlatform{execution.HostPlatform("linux/" + arch)}
	image.BuildRequirements.Resources = &execution.ResourceRequest{CPUMillis: 1000, MemoryMiB: 512, EphemeralStorageMiB: 128}
	image.BuildRequirements.TimeoutSeconds = 60
	image.ToolchainImages = map[string]string{arch: "example.test/packer@" + pin}
	// Declare only this test's minimum TCG route, not the general fixture's
	// fictional 16 GiB KVM grant. LocalVM admission independently observes and
	// authenticates actual host capacity; inspection still requires real 9 GiB.
	for _, class := range environment.ExecutionClasses {
		if class.HostPlatform != execution.HostPlatform("linux/"+arch) {
			continue
		}
		class.Capabilities = nil
		class.Resources = execution.ResourceCapacity{CPUMillis: 1000, MemoryMiB: 512, EphemeralStorageMiB: 128}
		environment.ExecutionClasses = []execution.Class{class}
		break
	}
	// The fixture checkout is genuinely measured and then authenticated by
	// pipeline's normal fetch/materialization owner, never a receipt literal.
	checkout := filepath.Join(root, "source")
	if err := os.MkdirAll(filepath.Join(checkout, "os-image/packer"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "os-image/packer/node.pkr.hcl"), []byte("packer {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", checkout}, args...)...)
		raw, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("controlled source git %v: %v %s", args, err, raw)
		}
		return strings.TrimSpace(string(raw))
	}
	git("init", "-q")
	git("add", ".")
	git("-c", "commit.gpgsign=false", "-c", "user.name=NKD Test", "-c", "user.email=nkd@example.test", "commit", "-qm", "controlled source")
	image.Commit, image.TreeHash = git("rev-parse", "HEAD"), git("rev-parse", "HEAD^{tree}")
	stack.Catalog.Components["image"] = image
	for _, entry := range []struct{ component, file, kind string }{{"runtime", "installer.iso", "raw"}, {"agent", "agent.deb", "host-package"}} {
		raw, err := os.ReadFile(filepath.Join(root, entry.file))
		if err != nil {
			t.Fatal(err)
		}
		stack.Catalog.Components[entry.component] = catalog.Component{Platforms: []string{"linux/" + arch}, OutputType: entry.kind, Tag: "v1.0.0", SourceDate: 1,
			Prebuilt: &catalog.Prebuilt{URL: "https://example.test/" + entry.file, Digests: map[string]string{arch: pipeline.FileDigest(raw)}, Evidence: &catalog.ExternalEvidence{None: true}}}
	}
	declared.Outputs[0].Components = []string{"agent", "image", "runtime"}
	if mode == "hosted publication" {
		environment.Publication = &definition.Publication{RepositoryPrefix: "example.test/nkd", Anonymous: true}
		environment.Signing = cosignSigningProfile()
		declared.CosignTool = "cosign"
		stack.Catalog.Tools = map[string]catalog.ToolPin{"cosign": {
			Format: ToolFormatTarGzip, URL: "https://example.test/cosign.tar.gz", Platform: environment.UtilityPlatform,
			ExecutablePath: "cosign", ArchiveDigest: testCosignArchiveDigest, ExecutableDigest: testCosignExecutableDigest,
		}}
	}
	stack, err = catalog.ApplyStack(stack.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Compile(ctx, declared, environment, stack, "controlled-test")
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	_, artifacts := planIndex(plan)
	var node Node
	for _, selected := range plan.Graph.Nodes {
		if selected.IRNode == "build-image" {
			node = selected
		}
	}
	if node.ID == "" {
		t.Fatal("no compiled root")
	}
	for _, selected := range plan.Builds[0].Graph.Nodes {
		if selected.Kind == ir.KindFetch {
			artifact := artifacts["source/node-image/"+selected.ID]
			if err := os.MkdirAll(filepath.Dir(artifact.Path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(checkout, artifact.Path); err != nil {
				t.Fatal(err)
			}
		}
	}
	return publicDiskEvidenceFixture{root: root, runtime: runtimePath, workspace: workspace, profile: profile, plan: plan, node: node}
}

func (fixture publicDiskEvidenceFixture) produce(t *testing.T, ctx context.Context) (*LocalRunResult, *pipeline.PackerArtifact) {
	t.Helper()
	owner, artifact, _ := fixture.produceWithDiagnostics(t, ctx)
	return owner, artifact
}

func (fixture publicDiskEvidenceFixture) produceWithDiagnostics(t *testing.T, ctx context.Context) (*LocalRunResult, *pipeline.PackerArtifact, *pipeline.PackerDiagnosticSink) {
	t.Helper()
	owner := localDiskRunResult(t, fixture.plan)
	t.Cleanup(func() { _ = owner.Close() })
	session, err := pipeline.NewNodeSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.Close)
	_, artifacts := planIndex(fixture.plan)
	for _, instruction := range fixture.plan.Builds[0].Graph.Nodes {
		if instruction.Kind == ir.KindFetch {
			artifact := artifacts["source/node-image/"+instruction.ID]
			sourceRoot := strings.TrimSuffix(artifact.Path, "/"+instruction.Dest)
			if _, err := session.ExecNode(instruction, pipeline.RunOptions{Ctx: ctx, Launch: true, SourceRoot: sourceRoot, Stdout: io.Discard, Stderr: io.Discard}); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, component := range []string{"runtime", "agent"} {
		var operation Node
		for _, node := range fixture.plan.Graph.Nodes {
			if node.BuildTarget == "node-image" && node.IRNode == "pull-"+component {
				operation = node
			}
		}
		_, instruction, err := fixture.plan.producerInstruction(operation)
		if err != nil {
			t.Fatal(err)
		}
		filename := "agent.deb"
		if component == "runtime" {
			filename = "installer.iso"
		}
		raw, err := os.ReadFile(filepath.Join(fixture.root, filename))
		if err != nil {
			t.Fatal(err)
		}
		payload := artifacts["artifact/node-image/"+component]
		if err := os.MkdirAll(filepath.Dir(payload.Path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(payload.Path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		record := &pipeline.ArtifactResult{Component: component, OutputType: instruction.OutputType, SHA256: pipeline.FileDigest(raw), Size: int64(len(raw)), AbsPath: payload.Path, Qualification: pipeline.ArtifactProfileFreeAcquisition, EngineClass: ir.EnginePrebuiltAcquire}
		if err := executeProducer(ctx, fixture.plan, operation, "docker", 1, io.Discard, io.Discard, func(ir.Node, pipeline.RunOptions) (*pipeline.ArtifactResult, error) { return record, nil }, owner); err != nil {
			t.Fatal(err)
		}
	}
	diagnostics, err := pipeline.NewPackerDiagnosticSink(ctx, fixture.workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = diagnostics.Close() })
	executor, err := (localVMPackerResolver{}).Admit(ctx, fixture.plan, session, vmDriverRunConfig{Runtime: fixture.runtime, Diagnostics: diagnostics, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = executor.Close() })
	admission, err := AdmitLocalVMBuild(ctx, fixture.plan, fixture.node.ID, fixture.workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admission.Close() })
	var original *pipeline.PackerArtifact
	if err := populateDiskProducerCompletion(ctx, fixture.plan, fixture.node.ID, owner, func(ctx context.Context, node Node, _ ir.Node) (*pipeline.PackerArtifact, *pipeline.ArtifactResult, error) {
		artifact, result, err := dispatchAdmittedVMProducer(ctx, admission, fixture.plan, node, executor, owner)
		original = artifact
		return artifact, result, err
	}); err != nil {
		t.Fatal(err)
	}
	if original == nil {
		t.Fatal("real public executor did not issue original artifact")
	}
	return owner, original, diagnostics
}

const publicDiskEvidenceRuntimeSource = `package main
import("archive/tar";"crypto/sha256";"encoding/json";"fmt";"io";"os";"path/filepath";"runtime";"strconv";"strings";"syscall";"time")
var fixtureRoot string
func fail(err error){ if err!=nil {fmt.Fprintln(os.Stderr,err);os.Exit(90)} }
func read(name string) []byte {raw,err:=os.ReadFile(filepath.Join(fixtureRoot,name));fail(err);return raw}
func value(args []string,key string) string {for i,arg:=range args {if arg==key&&i+1<len(args){return args[i+1]};if strings.HasPrefix(arg,key+"="){return strings.TrimPrefix(arg,key+"=")}};return ""}
func id(name string) string{return fmt.Sprintf("%x",sha256.Sum256([]byte(name)))}
func put(name string,v any){raw,err:=json.Marshal(v);fail(err);fail(os.WriteFile(filepath.Join(fixtureRoot,name),raw,0600))}
func container(name string) []string {var args []string;fail(json.Unmarshal(read("container-"+name),&args));return args}
func carrier(name string,raw []byte){w:=tar.NewWriter(os.Stdout);fail(w.WriteHeader(&tar.Header{Name:name,Mode:0600,Typeflag:tar.TypeReg,Format:tar.FormatUSTAR,Size:int64(len(raw))}));_,err:=w.Write(raw);fail(err);fail(w.Close())}
func main(){a:=os.Args[1:];if len(a)==0{os.Exit(96)};pin:=string(read("pin"));mode:=string(read("mode"))
switch a[0]{
case "version":fmt.Print("{\"Client\":{\"Version\":\"fixture\"},\"Server\":{\"Version\":\"fixture\"}}")
case "info":fmt.Print("\"controlled-original-daemon\"")
case "image":ref:=a[len(a)-1];if strings.Contains(ref,"--format"){os.Exit(96)};fail(json.NewEncoder(os.Stdout).Encode(map[string]any{"Id":pin,"RepoDigests":[]string{ref},"Os":"linux","Architecture":runtime.GOARCH,"Config":map[string]any{"Env":[]string{}}}))
case "volume":switch a[1]{case "create":name:=a[len(a)-1];labels:=map[string]string{};for i,arg:=range a {if arg=="--label"&&i+1<len(a){k,v,_:=strings.Cut(a[i+1],"=");labels[k]=v}};put("volume-"+name,map[string]any{"Name":name,"Driver":"local","Options":map[string]string{},"Labels":labels});fmt.Println(name)
case "inspect":_,err:=os.Stdout.Write(read("volume-"+a[len(a)-1]));fail(err)
case "rm":fail(os.Remove(filepath.Join(fixtureRoot,"volume-"+a[len(a)-1])))
default:os.Exit(96)}
case "create":name:=value(a,"--name");put("container-"+name,a);put("container-"+id(name),a);fmt.Print(id(name))
case "cp":if a[1]=="-"{_,err:=io.Copy(io.Discard,os.Stdin);fail(err);break};source:=a[1];switch {case strings.Contains(source,"capture.tar"):carrier("capture.tar",read("capture.tar"));case strings.Contains(source,".qcow2"):carrier(filepath.Base(source),read("node.qcow2"));default:carrier("serial.log",[]byte("controlled serial diagnostic"))}
case "start":args:=container(a[len(a)-1]);name:=value(args,"--name");switch {
case strings.HasSuffix(name,"-scratch-prepare"):var facts syscall.Statfs_t;fail(syscall.Statfs(fixtureRoot,&facts));if facts.Type==0x01021994 {fmt.Fprintln(os.Stderr,"fixture scratch is tmpfs");os.Exit(90)};fmt.Printf("%d %d\n",facts.Bavail,facts.Bsize)
case value(args,"--entrypoint")=="/usr/local/bin/nkd-disk-inspect":raw,err:=io.ReadAll(os.Stdin);fail(err);fail(os.WriteFile(filepath.Join(fixtureRoot,"scan-started"),nil,0600));if mode=="cancel during root scan"{time.Sleep(60*time.Second)}
var at int;for i,arg:=range args{if arg=="--entrypoint=/usr/local/bin/nkd-disk-inspect"{at=i+2}};if at==0{os.Exit(96)};p:=args[at:];if len(p)<11{os.Exit(96)};kind:=p[5];size,err:=strconv.ParseInt(p[3],10,64);fail(err);digest:=fmt.Sprintf("sha256:%x",sha256.Sum256(raw));if digest!=p[2]||size!=int64(len(raw)){os.Exit(91)};if mode=="substituted report subject"{digest="sha256:"+strings.Repeat("f",64)};max,err:=strconv.ParseUint(p[9],10,64);fail(err)
props:=[]map[string]string{{"name":"nkd:diskContentClass","value":"filesystem"},{"name":"nkd:packageArchitecture","value":runtime.GOARCH},{"name":"nkd:packageFormat","value":"deb"}};pkg:=map[string]any{"type":"library","name":"captured","version":"1","purl":"pkg:deb/fixture/captured@1?arch="+runtime.GOARCH,"properties":props};components:=[]any{};if kind=="disk-image"||kind=="installer-iso"||kind=="apt-acquisition"{if kind=="apt-acquisition"{props[0]["value"]="debian-archive"};if mode=="missing installed correspondence"&&kind=="disk-image"{pkg["version"]="2";pkg["purl"]="pkg:deb/fixture/captured@2?arch="+runtime.GOARCH};components=append(components,pkg)}
files:=[]map[string]any{{"path":"controlled-file","kind":"regular","size":1,"digest":fmt.Sprintf("sha256:%x",sha256.Sum256([]byte("x")))}};fail(json.NewEncoder(os.Stdout).Encode(map[string]any{"subject":digest,"size":size,"kind":kind,"platform":p[10],"inspector":p[0],"codec":p[1],"guestfish":p[6],"scanner":p[7],"packageReader":p[8],"maxFilesystemBytes":max,"components":components,"files":files}))
case strings.Contains(name,"-version"):fmt.Print("Packer v1.14.2\n")
case strings.Contains(name,"-validate"):fmt.Print("The configuration is valid.\n")
default:_,err:=io.Copy(io.Discard,os.Stdin);fail(err)}
case "ps":filter:=value(a,"--filter");for _,entry:=range mustEntries(){if !strings.HasPrefix(entry.Name(),"container-")||len(strings.TrimPrefix(entry.Name(),"container-"))==64{continue};args:=container(strings.TrimPrefix(entry.Name(),"container-"));for _,arg:=range args {if arg=="--"+filter||arg==strings.TrimPrefix(filter,"label="){fmt.Println(id(value(args,"--name")));break}}}
case "rm":for _,target:=range a[1:] {if strings.HasPrefix(target,"-"){continue};args:=container(target);name:=value(args,"--name");fail(os.Remove(filepath.Join(fixtureRoot,"container-"+name)));fail(os.Remove(filepath.Join(fixtureRoot,"container-"+id(name))))}
default:os.Exit(96)}}
func mustEntries() []os.DirEntry {entries,err:=os.ReadDir(fixtureRoot);fail(err);return entries}
`
