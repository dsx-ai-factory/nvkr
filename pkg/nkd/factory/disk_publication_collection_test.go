package factory

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

// This exercises the factory-to-delivery transport join, not production
// reconciliation, signatures or release acceptance. Its evidence is synthetic.
func TestDiskPublicationCollectionPinsLayoutNotPayload(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		t.Run(platform, func(t *testing.T) {
			plan := diskPublicationPlanFixture(t, platform)
			expected := plan.LifecycleOutputs[0]
			_, artifacts := planIndex(plan)
			wrapper := artifacts["package/"+expected.Name]
			payload := testQCOW2HeaderPayload()
			payloadPin := pipeline.FileDigest(payload)
			path := filepath.Join(t.TempDir(), "disk.qcow2")
			if err := os.WriteFile(path, payload, 0o600); err != nil {
				t.Fatal(err)
			}
			built, err := delivery.BuildDiskArtifact(t.Context(), wrapper.Path, delivery.DiskArtifactRequest{
				Path: path, Digest: payloadPin, Size: int64(len(payload)), Platform: platform,
				Format: "qcow2", Encryption: "none", ArtifactType: wrapper.MediaType,
			})
			if err != nil {
				t.Fatal(err)
			}
			descriptor, err := delivery.PayloadDescriptor(t.Context(), wrapper.Path)
			if err != nil {
				t.Fatal(err)
			}
			objects, cleanup, err := collectPublicationObjects(t.Context(), plan, artifacts["package/release"], []Artifact{wrapper}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			if len(objects) != 1 || objects[0].BlobDigest != built.RootDigest || objects[0].BlobDigest == payloadPin ||
				objects[0].SemanticDigest != descriptor.Digest {
				t.Fatalf("collection confused layout, manifest and payload identities: %+v", objects)
			}
			// Disk transport requires explicit metadata and subject evidence even
			// in a diagnostic root. Use one synthetic root-component slot only.
			component := artifacts[expected.ArtifactID].Component
			disk := &delivery.DiskOutput{PayloadDigest: payloadPin, Producer: "fixture", Component: component,
				Format: "qcow2", Encryption: "none", Size: int64(len(payload)), PlanDigest: expected.BuildDigest, Outcome: "succeeded"}
			objects[0].Output = &delivery.OutputBinding{Repository: expected.Name, Format: PackageDiskImage,
				BuildDigest: expected.BuildDigest, Components: []string{component}, Disk: disk,
				BuildSubjects: []delivery.BuildSubject{{Component: component, Digest: payloadPin}}}
			bom, err := sbom.NewSubject(expected.BuildDigest, component, payloadPin)
			if err != nil {
				t.Fatal(err)
			}
			bom, err = sbom.ForSubject(bom, payloadPin, "build-output", payloadPin)
			if err != nil {
				t.Fatal(err)
			}
			dependency := "pkg:generic/collection-test@1"
			bom.Metadata.Component.BOMRef = "artifact:" + strings.TrimPrefix(payloadPin, "sha256:")
			bom.Components = []sbom.Component{{Type: "library", Name: "collection-test", Version: "1", PURL: dependency, BOMRef: dependency}}
			bom.Dependencies = []sbom.Dependency{{Ref: bom.Metadata.Component.BOMRef, DependsOn: []string{dependency}}}
			bom.Metadata.Component.Type = "operating-system"
			bom.Metadata.Props = append(bom.Metadata.Props, sbom.Property{Name: "nkd:dependencyInspection", Value: "disk-reconciled/v1"})
			for _, key := range []string{"nkd:dependencyScannerDigest", "nkd:diskInspectionRuntimeDigest", "nkd:diskInspectorDigest", "nkd:diskCodecDigest"} {
				bom.Metadata.Props = append(bom.Metadata.Props, sbom.Property{Name: key, Value: expected.BuildDigest})
			}
			sort.Slice(bom.Metadata.Props, func(i, j int) bool { return bom.Metadata.Props[i].Name < bom.Metadata.Props[j].Name })
			document, err := bom.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			semantic, err := bom.Digest()
			if err != nil {
				t.Fatal(err)
			}
			objects = append(objects, delivery.ObjectInput{Name: "sbom-" + strings.TrimPrefix(semantic, "sha256:"), Kind: "sbom",
				ArtifactType: "application/vnd.cyclonedx+json", LayerMediaType: "application/vnd.cyclonedx+json", Bytes: document, SemanticDigest: semantic,
				Evidence: &delivery.EvidenceBinding{Outputs: []string{expected.Name}, Component: component, Stage: "build-output", SubjectKind: "file", SubjectDigest: payloadPin}})
			content, err := delivery.ContentDigest([]delivery.Output{{Name: expected.Name, Kind: PackageDiskImage,
				Digest: descriptor.Digest, BuildDigest: expected.BuildDigest, Components: []string{component}, Disk: disk}})
			if err != nil {
				t.Fatal(err)
			}
			request := delivery.PublicationRequest{Release: &delivery.ReleaseMetadata{DefinitionDigest: expected.BuildDigest,
				EnvironmentDigest: expected.BuildDigest, ContentSubject: content}, Objects: objects}
			publication := filepath.Join(t.TempDir(), "diagnostic")
			if _, err := delivery.BuildPublicationRoot(t.Context(), publication, request); err != nil {
				t.Fatal(err)
			}
			inspection, err := delivery.Inspect(t.Context(), publication)
			if err != nil {
				t.Fatal(err)
			}
			for _, object := range inspection.Root.Objects {
				if object.Output != nil && (object.DescriptorDigest != descriptor.Digest || object.BlobDigest != payloadPin || object.Output.Disk.Size != int64(len(payload))) {
					t.Fatalf("diagnostic transport changed exact disk subjects: %+v", object)
				}
			}
			request.Objects[0].BlobDigest = payloadPin
			invalid := filepath.Join(t.TempDir(), "invalid")
			_, err = delivery.BuildPublicationRoot(t.Context(), invalid, request)
			assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
			if _, err := os.Lstat(invalid); !os.IsNotExist(err) {
				t.Fatalf("wrong layout pin exposed a publication: %v", err)
			}
		})
	}
}
