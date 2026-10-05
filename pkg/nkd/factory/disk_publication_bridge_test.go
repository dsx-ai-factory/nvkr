package factory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/buildgraph"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	authored "github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

func diskPublicationPlanFixture(t *testing.T, platform string) *Plan {
	return diskPublicationPlanFixtureForHost(t, platform, platform)
}

func diskPublicationPlanFixtureForHost(t *testing.T, platform, host string) *Plan {
	t.Helper()
	fixture := newVMPackerMaterialFixtureForPlatform(t, nil,
		map[string]string{"agent": "agent", "runtime": "runtime"}, platform)
	definition, environment := fixture.plan.Definition.Clone(), fixture.plan.Environment.Clone()
	for _, class := range environment.ExecutionClasses {
		if string(class.HostPlatform) == host {
			environment.ExecutionClasses = []execution.Class{class}
			break
		}
	}
	environment.Runtime.Runtime = "podman"
	environment.Publication = &authored.Publication{RepositoryPrefix: "example.test/nkd", Anonymous: true}
	environment.Signing = cosignSigningProfile()
	definition.CosignTool = "cosign"
	environment.DiskInspectionProfiles = map[string]authored.DiskInspectionProfile{host: {
		Image:           "example.test/inspection@sha256:" + strings.Repeat("1", 64),
		InspectorDigest: "sha256:" + strings.Repeat("2", 64), CodecDigest: "sha256:" + strings.Repeat("3", 64),
		GuestfishDigest: "sha256:" + strings.Repeat("4", 64), ScannerDigest: "sha256:" + strings.Repeat("5", 64),
		PackageReaderDigest: "sha256:" + strings.Repeat("6", 64),
		Resources:           execution.ResourceRequest{CPUMillis: 1000, MemoryMiB: 128, EphemeralStorageMiB: 2048},
		TimeoutSeconds:      60, MaxFilesystemBytes: 1 << 20, ScratchStorage: "private-volume",
	}}
	base := fixture.plan.CatalogSnapshot
	root := base.Components["image"]
	packer := *root.Packer
	packer.AcquisitionArchive = "test-acquisitions.tar"
	root.Packer = &packer
	base.Components["image"] = root
	base.Tools = map[string]catalog.ToolPin{"cosign": {
		Format: ToolFormatTarGzip, URL: "https://example.test/cosign.tar.gz", Platform: environment.UtilityPlatform,
		ExecutablePath: "cosign", ArchiveDigest: testCosignArchiveDigest, ExecutableDigest: testCosignExecutableDigest,
	}}
	stack, err := catalog.ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Compile(t.Context(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestDiskPublicationSelectsFrozenExecutionHostProfile(t *testing.T) {
	for _, pair := range [][2]string{{"linux/arm64", "linux/amd64"}, {"linux/amd64", "linux/arm64"}} {
		t.Run(pair[0]+" on "+pair[1], func(t *testing.T) {
			plan := diskPublicationPlanFixtureForHost(t, pair[0], pair[1])
			expected := plan.LifecycleOutputs[0]
			nodes, artifacts := planIndex(plan)
			root, err := plan.ExecutionFor(nodes[artifacts[expected.ArtifactID].Producer])
			if err != nil || root.ExecutionBinding == nil || string(root.ExecutionBinding.Class.HostPlatform) != pair[1] {
				t.Fatalf("root execution host = %+v, %v", root, err)
			}
			profile := plan.Environment.DiskInspectionProfiles[pair[1]]
			if evidence := nodes[expected.Name+"/sbom"]; evidence.Platform != pair[1] || evidence.Container != profile.Image {
				t.Fatalf("inspection used guest instead of frozen host: %+v", evidence)
			}
			guest, selected, err := reconciledDiskPackageBinding(plan, expected, artifacts["package/"+expected.Name])
			if err != nil || guest != pair[0] || selected != profile {
				t.Fatalf("wrapper guest/profile = %s, %+v, %v", guest, selected, err)
			}
			// Persisted evidence uses native host tooling but retains the guest
			// platform in its catalog-derived source subject. No receipt is minted.
			component := artifacts[expected.ArtifactID].Component
			pin := pipeline.FileDigest(testQCOW2HeaderPayload())
			var sources []catalog.NamedComponent
			for _, source := range plan.Builds[0].ResolvedComponents {
				if source.Name == component {
					sources = append(sources, source)
				}
			}
			documents, _, err := sbom.PerArtifact(&buildgraph.Resolved{Platform: guest,
				Components: sources}, expected.BuildDigest, map[string]string{component: pin})
			if err != nil {
				t.Fatal(err)
			}
			bom, err := sbom.ForSubject(documents[component], pin, "build-output", pin)
			if err != nil {
				t.Fatal(err)
			}
			bom, err = sbom.WithDependencies(bom, []sbom.Component{{Type: "library", Name: "controlled-package", Version: "1"}}, "disk-reconciled/v1", profile.ScannerDigest)
			if err != nil {
				t.Fatal(err)
			}
			for key, value := range map[string]string{
				"nkd:diskInspectionRuntimeDigest": pin, "nkd:diskInspectorDigest": profile.InspectorDigest, "nkd:diskCodecDigest": profile.CodecDigest,
			} {
				bom.Metadata.Props = append(bom.Metadata.Props, sbom.Property{Name: key, Value: value})
			}
			sort.Slice(bom.Metadata.Props, func(i, j int) bool { return bom.Metadata.Props[i].Name < bom.Metadata.Props[j].Name })
			if err := validateReconciledDiskDocument(plan, expected.Name, component, pin, bom); err != nil {
				t.Fatalf("native host evidence rejected: %v", err)
			}
			for index := range bom.Metadata.Props {
				if bom.Metadata.Props[index].Name == "nkd:dependencyScannerDigest" {
					bom.Metadata.Props[index].Value = "sha256:" + strings.Repeat("9", 64)
				}
			}
			assertFactoryErrorCode(t, validateReconciledDiskDocument(plan, expected.Name, component, pin, bom), e.CodeVerifyFailed)
		})
	}
}

func TestCompileProductionDiskPreservesRawRootAndAllEvidence(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		t.Run(platform, func(t *testing.T) {
			plan := diskPublicationPlanFixture(t, platform)
			if plan.Definition.SBOMTool != "" {
				t.Fatal("fixture unexpectedly selected an ordinary scanner")
			}
			expected := plan.LifecycleOutputs[0]
			nodes, artifacts := planIndex(plan)
			if expected.ArtifactID != "artifact/node-image/image" || artifacts[expected.ArtifactID].BuildTarget != expected.Name {
				t.Fatal("production compilation substituted the raw lifecycle subject")
			}
			wrapper := artifacts["package/"+expected.Name]
			if wrapper.Kind != PackageDiskImage || wrapper.MediaType != "application/vnd.nkd.disk-image.v1" || wrapper.BuildTarget != "" {
				t.Fatalf("wrapper = %+v", wrapper)
			}
			evidence := nodes[expected.Name+"/sbom"]
			if evidence.Kind != NodeSBOM || evidence.Platform != platform || evidence.Container != plan.Environment.DiskInspectionProfiles[platform].Image {
				t.Fatalf("evidence operation = %+v", evidence)
			}
			for _, component := range expected.Components {
				for _, id := range []string{"artifact/" + expected.Name + "/" + component, "result/" + expected.Name + "/" + component} {
					if !containsString(evidence.Inputs, id) || !containsString(nodes[wrapper.Producer].Inputs, id) {
						t.Fatalf("disk evidence or wrapper omits %s", id)
					}
				}
			}
			if !containsString(nodes["package/release"].Inputs, wrapper.ID) || !containsString(nodes["package/release"].Inputs, "sbom/"+expected.Name) {
				t.Fatal("publication omits disk wrapper or component evidence")
			}
			if err := plan.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestObserveProductionDiskKeepsManifestAndRawDigestsSeparate(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		for _, condition := range []string{"valid", "manifest substituted for raw", "wrong size", "wrong wrapper platform", "canceled"} {
			t.Run(platform+"/"+condition, func(t *testing.T) {
				plan := diskPublicationPlanFixture(t, platform)
				expected := plan.LifecycleOutputs[0]
				_, artifacts := planIndex(plan)
				raw := testQCOW2HeaderPayload()
				path := filepath.Join(t.TempDir(), "disk.qcow2")
				if err := os.WriteFile(path, raw, 0o600); err != nil {
					t.Fatal(err)
				}
				wrapper := artifacts["package/"+expected.Name]
				wrapperPlatform := platform
				if condition == "wrong wrapper platform" {
					wrapperPlatform = "linux/amd64"
					if platform == wrapperPlatform {
						wrapperPlatform = "linux/arm64"
					}
				}
				_, err := delivery.BuildDiskArtifact(t.Context(), wrapper.Path, delivery.DiskArtifactRequest{
					Path: path, Digest: pipeline.FileDigest(raw), Size: int64(len(raw)), Platform: wrapperPlatform,
					Format: "qcow2", Encryption: "none", ArtifactType: wrapper.MediaType,
				})
				if err != nil {
					t.Fatal(err)
				}
				descriptor, err := delivery.PayloadDescriptor(t.Context(), wrapper.Path)
				if err != nil {
					t.Fatal(err)
				}
				record := artifacts["result/"+expected.Name+"/image"]
				observed := pipeline.ArtifactResult{Component: "image", OutputType: PackageDiskImage,
					SHA256: pipeline.FileDigest(raw), Size: int64(len(raw))}
				if condition == "manifest substituted for raw" {
					observed.SHA256 = descriptor.Digest
				}
				if condition == "wrong size" {
					observed.Size++
				}
				encoded, err := canonical.Marshal(observed)
				if err != nil {
					t.Fatal(err)
				}
				if err := writeFactoryFile(t.Context(), record.Path, encoded, 0o600); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if condition == "canceled" {
					cancel()
				}
				result, err := ObserveDiskPublicationPayload(ctx, plan, expected, artifacts)
				if condition != "valid" {
					if err == nil || !reflect.DeepEqual(result, delivery.Output{}) {
						t.Fatalf("invalid projection = %+v, %v", result, err)
					}
					if condition == "canceled" && (!errors.Is(err, context.Canceled) || factoryErrorCode(err) != e.CodeTimeout) {
						t.Fatalf("cancellation = %v", err)
					}
					return
				}
				if err != nil || result.Digest != descriptor.Digest || result.Disk == nil || result.Disk.PayloadDigest != observed.SHA256 || result.Digest == result.Disk.PayloadDigest {
					t.Fatalf("projection = %+v, %v", result, err)
				}
			})
		}
	}
}

func TestPackageReconciledDiskRequiresLiveEvidenceBeforeOutput(t *testing.T) {
	for _, condition := range []string{"nil context", "canceled", "missing owner", "missing runtime", "missing receipt"} {
		t.Run(condition, func(t *testing.T) {
			ctx := t.Context()
			owner, inspection := &LocalRunResult{}, &sbom.DiskInspectionRuntime{}
			var receipt *DiskEvidenceReceipt
			want := e.CodeProvenanceMissing
			switch condition {
			case "nil context":
				ctx, want = nil, e.CodeUsage
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = e.CodeTimeout
			case "missing owner":
				owner = nil
			case "missing runtime":
				inspection = nil
			}
			path := filepath.Join(t.TempDir(), "never-created")
			result, document, err := owner.PackageReconciledDisk(ctx, &Plan{}, LifecycleOutputPlan{}, inspection, receipt, Artifact{Path: path})
			if factoryErrorCode(err) != want || document != nil || !reflect.DeepEqual(result, delivery.Result{}) {
				t.Fatalf("refusal = %+v, %v, %v", result, document, err)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("missing live authority created output: %v", err)
			}
		})
	}
}

func TestHostedDiskCompletionRequiresOriginalLiveOwners(t *testing.T) {
	for _, condition := range []string{"nil context", "nil plan", "missing owner", "missing runtime", "canceled", "not pipeline-attested"} {
		t.Run(condition, func(t *testing.T) {
			ctx := t.Context()
			plan, owner, inspection := &Plan{}, &LocalRunResult{}, &sbom.DiskInspectionRuntime{}
			want := e.CodeProvenanceMissing
			switch condition {
			case "nil context":
				ctx, want = nil, e.CodeUsage
			case "nil plan":
				plan = nil
			case "missing owner":
				owner = nil
			case "missing runtime":
				inspection = nil
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = e.CodeTimeout
			case "not pipeline-attested":
				want = e.CodeEngineCapability
			}
			err := CompleteHostedDiskPublication(ctx, plan, LifecycleOutputPlan{}, owner, inspection)
			assertFactoryErrorCode(t, err, want)
			if owner != nil && len(owner.Nodes) != 0 {
				t.Fatal("failed completion recorded successful node outcomes")
			}
		})
	}
}
