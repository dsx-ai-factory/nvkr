package ir

import (
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestPackerAcquisitionArchiveRejectsUnsafeOrDiskPaths(t *testing.T) {
	for _, path := range []string{".", "../capture.tar", "/capture.tar", "capture-{{.Arch}}.tar", "node.qcow2"} {
		t.Run(path, func(t *testing.T) {
			graph := vmGraph(t)
			graph.Nodes[0].Packer.AcquisitionArchive = path
			assertIRCode(t, graph.Validate(), e.CodeInvalidCatalog)
		})
	}
}
