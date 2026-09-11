package delivery

import (
	"bytes"
	"context"
	"reflect"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

// verifyReleaseEvidence is shared by staged publication and consumption through
// Inspect. Every declared output derives its required evidence slots; metadata
// cannot waive a slot or substitute a document for another stage or subject.
func verifyReleaseEvidence(ctx context.Context, root string, publication PublicationRoot) error {
	if publication.Release == nil {
		return nil
	}
	expected := map[string]EvidenceBinding{}
	recipes := map[string]string{}
	for _, o := range publication.Objects {
		output := o.Output
		if output == nil {
			continue
		}
		recipes[o.Name] = output.RecipeDigest
		subjects := map[string]string{}
		previous := ""
		for _, subject := range output.BuildSubjects {
			if subject.Component <= previous || !isDigest(subject.Digest) {
				return e.New(e.CodeVerifyFailed, "output build subjects must be sorted, unique and exact")
			}
			subjects[subject.Component] = subject.Digest
			previous = subject.Component
		}
		if len(subjects) != len(output.Components) {
			return e.New(e.CodeVerifyFailed, "output build subject membership is incomplete")
		}
		if output.Format == "executable" {
			manifest, err := flatManifest(ctx, root, o)
			if err != nil {
				return err
			}
			raw, err := readBlob(ctx, root, manifest.Config, 8<<20)
			if err != nil {
				return err
			}
			var bundle BinaryBundle
			if err := canonical.UnmarshalExact(raw, &bundle); err != nil {
				return e.Wrap(e.CodeVerifyFailed, err, "decoding release binary config")
			}
			if len(bundle.Entries) != len(subjects) {
				return e.New(e.CodeVerifyFailed, "binary bundle differs from release subjects")
			}
			for _, entry := range bundle.Entries {
				if subjects[entry.Name] != entry.SHA256 {
					return e.New(e.CodeVerifyFailed, "binary SBOM subject differs from delivered file")
				}
			}
		}
		for _, component := range output.Components {
			subject, ok := subjects[component]
			if !ok {
				return e.New(e.CodeVerifyFailed, "output component has no build subject")
			}
			binding := EvidenceBinding{Outputs: []string{o.Name}, Component: component, Stage: "build-output", SubjectKind: "file", SubjectDigest: subject}
			switch output.Format {
			case "oci":
				binding.SubjectKind = "oci-manifest"
				binding.SubjectDigest = o.DescriptorDigest
				binding.InspectionInputDigest = subject
			case "helm-oci":
				binding.Stage = "source-build"
				binding.SubjectKind = "source-archive"
			case "executable", "apt-repository":
			default:
				return e.New(e.CodeVerifyFailed, "release has an unsupported output format")
			}
			expected[o.Name+"/"+binding.Stage+"/"+binding.Component] = binding
		}
		if output.Format == "helm-oci" {
			binding := EvidenceBinding{Outputs: []string{o.Name}, Component: o.Name, Stage: "packaging", SubjectKind: "helm-bundle", SubjectDigest: o.BlobDigest}
			expected[o.Name+"/"+binding.Stage+"/"+binding.Component] = binding
		}
	}
	seen := map[string]bool{}
	for _, object := range publication.Objects {
		if object.Evidence == nil {
			if object.Kind == "sbom" || object.Kind == "sbom-index" {
				return e.New(e.CodeVerifyFailed, "unbound release evidence object")
			}
			continue
		}
		binding := *object.Evidence
		if len(binding.Outputs) == 0 {
			return e.New(e.CodeVerifyFailed, "evidence has no output membership")
		}
		previousOutput := ""
		for _, outputName := range binding.Outputs {
			key := outputName + "/" + binding.Stage + "/" + binding.Component
			want, ok := expected[key]
			single := binding
			single.Outputs = []string{outputName}
			if outputName <= previousOutput || !ok || seen[key] || !reflect.DeepEqual(single, want) || object.Kind != "sbom" || object.ArtifactType != "application/vnd.cyclonedx+json" {
				return e.New(e.CodeVerifyFailed, "missing, duplicate or substituted evidence binding %q", key)
			}
			previousOutput = outputName
			seen[key] = true
		}
		manifest, err := flatManifest(ctx, root, object)
		if err != nil {
			return err
		}
		if len(manifest.Layers) != 1 || manifest.Layers[0].MediaType != "application/vnd.cyclonedx+json" {
			return e.New(e.CodeVerifyFailed, "SBOM manifest has an invalid payload")
		}
		raw, err := readBlob(ctx, root, manifest.Layers[0], 8<<20)
		if err != nil {
			return err
		}
		var bom sbom.BOM
		if err := canonical.UnmarshalExact(bytes.TrimSuffix(raw, []byte{'\n'}), &bom); err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "decoding release SBOM")
		}
		digest, err := bom.Digest()
		if err != nil || digest != object.SemanticDigest || object.Name != "sbom-"+strings.TrimPrefix(digest, "sha256:") || len(bom.Metadata.Component.Hashes) != 1 || "sha256:"+bom.Metadata.Component.Hashes[0].Content != binding.SubjectDigest || bom.Metadata.Component.Name != binding.Component {
			return e.New(e.CodeVerifyFailed, "SBOM document differs from its exact release subject")
		}
		props := map[string]string{}
		for _, p := range bom.Metadata.Props {
			props[p.Name] = p.Value
		}
		for _, outputName := range binding.Outputs {
			if props["nkd:recipeDigest"] != recipes[outputName] {
				return e.New(e.CodeVerifyFailed, "SBOM recipe is unbound")
			}
		}
		if props["nkd:evidenceStage"] != binding.Stage || props["nkd:inspectionInputDigest"] != binding.InspectionInputDigest {
			return e.New(e.CodeVerifyFailed, "SBOM recipe, stage or inspection input is unbound")
		}
		if binding.Stage == "packaging" && (len(bom.Components) == 0 || props["nkd:dependencyInspection"] != "helm-manifests/v1") {
			return e.New(e.CodeProvenanceMissing, "final Helm package requires an inspected inventory")
		}
	}
	if len(seen) != len(expected) {
		return e.New(e.CodeProvenanceMissing, "release omits required stage-specific SBOM evidence")
	}
	return nil
}
