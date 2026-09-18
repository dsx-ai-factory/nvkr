package buildgraph

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestGoldenBuildIdentities pins explicit producer graphs across supported targets.
// An ordinary run never changes expected bytes; regenerate only after reviewing
// intentional graph or pin changes with NKD_UPDATE_GOLDEN=1.
func TestGoldenBuildIdentities(t *testing.T) {
	cat := kubernetesFixture(t)
	type vector struct {
		Platform   string   `json:"platform"`
		Components []string `json:"components"`
		Digest     string   `json:"buildDigest"`
	}
	var vectors []vector
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		selection := Selection{Components: fixtureProducers(), Platform: platform}
		resolved, err := Resolve(cat, selection)
		if err != nil {
			t.Fatal(err)
		}
		digest, err := mustCompile(t, resolved).Digest()
		if err != nil {
			t.Fatal(err)
		}
		vectors = append(vectors, vector{Platform: platform, Components: selection.Components, Digest: digest})
	}
	actual, err := json.MarshalIndent(vectors, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	actual = append(actual, '\n')
	path := filepath.Join("testdata", "golden_builds.json")
	if os.Getenv("NKD_UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, actual, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	expected, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(actual) != string(expected) {
		t.Fatalf("explicit producer build identities drifted; review changes before regenerating with NKD_UPDATE_GOLDEN=1\n%s", actual)
	}
}
