package delivery

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// Tiny synthetic closures establish admission order and unique disk accounting,
// not observed production capacity, a reservation or a real inspected disk.
func TestPublicationAdmitsUniqueDiskScratchBeforeSnapshot(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			for _, admitted := range []bool{false, true} {
				t.Run(map[bool]string{false: "insufficient", true: "admitted"}[admitted], func(t *testing.T) {
					request, manifest := diskPublicationRequest(t, arch)
					otherArch := "arm64"
					if arch == "arm64" {
						otherArch = "amd64"
					}
					otherLayout, _, otherManifest := diskFixture(t, "linux/"+otherArch)
					otherManifest = rewriteDiskFixture(t, otherLayout, otherManifest, func(wrapper *ociManifest, _ *[]byte) {
						wrapper.ArtifactType = MediaDiskImage
					})
					_, otherIndexRaw, err := readIndex(t.Context(), otherLayout)
					if err != nil {
						t.Fatal(err)
					}
					second := request.Objects[0]
					second.Name = "node-two"
					second.Path, second.Arch, second.BlobDigest = otherLayout, otherArch, digestBytes(otherIndexRaw)
					binding := *second.Output
					binding.Repository = second.Name
					second.Output = &binding
					request.Objects = append(request.Objects, second)
					for index := range request.Objects {
						if request.Objects[index].Evidence != nil {
							request.Objects[index].Evidence.Outputs = []string{"node-image", "node-two"}
						}
					}
					disk := request.Objects[0].Output.Disk
					outputs := []Output{
						{Name: "node-image", Kind: "disk-image", Digest: manifest, BuildDigest: binding.BuildDigest, Components: binding.Components, Disk: disk},
						{Name: "node-two", Kind: "disk-image", Digest: otherManifest, BuildDigest: binding.BuildDigest, Components: binding.Components, Disk: disk},
					}
					content, err := ContentDigest(outputs)
					if err != nil {
						t.Fatal(err)
					}
					request.Release.ContentSubject = content
					root := filepath.Join(t.TempDir(), "publication")
					result, err := BuildPublicationRoot(t.Context(), root, request)
					if err != nil {
						t.Fatal(err)
					}
					calls := 0
					stage := ""
					publication, err := openPublication(t.Context(), root, result.RootDigest,
						func(_ context.Context, directory *os.File, required uint64) error {
							calls++
							stage = directory.Name()
							if required != uint64(disk.Size) {
								t.Fatalf("duplicate payload charged twice: need=%d, unique=%d", required, disk.Size)
							}
							if _, err := os.Lstat(diskBlobPath(filepath.Join(stage, "layout"), disk.PayloadDigest)); !os.IsNotExist(err) {
								t.Fatalf("disk snapshot preceded capacity admission: %v", err)
							}
							if !admitted {
								return e.New(e.CodeEngineCapability, "injected insufficient disk-backed scratch")
							}
							return nil
						})
					if calls != 1 {
						t.Fatalf("expected one aggregate capacity admission, calls=%d", calls)
					}
					if admitted {
						if err != nil || publication == nil {
							t.Fatalf("admitted shared-payload closure failed: %v", err)
						}
						defer func() { _ = publication.Close() }()
						outputs, _, err := publication.Artifacts("example.test/nkd/releases@" + result.RootDigest)
						if err != nil || len(outputs) != 2 || len(publication.diskPayloads) != 1 || len(publication.diskFiles) != 1 {
							t.Fatalf("shared payload duplicated snapshot or lost membership: %v, %v", outputs, err)
						}
					} else {
						assertRegistryCode(t, err, e.CodeEngineCapability)
						if publication != nil {
							t.Fatalf("failed admission returned an owner: %v", publication)
						}
					}
					if _, err := os.Lstat(stage); !os.IsNotExist(err) {
						t.Fatalf("failed admission left private scratch: %v", err)
					}
				})
			}
		})
	}
}
