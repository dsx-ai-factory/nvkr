package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestBuildBundleManifestRejectsLegacyIdentityAndMemberDrift(t *testing.T) {
	graph := prebuiltEmitterGraph()
	digest := graphDigest(t, graph)
	emitted, err := testEmitters("catalog")[0].Render(context.Background(), graph, digest)
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := NodesJSON(graph)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := TransportPlanJSON(graph)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := ManifestJSON("gitlab-ci", digest, "pipeline.yml", emitted, nodes, transport)
	if err != nil {
		t.Fatal(err)
	}
	var manifest BundleManifest
	if err := canonical.UnmarshalExact(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != "nkd.dev/execution-bundle-manifest/v2" || manifest.BuildDigest != digest || strings.Contains(string(raw), "recipe") {
		t.Fatalf("manifest retains removed identity: %s", raw)
	}
	if err := VerifyManifest(raw, "gitlab-ci", digest, "pipeline.yml", emitted, nodes, transport); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name                       string
		manifest, nodes, transport []byte
	}{
		{"old schema", []byte(strings.Replace(string(raw), "/v2", "/v1", 1)), nodes, transport},
		{"old field", []byte(strings.Replace(string(raw), "buildDigest", "recipeDigest", 1)), nodes, transport},
		{"changed graph", raw, append(append([]byte(nil), nodes...), ' '), transport},
		{"changed transport", raw, nodes, append(append([]byte(nil), transport...), ' ')},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := VerifyManifest(test.manifest, "gitlab-ci", digest, "pipeline.yml", emitted, test.nodes, test.transport)
			if code(err) != e.CodePipelineDrift {
				t.Fatalf("drift error = %v, want %s", err, e.CodePipelineDrift)
			}
		})
	}
}
