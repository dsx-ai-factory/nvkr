package delivery

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestReleaseDiskTypeAdmissionRejectsOtherArtifactFamilies(t *testing.T) {
	for _, artifactType := range []string{MediaDiskImage, testDiskType, MediaOCIManifest, MediaPublicationRoot, "application/vnd.cyclonedx+json"} {
		t.Run(artifactType, func(t *testing.T) {
			request, _ := diskPublicationRequest(t, "amd64")
			request.Objects[0].ArtifactType = artifactType
			// The release constructor must reject unsupported types before opening
			// the source layout, even when the MIME grammar admits them.
			request.Objects[0].Path = "missing-layout"
			output := filepath.Join(t.TempDir(), "never-created")
			_, err := BuildPublicationRoot(t.Context(), output, request)
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			if artifactType != MediaDiskImage && !strings.Contains(err.Error(), "disk release input requires") {
				t.Fatalf("unsupported artifact type reached layout I/O: %v", err)
			}
			if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("refusal exposed output: %v", err)
			}
		})
	}
}

// This writes hostile metadata beneath the producer constructor. All outer
// content identities are recomputed so OpenPublication must reject the actual
// inconsistent disk binding, not merely a stale metadata hash.
func rewriteDiskPublicationRoot(t *testing.T, root string, inspection Inspection, edit func(*PublicationRoot)) string {
	t.Helper()
	publication := inspection.Root
	edit(&publication)
	raw, err := canonical.Marshal(publication)
	if err != nil {
		t.Fatal(err)
	}
	writer := layoutWriter{root: root}
	config, err := writer.addBlobBytes(raw, MediaPublicationRootConfig)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := writer.addJSONBlob(ociManifest{SchemaVersion: 2, MediaType: MediaOCIManifest,
		ArtifactType: MediaPublicationRoot, Config: config, Layers: []Descriptor{}}, MediaOCIManifest)
	if err != nil {
		t.Fatal(err)
	}
	descriptor.ArtifactType = MediaPublicationRoot
	semantic, err := publication.Digest()
	if err != nil {
		// Invalid release metadata is intentionally encoded without validation.
		semantic, err = canonical.Digest(canonical.KindPublicationRoot, publication)
		if err != nil {
			t.Fatal(err)
		}
	}
	index, _, err := readIndex(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	for _, original := range index.Manifests {
		if err := os.Remove(diskBlobPath(root, original.Digest)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(diskBlobPath(root, inspection.RootConfig.Digest)); err != nil {
		t.Fatal(err)
	}
	pin, err := writer.writeIndex(ociIndex{SchemaVersion: 2, MediaType: MediaOCIIndex,
		ArtifactType: MediaPublicationRoot, Manifests: []Descriptor{descriptor},
		Annotations: map[string]string{"dev.nkd.semantic.digest": semantic}})
	if err != nil {
		t.Fatal(err)
	}
	return pin
}

func TestOpenPublicationRejectsSelfPinnedDiskBindingChanges(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		for _, test := range []struct {
			name, diagnostic string
			edit             func(*PublishedObject)
		}{
			{"size", "disk wrapper differs", func(o *PublishedObject) { o.Output.Disk.Size++ }},
			{"payload", "published disk requires", func(o *PublishedObject) { o.Output.Disk.PayloadDigest = digestBytes([]byte("sibling")) }},
			{"missing binding", "published disk requires", func(o *PublishedObject) { o.Output.Disk = nil }},
			{"non-disk binding", "non-disk release output", func(o *PublishedObject) {
				o.Kind, o.ArtifactType, o.Output.Format = "container-image", MediaOCIManifest, "oci"
			}},
		} {
			t.Run(arch+"/"+test.name, func(t *testing.T) {
				request, _ := diskPublicationRequest(t, arch)
				root := filepath.Join(t.TempDir(), "publication")
				if _, err := BuildPublicationRoot(t.Context(), root, request); err != nil {
					t.Fatal(err)
				}
				inspection, err := Inspect(t.Context(), root)
				if err != nil {
					t.Fatal(err)
				}
				pin := rewriteDiskPublicationRoot(t, root, inspection, func(publication *PublicationRoot) {
					for index := range publication.Objects {
						if publication.Objects[index].Output != nil {
							test.edit(&publication.Objects[index])
						}
					}
				})
				owner, err := OpenPublication(t.Context(), root, pin)
				assertRegistryCode(t, err, e.CodeVerifyFailed)
				if owner != nil || !strings.Contains(err.Error(), test.diagnostic) {
					t.Fatalf("changed binding reached the wrong gate: owner=%v, %v", owner, err)
				}
			})
		}
	}
}

func TestDiskPayloadCopyRechecksExistingBlob(t *testing.T) {
	for _, condition := range []string{"identical", "changed", "symlink"} {
		t.Run(condition, func(t *testing.T) {
			root, payload, manifest := diskFixture(t, "linux/amd64")
			disk, err := OpenDiskArtifact(t.Context(), root, manifest, "linux/amd64", int64(len(payload)))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = disk.Close() }()
			writer := layoutWriter{root: t.TempDir()}
			if err := writer.copyDiskPayload(t.Context(), disk); err != nil {
				t.Fatal(err)
			}
			path := diskBlobPath(writer.root, digestBytes(payload))
			switch condition {
			case "changed":
				if err := os.WriteFile(path, []byte("different"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(diskBlobPath(root, digestBytes(payload)), path); err != nil {
					t.Fatal(err)
				}
			}
			err = writer.copyDiskPayload(t.Context(), disk)
			if condition == "identical" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertRegistryCode(t, err, e.CodeVerifyFailed)
			}
			if _, err := disk.WriteToContext(t.Context(), io.Discard); err != nil {
				t.Fatalf("destination change corrupted source owner: %v", err)
			}
		})
	}
}

func TestPrivateDiskOutputProjectionRequiresCurrentWalk(t *testing.T) {
	request, _ := diskPublicationRequest(t, "amd64")
	root := filepath.Join(t.TempDir(), "publication")
	if _, err := BuildPublicationRoot(t.Context(), root, request); err != nil {
		t.Fatal(err)
	}
	disks := map[string]DiskArtifactInspection{}
	inspection, err := inspectPublicationWithDisks(t.Context(), root, nil, disks)
	if err != nil {
		t.Fatal(err)
	}
	for _, condition := range []string{"valid", "missing", "size", "manifest"} {
		t.Run(condition, func(t *testing.T) {
			observations := make(map[string]DiskArtifactInspection, len(disks))
			for pin, observation := range disks {
				if condition == "missing" {
					continue
				}
				if condition == "size" {
					observation.Size++
				}
				if condition == "manifest" {
					observation.ManifestDigest = digestBytes([]byte("sibling"))
				}
				observations[pin] = observation
			}
			outputs, err := inspectOutputs(t.Context(), root, inspection, t.TempDir(), map[string]publicationPlatform{}, observations)
			if condition == "valid" {
				if err != nil || len(outputs) != 1 {
					t.Fatalf("checked private projection: %v, %v", outputs, err)
				}
			} else {
				assertRegistryCode(t, err, e.CodeVerifyFailed)
				if outputs != nil {
					t.Fatal("failed projection returned partial outputs")
				}
			}
		})
	}
}
