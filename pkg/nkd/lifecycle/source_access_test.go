package lifecycle

import (
	"testing"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestEnvironmentSourceAccessPreservesBuildIdentity(t *testing.T) {
	definition := testDefinition(t)
	baseEnvironment := testEnvironment()
	basePlan, err := Compile(t.Context(), "definition.yaml", definition, "environment.yaml", baseEnvironment, "test")
	if err != nil {
		t.Fatal(err)
	}
	environment := testEnvironment()
	source := "https://github.com/kubernetes/kubernetes"
	environment.SourceAccess = map[string]SourceAccess{source: {
		Mirror: "https://github.com/example/kubernetes-mirror", CredentialRef: "github:SOURCE_TOKEN",
	}}
	plan, err := Compile(t.Context(), "definition.yaml", definition, "environment.yaml", environment, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Builds) != len(basePlan.Builds) {
		t.Fatal("source access changed selected producers")
	}
	for i, build := range plan.Builds {
		if build.Name != basePlan.Builds[i].Name || build.BuildDigest != basePlan.Builds[i].BuildDigest {
			t.Fatal("source access changed a logical Build identity")
		}
	}
	if got := plan.Environment.SourceAccess[source]; got.Mirror != "https://github.com/example/kubernetes-mirror" || got.CredentialRef != "github:SOURCE_TOKEN" {
		t.Fatalf("compiled source access = %+v", got)
	}
	if plan.DefinitionDigest != basePlan.DefinitionDigest || plan.EnvironmentDigest == basePlan.EnvironmentDigest {
		t.Fatal("source access did not remain an Environment-only identity change")
	}
}

func TestEnvironmentRejectsInvalidSourceAccess(t *testing.T) {
	tests := []struct {
		name   string
		source string
		access SourceAccess
	}{
		{name: "source scheme", source: "http://github.com/NVIDIA/nke", access: SourceAccess{Mirror: "https://github.com/example/nke-mirror", CredentialRef: "github:NKE_SOURCE_TOKEN"}},
		{name: "source userinfo", source: "https://token@github.com/NVIDIA/nke", access: SourceAccess{Mirror: "https://github.com/example/nke-mirror", CredentialRef: "github:NKE_SOURCE_TOKEN"}},
		{name: "source empty query", source: "https://github.com/NVIDIA/nke?", access: SourceAccess{Mirror: "https://github.com/example/nke-mirror", CredentialRef: "github:NKE_SOURCE_TOKEN"}},
		{name: "mirror fragment", source: "https://github.com/NVIDIA/nke", access: SourceAccess{Mirror: "https://github.com/example/nke-mirror#main", CredentialRef: "github:NKE_SOURCE_TOKEN"}},
		{name: "credential", source: "https://github.com/NVIDIA/nke", access: SourceAccess{Mirror: "https://github.com/example/nke-mirror", CredentialRef: "github:NKE-SOURCE-TOKEN"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment := testEnvironment()
			environment.SourceAccess = map[string]SourceAccess{test.source: test.access}
			if err := environment.Validate(); err == nil {
				t.Fatal("invalid source access accepted")
			} else {
				assertCode(t, err, nkderr.CodeInvalidCatalog)
			}
		})
	}
}
