package lifecycle

import (
	"reflect"
	"testing"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestEnvironmentSourceAccessValidationAndLowering(t *testing.T) {
	definition := testDefinition(t)
	baseEnvironment := testEnvironment()
	baseCompileSpec, _, err := lower(definition, baseEnvironment)
	if err != nil {
		t.Fatal(err)
	}
	baseRecipeDigest, err := definition.Digest()
	if err != nil {
		t.Fatal(err)
	}

	environment := testEnvironment()
	source := "https://github.com/NVIDIA/nke"
	environment.SourceAccess = map[string]SourceAccess{source: {
		Mirror: "https://github.com/njhensley/nke", CredentialRef: "github:NKE_SOURCE_TOKEN",
	}}
	if err := environment.Validate(); err != nil {
		t.Fatal(err)
	}
	compileSpec, settings, err := lower(definition, environment)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(compileSpec, baseCompileSpec) {
		t.Fatal("source access changed the Recipe-affecting compile spec")
	}
	if got := settings.SourceAccess[source]; got.Mirror != "https://github.com/njhensley/nke" || got.CredentialRef != "github:NKE_SOURCE_TOKEN" {
		t.Fatalf("lowered source access = %+v", got)
	}
	if digest, err := definition.Digest(); err != nil || digest != baseRecipeDigest {
		t.Fatalf("source access changed Definition identity: %q, %v", digest, err)
	}
}

func TestEnvironmentRejectsInvalidSourceAccess(t *testing.T) {
	tests := []struct {
		name   string
		source string
		access SourceAccess
	}{
		{name: "source scheme", source: "http://github.com/NVIDIA/nke", access: SourceAccess{Mirror: "https://github.com/njhensley/nke", CredentialRef: "github:NKE_SOURCE_TOKEN"}},
		{name: "source userinfo", source: "https://token@github.com/NVIDIA/nke", access: SourceAccess{Mirror: "https://github.com/njhensley/nke", CredentialRef: "github:NKE_SOURCE_TOKEN"}},
		{name: "source empty query", source: "https://github.com/NVIDIA/nke?", access: SourceAccess{Mirror: "https://github.com/njhensley/nke", CredentialRef: "github:NKE_SOURCE_TOKEN"}},
		{name: "mirror fragment", source: "https://github.com/NVIDIA/nke", access: SourceAccess{Mirror: "https://github.com/njhensley/nke#main", CredentialRef: "github:NKE_SOURCE_TOKEN"}},
		{name: "credential", source: "https://github.com/NVIDIA/nke", access: SourceAccess{Mirror: "https://github.com/njhensley/nke", CredentialRef: "github:NKE-SOURCE-TOKEN"}},
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
