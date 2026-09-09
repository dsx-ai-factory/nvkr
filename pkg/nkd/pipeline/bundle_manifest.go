package pipeline

import (
	"bytes"
	"path/filepath"
	"strings"

	"github.com/nhensley/nkdctl/pkg/nkd/canonical"
	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
)

const BundleManifestSchema = "nkd.dev/execution-bundle-manifest/v1"

// BundleMember binds the exact bytes and intended mode of one execution file.
type BundleMember struct {
	Name      string `json:"name"`
	MediaType string `json:"mediaType"`
	SHA256    string `json:"sha256"`
	Mode      uint32 `json:"mode"`
}

// BundleManifest is the commit record for the generated pipeline transaction. It
// binds the full emitted file (including provenance header), canonical nodes.json,
// and transport plan. BundleDigest is the semantic execution identity already
// recorded in the header; the commit manifest is intentionally a separate exact-byte
// transaction record rather than a self-hashing member.
type BundleManifest struct {
	SchemaVersion string         `json:"schemaVersion"`
	Engine        string         `json:"engine"`
	RecipeID      string         `json:"recipeId"`
	RecipeDigest  string         `json:"recipeDigest"`
	BundleDigest  string         `json:"bundleDigest"`
	Members       []BundleMember `json:"members"`
}

// ManifestJSON returns canonical bytes for the exact generated execution bundle.
func ManifestJSON(engine, recipeID, recipeDigest, pipelineName string, emitted, nodes, transportPlan []byte) ([]byte, error) {
	bundleDigest := generatedHeaderValue(emitted, "bundleDigest")
	if bundleDigest == "" || generatedHeaderValue(emitted, "nodesDigest") != FileDigest(nodes) ||
		generatedHeaderValue(emitted, "transportPlanDigest") != FileDigest(transportPlan) || generatedHeaderValue(emitted, "recipeDigest") != recipeDigest {
		return nil, e.New(e.CodePipelineDrift, "generated pipeline header does not bind the supplied recipe and nodes.json bytes")
	}
	name := filepath.Base(pipelineName)
	if name == "." || name == "" || name == NodesFile {
		return nil, e.New(e.CodeUsage, "pipeline output name %q collides with the execution bundle", pipelineName)
	}
	manifest := BundleManifest{
		SchemaVersion: BundleManifestSchema, Engine: engine, RecipeID: recipeID,
		RecipeDigest: recipeDigest, BundleDigest: bundleDigest,
		Members: []BundleMember{
			{Name: name, MediaType: "application/yaml", SHA256: FileDigest(emitted), Mode: 0o644},
			{Name: NodesFile, MediaType: "application/vnd.nkd.ir+json", SHA256: FileDigest(nodes), Mode: 0o644},
			{Name: TransportPlanFile, MediaType: "application/vnd.nkd.transport-plan+json", SHA256: FileDigest(transportPlan), Mode: 0o644},
		},
	}
	return canonical.Marshal(manifest)
}

// VerifyManifest requires exact canonical manifest bytes and exact member bytes.
func VerifyManifest(manifestBytes []byte, engine, recipeID, recipeDigest, pipelineName string, emitted, nodes, transportPlan []byte) error {
	want, err := ManifestJSON(engine, recipeID, recipeDigest, pipelineName, emitted, nodes, transportPlan)
	if err != nil {
		return err
	}
	if !bytes.Equal(manifestBytes, want) {
		return e.New(e.CodePipelineDrift, "execution bundle manifest or member identity has drifted")
	}
	return nil
}

func generatedHeaderValue(emitted []byte, key string) string {
	prefix := "# " + key + ":"
	for _, line := range strings.Split(string(emitted), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), prefix); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}
