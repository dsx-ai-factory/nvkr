package provenance

import (
	"encoding/json"
	"strings"
	"testing"

	nkderr "github.com/nhensley/nkdctl/pkg/nkd/errors"
)

func provenanceDigest(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }

func sampleArtifacts() []Artifact {
	return []Artifact{
		{Component: "kubelet", SHA256: provenanceDigest('a'), InputDigest: provenanceDigest('1'), BuilderType: "oci-hermetic",
			Deps: []Dependency{
				{URI: "golang@" + provenanceDigest('b')},
				{URI: "https://x/kubernetes", Digest: map[string]string{"gitCommit": strings.Repeat("c", 40)}},
			}},
		{Component: "etcd", SHA256: provenanceDigest('d'), InputDigest: provenanceDigest('2'), BuilderType: "nix",
			Deps: []Dependency{{URI: "nixpkgs@" + provenanceDigest('e')}}},
	}
}

// Times are omitted and builder.id is the profile digest — the provenance is
// reproducible, so its index digest is stable across runs (ADR-09 §1).
func TestProvenanceDeterministicAndTimesOmitted(t *testing.T) {
	ix1 := BuildIndex(provenanceDigest('f'), provenanceDigest('3'), sampleArtifacts())
	ix2 := BuildIndex(provenanceDigest('f'), provenanceDigest('3'), sampleArtifacts())
	d1, err := ix1.Digest()
	if err != nil {
		t.Fatal(err)
	}
	d2, _ := ix2.Digest()
	if d1 != d2 {
		t.Fatalf("provenance index digest not stable: %s vs %s", d1, d2)
	}
	b, _ := json.Marshal(ix1)
	s := string(b)
	if strings.Contains(s, "startedOn") || strings.Contains(s, "finishedOn") {
		t.Fatal("deterministic provenance must omit build times")
	}
	if !strings.Contains(s, `"id":"`+provenanceDigest('3')+`"`) {
		t.Fatalf("builder.id must be the profile digest:\n%s", s)
	}
	// statements sorted by subject name (etcd before kubelet)
	if ix1.Statements[0].Subject[0].Name != "etcd" {
		t.Fatalf("statements not sorted by subject: %s", ix1.Statements[0].Subject[0].Name)
	}
	// nix vs oci-hermetic pick different build-type URIs
	if ix1.Statements[0].Predicate.BuildDefinition.BuildType != BuildTypeNix {
		t.Fatalf("etcd (nix) build type wrong: %s", ix1.Statements[0].Predicate.BuildDefinition.BuildType)
	}
}

// A different profile changes the index digest (the builder is part of provenance).
func TestProvenanceProfileSensitive(t *testing.T) {
	a, err := BuildIndex(provenanceDigest('f'), provenanceDigest('4'), sampleArtifacts()).Digest()
	if err != nil {
		t.Fatal(err)
	}
	b, err := BuildIndex(provenanceDigest('f'), provenanceDigest('5'), sampleArtifacts()).Digest()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("provenance digest must change with the builder profile")
	}
}

func TestProvenanceValidationFailsClosed(t *testing.T) {
	valid := BuildIndex(provenanceDigest('f'), provenanceDigest('3'), sampleArtifacts())
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*Index){
		"malformed recipe digest": func(index *Index) { index.RecipeDigest = "sha256:recipe" },
		"duplicate subject": func(index *Index) {
			index.Statements = append(index.Statements, index.Statements[len(index.Statements)-1])
		},
		"unknown build type": func(index *Index) {
			index.Statements[0].Predicate.BuildDefinition.BuildType = "mystery"
		},
		"builder erasure": func(index *Index) {
			index.Statements[0].Predicate.RunDetails.Builder.ID = "none"
		},
		"input erasure": func(index *Index) {
			index.Statements[0].Predicate.BuildDefinition.ExternalParameters["inputDigest"] = ""
		},
		"mutable dependency": func(index *Index) {
			index.Statements[0].Predicate.BuildDefinition.ResolvedDependencies[0] = Dependency{URI: "nixpkgs:latest"}
		},
		"duplicate dependency": func(index *Index) {
			dependencies := index.Statements[0].Predicate.BuildDefinition.ResolvedDependencies
			index.Statements[0].Predicate.BuildDefinition.ResolvedDependencies = append(dependencies, dependencies[len(dependencies)-1])
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			index := BuildIndex(provenanceDigest('f'), provenanceDigest('3'), sampleArtifacts())
			mutate(index)
			if _, err := index.Digest(); err == nil {
				t.Fatal("invalid provenance acquired an identity")
			} else if coded, ok := err.(*nkderr.Error); !ok || coded.Code == "" {
				t.Fatalf("invalid provenance error is not coded: %v", err)
			}
		})
	}
}

func TestPrebuiltProvenanceIsExplicitlyProfileIndependent(t *testing.T) {
	index := BuildIndex(provenanceDigest('f'), "", []Artifact{{
		Component: "firmware", SHA256: provenanceDigest('a'), BuilderType: "",
		Deps: []Dependency{{URI: "registry.example/firmware@" + provenanceDigest('b')}},
	}})
	if err := index.Validate(); err != nil {
		t.Fatal(err)
	}
	index.Statements[0].Predicate.RunDetails.Builder.ID = provenanceDigest('3')
	if err := index.Validate(); err == nil {
		t.Fatal("prebuilt provenance must not claim a builder profile")
	}
}

func TestUnknownArtifactBuilderIsNotRelabeledPrebuilt(t *testing.T) {
	index := BuildIndex(provenanceDigest('f'), "", []Artifact{{
		Component: "artifact", SHA256: provenanceDigest('a'), BuilderType: "mystery",
		Deps: []Dependency{{URI: "registry.example/artifact@" + provenanceDigest('b')}},
	}})
	if err := index.Validate(); err == nil {
		t.Fatal("unknown builder type was silently relabeled as prebuilt")
	}
}
