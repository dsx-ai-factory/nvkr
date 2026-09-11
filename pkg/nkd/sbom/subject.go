package sbom

import (
	"sort"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// NewSubject starts a packaging-stage document without inventing upstream source
// metadata. The recipe identity and inspected package bytes are its inputs.
func NewSubject(recipeDigest, name, digest string) (*BOM, error) {
	bom := &BOM{BOMFormat: bomFormat, SpecVersion: specVersion, Version: 1, SerialNumber: deriveURN(recipeDigest + "/" + name), Metadata: Metadata{
		Component: Component{Type: "application", Name: name, Hashes: []Hash{{Alg: "SHA-256", Content: strings.TrimPrefix(digest, "sha256:")}}},
		Tools:     []ToolRef{{Vendor: "NVIDIA", Name: "nkdctl"}}, Props: []Property{{Name: "nkd:artifact", Value: name}, {Name: "nkd:recipeDigest", Value: recipeDigest}},
	}}
	if !canonical.IsDigest(digest) {
		return nil, e.New(e.CodeUnverifiedRef, "SBOM subject requires an exact digest")
	}
	if err := bom.Validate(); err != nil {
		return nil, err
	}
	return bom, nil
}

// ForSubject binds an inspected inventory to a precise build or packaging stage.
// It is descriptive transformation, not authentication; the publisher must verify
// the relationship between inspected input and the delivered subject.
func ForSubject(bom *BOM, digest, stage, inputDigest string) (*BOM, error) {
	if err := bom.Validate(); err != nil {
		return nil, err
	}
	if !canonical.IsDigest(digest) || (inputDigest != "" && !canonical.IsDigest(inputDigest)) || !validEvidenceStage(stage) {
		return nil, e.New(e.CodeUnverifiedRef, "SBOM stage/subject binding is invalid")
	}
	result := cloneBOM(bom)
	recipe, artifact, err := metadataBindings(result.Metadata.Props)
	if err != nil {
		return nil, err
	}
	result.Metadata.Component.Hashes = []Hash{{Alg: "SHA-256", Content: strings.TrimPrefix(digest, "sha256:")}}
	if len(result.Dependencies) > 0 {
		result.Metadata.Component.BOMRef = "artifact:" + strings.TrimPrefix(digest, "sha256:")
		for i := range result.Dependencies {
			if result.Dependencies[i].Ref == bom.Metadata.Component.BOMRef {
				result.Dependencies[i].Ref = result.Metadata.Component.BOMRef
			}
		}
	}
	props := result.Metadata.Props[:0]
	for _, p := range result.Metadata.Props {
		if p.Name != "nkd:evidenceStage" && p.Name != "nkd:inspectionInputDigest" {
			props = append(props, p)
		}
	}
	result.Metadata.Props = append(props, Property{Name: "nkd:evidenceStage", Value: stage})
	if inputDigest != "" && inputDigest != digest {
		result.Metadata.Props = append(result.Metadata.Props, Property{Name: "nkd:inspectionInputDigest", Value: inputDigest})
	}
	sort.Slice(result.Metadata.Props, func(i, j int) bool { return result.Metadata.Props[i].Name < result.Metadata.Props[j].Name })
	result.SerialNumber, err = stageSerialNumber(recipe, artifact, stage, digest)
	if err != nil {
		return nil, err
	}
	if err := result.Validate(); err != nil {
		return nil, err
	}
	return result, nil
}

func validEvidenceStage(stage string) bool {
	return stage == "build-output" || stage == "source-build" || stage == "packaging"
}

func stageSerialNumber(recipe, artifact, stage, digest string) (string, error) {
	identity, err := canonical.Digest(canonical.KindSBOM, struct {
		Recipe   string `json:"recipe"`
		Artifact string `json:"artifact"`
		Stage    string `json:"stage"`
		Subject  string `json:"subject"`
	}{recipe, artifact, stage, digest})
	if err != nil {
		return "", err
	}
	return deriveURN(identity), nil
}
