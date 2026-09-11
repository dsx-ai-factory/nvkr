package delivery

import (
	"regexp"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// ReleaseMetadata is the release-wide context stored in the sole release index
// metadata document. Output identities and evidence membership live on Objects.
type ReleaseMetadata struct {
	DefinitionDigest  string           `json:"definitionDigest"`
	EnvironmentDigest string           `json:"environmentDigest"`
	ContentSubject    string           `json:"contentSubject"`
	Clusters          []ReleaseCluster `json:"clusters,omitempty"`
}

type ReleaseCluster struct {
	SchemaVersion     string                 `json:"schemaVersion"`
	Name              string                 `json:"name"`
	CompositionDigest string                 `json:"compositionDigest"`
	ContentSubject    string                 `json:"contentSubject"`
	Members           []ReleaseClusterMember `json:"members"`
}

type ReleaseClusterMember struct {
	Output         string   `json:"output"`
	ArtifactDigest string   `json:"artifactDigest"`
	RecipeDigest   string   `json:"recipeDigest"`
	Components     []string `json:"components"`
	Role           string   `json:"role,omitempty"`
}

// OutputBinding adds meaning to the object's one descriptor; it does not repeat
// the name or artifact digest already owned by the release index object.
type OutputBinding struct {
	// Repository is a sibling repository name relative to the release repository namespace.
	Repository    string         `json:"repository"`
	Format        string         `json:"format"`
	RecipeDigest  string         `json:"recipeDigest"`
	Components    []string       `json:"components"`
	TransportPath string         `json:"transportPath,omitempty"`
	BuildSubjects []BuildSubject `json:"buildSubjects,omitempty"`
}

type BuildSubject struct {
	Component string `json:"component"`
	Digest    string `json:"digest"`
}

// EvidenceBinding describes exactly which stage and bytes a document inventories.
// DescriptorDigest and BlobDigest on the enclosing object bind the document.
type EvidenceBinding struct {
	DocumentDigest        string   `json:"documentDigest"`
	Outputs               []string `json:"outputs"`
	Component             string   `json:"component"`
	Stage                 string   `json:"stage"`
	SubjectKind           string   `json:"subjectKind"`
	SubjectDigest         string   `json:"subjectDigest"`
	InspectionInputDigest string   `json:"inspectionInputDigest,omitempty"`
}

func validateReleaseMetadata(root PublicationRoot) error {
	if root.Release == nil {
		return nil
	}
	m := root.Release
	if !canonical.IsDigest(m.DefinitionDigest) || !canonical.IsDigest(m.EnvironmentDigest) || !canonical.IsDigest(m.ContentSubject) {
		return e.New(e.CodeVerifyFailed, "release metadata has incomplete identities")
	}
	outputs := map[string]PublishedObject{}
	repositories := map[string]bool{}
	names := map[string]bool{}
	for _, o := range root.Objects {
		if names[o.Name] {
			return e.New(e.CodeVerifyFailed, "duplicate release object name")
		}
		names[o.Name] = true
		if o.Output == nil && o.Evidence == nil && o.Kind != "cluster-composition" {
			return e.New(e.CodeVerifyFailed, "release object has no declared role")
		}

		if o.Output == nil {
			continue
		}
		if o.Evidence != nil || !canonical.IsDigest(o.Output.RecipeDigest) {
			return e.New(e.CodeVerifyFailed, "release object has conflicting output/evidence roles")
		}
		if _, ok := outputs[o.Name]; ok {
			return e.New(e.CodeVerifyFailed, "duplicate release output")
		}
		if !siblingRepositoryPattern.MatchString(o.Output.Repository) || repositories[o.Output.Repository] {
			return e.New(e.CodeVerifyFailed, "output requires a unique canonical sibling repository")
		}
		repositories[o.Output.Repository] = true
		var kind, media string
		switch o.Output.Format {
		case "executable":
			kind, media = "binary-bundle", MediaBinaryBundle
		case "oci":
			kind, media = "container-image", MediaOCIManifest
		case "helm-oci":
			kind, media = "helm-oci", "application/vnd.nkd.aicr-helm-bundle.v1"
		case "apt-repository":
			kind, media = "apt-repository", "application/vnd.nkd.apt-repository.v1"
		default:
			return e.New(e.CodeVerifyFailed, "unsupported release output format")
		}
		if o.Kind != kind || o.ArtifactType != media {
			return e.New(e.CodeVerifyFailed, "release output type differs from format")
		}
		previous := ""
		for _, component := range o.Output.Components {
			if !canonicalToken(component) || component <= previous {
				return e.New(e.CodeVerifyFailed, "output components must be sorted and unique")
			}
			previous = component
		}
		if o.Output.Format != "helm-oci" && len(o.Output.Components) == 0 {
			return e.New(e.CodeProvenanceMissing, "release output has no build subjects")
		}
		if o.Output.Format == "oci" && len(o.Output.Components) != 1 {
			return e.New(e.CodeVerifyFailed, "image requires one component")
		}
		outputs[o.Name] = o
	}
	if len(outputs) == 0 {
		return e.New(e.CodeReleaseIncomplete, "release has no deliverables")
	}
	return nil
}

var siblingRepositoryPattern = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)
