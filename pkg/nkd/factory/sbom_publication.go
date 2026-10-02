package factory

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

// sbomPublicationObjects reopens evidence once and returns those exact held bytes.
// Packaging and hosted finalization share this subject/index/scanner validation.
// These objects gain issuer authentication only when their enclosing root is signed.
func sbomPublicationObjects(ctx context.Context, plan *Plan, artifact Artifact, artifacts map[string]Artifact, local func(string) (string, error)) ([]delivery.ObjectInput, error) {
	path, err := local(artifact.Path)
	if err != nil {
		return nil, err
	}
	raw, err := readSmallFactoryFile(ctx, filepath.Join(path, "index.json"), "hosted SBOM index")
	if err != nil {
		return nil, e.Wrap(e.CodeProvenanceMissing, err, "reading hosted SBOM index")
	}
	var index sbom.Index
	if err := canonical.UnmarshalExact(bytes.TrimSuffix(raw, []byte{'\n'}), &index); err != nil {
		return nil, e.Wrap(e.CodeProvenanceMissing, err, "decoding hosted SBOM index")
	}
	if err := index.Validate(); err != nil {
		return nil, err
	}
	node := planNode(plan, artifact.Producer)
	results := map[string]pipeline.ArtifactResult{}
	for _, inputID := range node.Inputs {
		input := artifacts[inputID]
		if input.Kind != "producer-result" {
			continue
		}
		inputPath, err := local(input.Path)
		if err != nil {
			return nil, err
		}
		result, err := readProducerResult(ctx, inputPath)
		if err != nil {
			return nil, e.Wrap(e.CodeProvenanceMissing, err, "reading hosted producer result")
		}
		if result.Component != input.Component || input.BuildTarget != artifact.BuildTarget || results[result.Component].Component != "" {
			return nil, e.New(e.CodeVerifyFailed, "SBOM producer identity is ambiguous")
		}
		results[result.Component] = result
	}
	buildDigest := ""
	for _, build := range plan.Builds {
		if build.Name == artifact.BuildTarget {
			buildDigest = build.BuildDigest
		}
	}
	if index.BuildDigest != buildDigest || buildDigest == "" {
		return nil, e.New(e.CodeVerifyFailed, "SBOM index has the wrong build")
	}
	indexDigest, err := index.Digest()
	if err != nil {
		return nil, err
	}
	objects := []delivery.ObjectInput{{Name: "sbom-index-" + strings.TrimPrefix(indexDigest, "sha256:"), Kind: "sbom-index", ArtifactType: "application/vnd.nkd.sbom-index.v2+json", LayerMediaType: "application/vnd.nkd.sbom-index.v2+json", SemanticDigest: indexDigest, Bytes: raw}}
	if len(results) != len(index.Entries) {
		return nil, e.New(e.CodeProvenanceMissing, "hosted SBOM index does not cover every producer result")
	}
	for _, entry := range index.Entries {
		result, ok := results[entry.Artifact]
		if !ok || entry.ArtifactSHA != result.SHA256 {
			return nil, e.New(e.CodeVerifyFailed, "hosted SBOM entry %q has the wrong subject", entry.Artifact)
		}
		bomRaw, err := readSmallFactoryFile(ctx, filepath.Join(path, entry.Artifact+".cdx.json"), "hosted per-artifact SBOM")
		if err != nil {
			return nil, e.Wrap(e.CodeProvenanceMissing, err, "reading hosted per-artifact SBOM")
		}
		var bom sbom.BOM
		if err := canonical.UnmarshalExact(bytes.TrimSuffix(bomRaw, []byte{'\n'}), &bom); err != nil {
			return nil, e.Wrap(e.CodeProvenanceMissing, err, "decoding hosted per-artifact SBOM")
		}
		digest, err := bom.Digest()
		if err != nil || digest != entry.SBOMDigest {
			return nil, e.New(e.CodeVerifyFailed, "hosted per-artifact SBOM %q differs from its index", entry.Artifact)
		}
		if len(bom.Metadata.Component.Hashes) != 1 || "sha256:"+bom.Metadata.Component.Hashes[0].Content != entry.ArtifactSHA || bom.Metadata.Component.Name != entry.Artifact {
			return nil, e.New(e.CodeVerifyFailed, "hosted SBOM subject differs from its producer")
		}
		for _, prop := range bom.Metadata.Props {
			if prop.Name == "nkd:buildDigest" && prop.Value != buildDigest {
				return nil, e.New(e.CodeVerifyFailed, "SBOM document has the wrong build")
			}
		}
		objects = append(objects, delivery.ObjectInput{Name: "sbom-" + strings.TrimPrefix(digest, "sha256:"), Kind: "sbom", ArtifactType: "application/vnd.cyclonedx+json", LayerMediaType: "application/vnd.cyclonedx+json", SemanticDigest: digest, Bytes: bomRaw})
		if plan.Definition.SBOMTool != "" {
			if len(bom.Components) == 0 {
				return nil, e.New(e.CodeProvenanceMissing, "hosted SBOM omits required dependency inventory")
			}
			method, scanner := "", ""
			for _, property := range bom.Metadata.Props {
				if property.Name == "nkd:dependencyInspection" {
					method = property.Value
				}
				if property.Name == "nkd:dependencyScannerDigest" {
					scanner = property.Value
				}
			}
			kind := ""
			for _, inputID := range node.Inputs {
				input := artifacts[inputID]
				if input.Component == entry.Artifact && input.Kind != "producer-result" {
					var err error
					kind, err = plan.sbomPayloadKind(input)
					if err != nil {
						return nil, err
					}
				}
			}
			if (kind == "tarball" && method != "helm-manifests/v1") || (kind != "tarball" && (method != "syft-cyclonedx/v1" || scanner != toolExecutableDigest(plan, plan.Definition.SBOMTool))) {
				return nil, e.New(e.CodeVerifyFailed, "hosted SBOM inspection does not match the planned scanner")
			}
		}
	}
	return objects, nil
}
