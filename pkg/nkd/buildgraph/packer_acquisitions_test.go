package buildgraph

import (
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
)

func TestPackerAcquisitionArchiveLowering(t *testing.T) {
	for _, path := range []string{"", "captures/packages.tar"} {
		t.Run(path, func(t *testing.T) {
			input := &catalog.PackerConfig{AcquisitionArchive: path}
			compiled := packerConfig(input)
			if compiled.AcquisitionArchive != path {
				t.Fatalf("lost capture path: %+v", compiled)
			}
			input.AcquisitionArchive = "changed"
			if compiled.AcquisitionArchive != path {
				t.Fatal("caller changed compiled capture path")
			}
		})
	}
}
