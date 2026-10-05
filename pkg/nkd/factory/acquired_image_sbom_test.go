package factory

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/buildgraph"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	authored "github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

func acquiredImageSBOMFixture(t *testing.T, arch string, media types.MediaType, noCommand ...bool) (*Plan, Node, Artifact, map[string]Artifact, string) {
	t.Helper()
	t.Chdir(t.TempDir())
	var raw bytes.Buffer
	writer := tar.NewWriter(&raw)
	if err := writer.WriteHeader(&tar.Header{Name: "fixture", Mode: 0o644, Size: 7}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("fixture")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	layerMedia := types.DockerLayer
	if media == types.OCIManifestSchema1 {
		layerMedia = types.OCILayer
	}
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(raw.Bytes())), nil
	}, tarball.WithMediaType(layerMedia))
	if err != nil {
		t.Fatal(err)
	}
	image, err := mutate.AppendLayers(empty.Image, layer)
	if err != nil {
		t.Fatal(err)
	}
	config, err := image.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	config.OS, config.Architecture, config.Config.Cmd = "linux", arch, []string{"fixture"}
	if len(noCommand) != 0 && noCommand[0] {
		config.Config.Cmd = nil
	}
	image, err = mutate.ConfigFile(image, config)
	if err != nil {
		t.Fatal(err)
	}
	image = mutate.MediaType(image, media)
	configMedia := types.DockerConfigJSON
	if media == types.OCIManifestSchema1 {
		configMedia = types.OCIConfigJSON
	}
	image = mutate.ConfigMediaType(image, configMedia)
	digest, err := image.Digest()
	if err != nil {
		t.Fatal(err)
	}
	pin := digest.String()
	toolPin := pipeline.FileDigest([]byte("scanner fixture"))
	base := &catalog.Catalog{Components: map[string]catalog.Component{"image": {
		Platforms: []string{"linux/" + arch}, Tag: "1.0.0", OutputType: "oci-image",
		Prebuilt: &catalog.Prebuilt{Image: "example.test/acquired", Digests: map[string]string{arch: pin}, Evidence: &catalog.ExternalEvidence{None: true}},
	}}, Tools: map[string]catalog.ToolPin{"syft": {Format: "raw", URL: "https://example.test/syft",
		Platform: "linux/" + arch, ExecutablePath: "syft", ArchiveDigest: toolPin, ExecutableDigest: toolPin}}}
	stack, err := catalog.ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	definition := authored.Definition{APIVersion: authored.DefinitionAPIVersion, Kind: authored.DefinitionKind,
		Metadata: authored.Metadata{Name: "acquired-image"}, Catalog: authored.Catalog{Base: "catalog"}, SBOMTool: "syft",
		Outputs: []authored.DefinitionOutput{{Name: "image", Platform: "linux/" + arch, Format: authored.OutputOCI, Components: []string{"image"}}}}
	environment := authored.Environment{APIVersion: authored.EnvironmentAPIVersion, Kind: authored.EnvironmentKind,
		Metadata: authored.Metadata{Name: "local"}, UtilityContainer: fixtureUtilityContainer, UtilityPlatform: "linux/" + arch}
	plan, err := Compile(t.Context(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	nodes, artifacts := planIndex(plan)
	payload := artifacts["artifact/image/image"]
	if err := os.MkdirAll(filepath.Dir(payload.Path), 0o755); err != nil {
		t.Fatal(err)
	}
	directory, err := layout.Write(payload.Path, empty.Index)
	if err != nil {
		t.Fatal(err)
	}
	if err := directory.AppendImage(image); err != nil {
		t.Fatal(err)
	}
	var size int64
	if err := filepath.WalkDir(payload.Path, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			size += info.Size()
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, instruction, err := plan.producerInstruction(nodes[payload.Producer])
	if err != nil {
		t.Fatal(err)
	}
	result := pipeline.ArtifactResult{Component: "image", OutputType: "oci-image", Qualification: pipeline.ArtifactProfileFreeAcquisition,
		EngineClass: "prebuilt-acquire", SHA256: pin, Size: size, Ref: instruction.Artifact + "@" + pin, Packaged: true}
	record := artifacts["result/image/image"]
	encoded, err := canonical.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFactoryFile(t.Context(), record.Path, encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	return plan, nodes["image/sbom"], payload, artifacts, pin
}

func TestAcquiredImageSBOMArchiveRetainsNativeManifestSubject(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		for _, media := range []types.MediaType{types.OCIManifestSchema1, types.DockerManifestSchema2} {
			t.Run(arch+"/"+string(media), func(t *testing.T) {
				plan, node, payload, artifacts, subject := acquiredImageSBOMFixture(t, arch, media)
				archive, err := prepareAcquiredImageSBOMArchive(t.Context(), plan, node, payload, artifacts, subject)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = archive.close() }()
				if archive.digest == subject || !canonical.IsDigest(archive.digest) {
					t.Fatal("scan transport replaced native manifest identity")
				}
				digest, err := digestFactoryPath(t.Context(), archive.path)
				if err != nil || digest != archive.digest {
					t.Fatalf("archive integrity: %s %v", digest, err)
				}
				imported, err := delivery.InspectContainerImage(t.Context(), filepath.Dir(archive.path), "linux/"+arch)
				if err != nil || imported.ManifestDigest != subject || imported.MediaType != string(media) {
					t.Fatalf("native snapshot identity: %+v %v", imported, err)
				}
				boms, index, err := sbom.PerArtifact(&buildgraph.Resolved{Platform: "linux/" + arch, Components: plan.Builds[0].ResolvedComponents},
					plan.Builds[0].BuildDigest, map[string]string{"image": subject})
				if err != nil || index.Entries[0].ArtifactSHA != subject {
					t.Fatalf("acquisition subject: %v", err)
				}
				props := map[string]string{}
				for _, p := range boms["image"].Metadata.Component.Properties {
					props[p.Name] = p.Value
				}
				if props["nkd:fromSource"] != "false" || props["nkd:lane"] != "prebuilt" {
					t.Fatalf("acquisition became source claim: %v", props)
				}
				root := archive.root
				if err := archive.close(); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(root); !os.IsNotExist(err) {
					t.Fatalf("private scan workspace survived: %v", err)
				}
			})
		}
	}
}

func TestAcquiredImageSBOMRejectsPairSubstitutionBeforeSnapshot(t *testing.T) {
	for _, condition := range []string{"payload-path", "result-path", "missing-result", "subject", "profile", "source-metadata", "canceled"} {
		t.Run(condition, func(t *testing.T) {
			plan, node, payload, artifacts, subject := acquiredImageSBOMFixture(t, "amd64", types.OCIManifestSchema1)
			private := t.TempDir()
			t.Setenv("TMPDIR", private)
			ctx := t.Context()
			switch condition {
			case "payload-path":
				payload.Path = filepath.Join(private, "caller-layout")
			case "result-path":
				record := artifacts["result/image/image"]
				record.Path = filepath.Join(private, "caller-result")
				artifacts[record.ID] = record
			case "missing-result":
				delete(artifacts, "result/image/image")
			case "subject":
				subject = "sha256:" + strings.Repeat("a", 64)
			case "profile", "source-metadata":
				record := artifacts["result/image/image"]
				result, err := readProducerResult(ctx, record.Path)
				if err != nil {
					t.Fatal(err)
				}
				if condition == "profile" {
					result.ProfileDigest = "sha256:" + strings.Repeat("a", 64)
				} else {
					result.InputDigest = "sha256:" + strings.Repeat("a", 64)
				}
				raw, err := canonical.Marshal(result)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(record.Path, raw, 0o644); err != nil {
					t.Fatal(err)
				}
			case "canceled":
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			}
			archive, err := prepareAcquiredImageSBOMArchive(ctx, plan, node, payload, artifacts, subject)
			if archive != nil {
				t.Fatalf("substitution admitted: %v", err)
			}
			want := e.CodeProvenanceMissing
			if condition == "profile" {
				want = e.CodeVerifyFailed
			}
			if condition == "canceled" {
				want = e.CodeTimeout
			}
			assertFactoryErrorCode(t, err, want)
			entries, err := os.ReadDir(private)
			if err != nil || len(entries) != 0 {
				t.Fatalf("snapshot preceded pair admission: %v %v", entries, err)
			}
		})
	}
}

func TestAcquiredImageSBOMImportFailureCleansPrivateSnapshot(t *testing.T) {
	plan, node, payload, artifacts, subject := acquiredImageSBOMFixture(t, "amd64", types.OCIManifestSchema1, true)
	private := t.TempDir()
	t.Setenv("TMPDIR", private)
	// The acquisition owner verifies the complete pinned closure. The stricter
	// runnable-image importer then rejects an image without a command.
	if _, err := acquiredImageSBOMBinding(t.Context(), plan, node, payload, artifacts, subject); err != nil {
		t.Fatal(err)
	}
	archive, err := prepareAcquiredImageSBOMArchive(t.Context(), plan, node, payload, artifacts, subject)
	if archive != nil {
		t.Fatal("unsupported image returned scan transport")
	}
	assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
	entries, err := os.ReadDir(private)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed import leaked workspace: %v %v", entries, err)
	}
}

