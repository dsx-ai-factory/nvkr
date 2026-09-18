package delivery

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestPublishedImageRejoinsNativeIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, selection, os, arch, artifactType string
		corrupt                                 bool
		valid                                   bool
	}{
		{name: "exact image", selection: "worker", os: "linux", arch: "amd64", artifactType: MediaOCIManifest, valid: true},
		{name: "missing selection", selection: "absent", os: "linux", arch: "amd64", artifactType: MediaOCIManifest},
		{name: "empty selection", os: "linux", arch: "amd64", artifactType: MediaOCIManifest},
		{name: "wrong platform binding", selection: "worker", os: "linux", arch: "arm64", artifactType: MediaOCIManifest},
		{name: "unsupported OS", selection: "worker", os: "windows", arch: "amd64", artifactType: MediaOCIManifest},
		{name: "unsupported architecture", selection: "worker", os: "linux", arch: "riscv64", artifactType: MediaOCIManifest},
		{name: "missing platform", selection: "worker", artifactType: MediaOCIManifest},
		{name: "wrong object type", selection: "worker", os: "linux", arch: "amd64", artifactType: MediaDeliveryObject},
		{name: "corrupt nested manifest", selection: "worker", os: "linux", arch: "amd64", artifactType: MediaOCIManifest, corrupt: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := imageFixture(t)
			want, err := InspectContainerImage(t.Context(), root, "linux/amd64")
			if err != nil {
				t.Fatal(err)
			}
			descriptor, err := PayloadDescriptor(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			// Release outputs bind the native manifest, not a layout's local index.
			want.RootDigest = want.ManifestDigest
			publication := Inspection{Root: PublicationRoot{Objects: []PublishedObject{{
				Name: "worker", Kind: "oci-image", ArtifactType: tc.artifactType,
				DescriptorDigest: descriptor.Digest, BlobDigest: descriptor.Digest,
				SemanticDigest: want.ManifestDigest, Size: descriptor.Size, OS: tc.os, Arch: tc.arch,
			}}}}
			if tc.corrupt {
				path := filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(want.ManifestDigest, "sha256:"))
				if err := os.WriteFile(path, []byte("corrupt"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got, err := inspectPublishedContainerImage(t.Context(), root, publication, tc.selection)
			if tc.valid {
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("published image = %+v, %v; want %+v", got, err, want)
				}
				return
			}
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			if !reflect.DeepEqual(got, ImageInspection{}) {
				t.Fatalf("rejected publication returned image identity: %+v", got)
			}
		})
	}
}
