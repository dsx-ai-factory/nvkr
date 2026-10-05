package delivery

import (
	"os"
	"path/filepath"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

// Synthetic documents exercise release membership, not live scanner authority.
func TestDiskPublicationRequiresMaterialContentInventory(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		for _, method := range []string{"syft-cyclonedx/v1", "disk-reconciled/v1"} {
			t.Run(architecture+"/"+method, func(t *testing.T) {
				request, _ := diskPublicationRequest(t, architecture)
				changeDiskFixtureComponentBOM(&request, "agent", func(bom *sbom.BOM) {
					for index, property := range bom.Metadata.Props {
						if property.Name == "nkd:dependencyInspection" {
							bom.Metadata.Props[index].Value = method
							return
						}
					}
					t.Fatal("fixture lacks material method")
				})
				output := filepath.Join(t.TempDir(), "rejected")
				_, err := BuildPublicationRoot(t.Context(), output, request)
				assertRegistryCode(t, err, e.CodeProvenanceMissing)
				if _, err := os.Lstat(output); !os.IsNotExist(err) {
					t.Fatalf("rejected material evidence exposed output: %v", err)
				}
			})
		}
	}
}
