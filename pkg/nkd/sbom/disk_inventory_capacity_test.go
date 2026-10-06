package sbom

import (
	"fmt"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

// This measures complete modeled file projection, not Ubuntu discovery, a
// native helper run or issuance of an opaque runtime observation.
func TestCompleteDiskFileInventoryCapacity(t *testing.T) {
	for _, count := range []int{8000, 50000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			inspection := contentFixture("tarball", "amd64", "a")
			inspection.report.Components = nil
			inspection.report.Files = make([]DiskFile, count)
			for index := range inspection.report.Files {
				inspection.report.Files[index] = DiskFile{
					Path: fmt.Sprintf("usr/share/doc/package-%06d/architecture-independent-data.txt", index),
					Kind: "regular", Size: 1, Digest: "sha256:" + strings.Repeat("b", 64),
				}
			}
			report, err := canonical.Marshal(inspection.report)
			if err != nil || len(report) > pipeline.MaxDiskContentReportBytes {
				t.Fatalf("modeled helper report: bytes=%d, error=%v", len(report), err)
			}
			document, err := DiskContentDocument("sha256:"+strings.Repeat("c", 64), "material", inspection)
			if err != nil {
				t.Fatal(err)
			}
			full, err := canonical.Marshal(document)
			if err != nil || len(document.Components) != count || len(document.Dependencies[0].DependsOn) != count {
				t.Fatalf("inventory was omitted: components=%d bytes=%d error=%v", len(document.Components), len(full), err)
			}
			t.Logf("files=%d report=%d complete CycloneDX=%d policy=%d", count, len(report), len(full), MaxDocumentBytes)
			raw, err := document.Marshal()
			if err != nil || len(raw) != len(full) {
				t.Fatalf("bounded inventory: bytes=%d error=%v", len(raw), err)
			}
		})
	}
}

func TestDiskContentReportByteBoundBeforeDecoding(t *testing.T) {
	inspection, err := decodeBoundDiskContentReport([]byte(strings.Repeat(" ", pipeline.MaxDiskContentReportBytes+1)), "", diskContentReport{})
	assertDependencyCode(t, err, e.CodeEnvelopeDecode)
	if inspection != nil || !strings.Contains(err.Error(), "exceeds its budget") {
		t.Fatalf("oversized report reached decoding or returned inventory: %v, %v", inspection, err)
	}
}
