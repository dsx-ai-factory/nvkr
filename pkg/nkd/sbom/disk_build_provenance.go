package sbom

import (
	"sort"
	"strings"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// DiskBuildProvenance records producer evidence, never an installed inventory.
const DiskBuildProvenance = "build-provenance-v1"

// ForDiskBuildProvenance explicitly labels producer-only scope on an exact source-bound
// subject. The factory must authenticate that subject against its live producer.
func ForDiskBuildProvenance(bom *BOM, digest string) (*BOM, error) {
	result, err := ForSubject(bom, digest, "build-output", "")
	if err != nil {
		return nil, err
	}
	result.Metadata.Props = append(result.Metadata.Props,
		Property{Name: "nkd:diskEvidence", Value: DiskBuildProvenance},
		Property{Name: "nkd:diskInspection", Value: "not-performed"})
	sort.Slice(result.Metadata.Props, func(i, j int) bool { return result.Metadata.Props[i].Name < result.Metadata.Props[j].Name })
	if err := ValidateDiskBuildProvenance(result); err != nil {
		return nil, err
	}
	return result, nil
}

// ValidateDiskBuildProvenance rejects missing scope and any invented inspection
// claims. Exact byte, source and release membership checks remain with callers.
func ValidateDiskBuildProvenance(bom *BOM) error {
	if err := bom.Validate(); err != nil {
		return err
	}
	if len(bom.Components) != 0 || len(bom.Dependencies) != 0 {
		return e.New(e.CodeVerifyFailed, "disk build provenance cannot claim inspected dependencies")
	}
	props := map[string]string{}
	for _, p := range bom.Metadata.Props {
		props[p.Name] = p.Value
		if strings.HasPrefix(p.Name, "nkd:dependency") || strings.HasPrefix(p.Name, "nkd:diskInspect") && p.Name != "nkd:diskInspection" || p.Name == "nkd:diskCodecDigest" || p.Name == "nkd:inspectionInputDigest" {
			return e.New(e.CodeVerifyFailed, "disk build provenance carries an inspection claim")
		}
	}
	if props["nkd:diskEvidence"] != DiskBuildProvenance || props["nkd:diskInspection"] != "not-performed" || props["nkd:evidenceStage"] != "build-output" {
		return e.New(e.CodeProvenanceMissing, "disk build provenance lacks explicit evidence scope")
	}
	return nil
}