func TestAcquiredImageSBOMScannerAdmission(t *testing.T) {
	var noContext context.Context
	_, err := inspectAcquiredImageDependencies(noContext, nil, Node{}, Artifact{}, nil, "", sbom.Scanner{})
	assertFactoryErrorCode(t, err, e.CodeUsage)
	_, err = inspectAcquiredImageDependencies(t.Context(), nil, Node{}, Artifact{}, nil, "", sbom.Scanner{})
	assertFactoryErrorCode(t, err, e.CodeProvenanceMissing)
	for _, condition := range []string{"path", "digest", "missing-tool"} {
		t.Run(condition, func(t *testing.T) {
			plan, node, payload, artifacts, subject := acquiredImageSBOMFixture(t, "amd64", types.OCIManifestSchema1)
			scanner := sbom.Scanner{Path: artifacts["tool/syft"].Path, Digest: toolExecutableDigest(plan, "syft")}
			switch condition {
			case "path":
				scanner.Path = "caller-scanner"
			case "digest":
				scanner.Digest = "sha256:" + strings.Repeat("a", 64)
			case "missing-tool":
				delete(artifacts, "tool/syft")
			}
			private := t.TempDir()
			t.Setenv("TMPDIR", private)
			_, err := inspectAcquiredImageDependencies(t.Context(), plan, node, payload, artifacts, subject, scanner)
			assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
			entries, err := os.ReadDir(private)
			if err != nil || len(entries) != 0 {
				t.Fatalf("scanner substitution created scratch: %v %v", entries, err)
			}
		})
	}
}

func TestAcquiredImageSBOMRechecksOriginalResultAfterPreparation(t *testing.T) {
	plan, node, payload, artifacts, subject := acquiredImageSBOMFixture(t, "amd64", types.DockerManifestSchema2)
	archive, err := prepareAcquiredImageSBOMArchive(t.Context(), plan, node, payload, artifacts, subject)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = archive.close() }()
	record := artifacts["result/image/image"]
	result, err := readProducerResult(t.Context(), record.Path)
	if err != nil {
		t.Fatal(err)
	}
	result.Qualification = pipeline.ArtifactUnqualified
	raw, err := canonical.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(record.Path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = acquiredImageSBOMBinding(t.Context(), plan, node, payload, artifacts, subject)
	assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
}
