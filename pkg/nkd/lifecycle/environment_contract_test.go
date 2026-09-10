package lifecycle

import (
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestReleaseDescriptionRejectsAmbiguousArtifactOrder(t *testing.T) {
	for _, kind := range []string{"valid", "reversed", "duplicate", "invalid artifact", "unpinned subject"} {
		t.Run(kind, func(t *testing.T) {
			digest := "sha256:" + repeat("a", 64)
			r := Release{SchemaVersion: ReleaseSchemaVersion, Lane: LanePipeline, Scope: ScopeArtifactPublication, PolicyDigest: digest, ContentSubject: digest, Subject: "registry.example/product@" + digest,
				Artifacts: []Artifact{{Name: "a", Kind: OutputExecutable, Digest: digest}, {Name: "b", Kind: OutputExecutable, Digest: digest}}}
			want := e.CodeVerifyFailed
			switch kind {
			case "reversed":
				r.Artifacts[0], r.Artifacts[1] = r.Artifacts[1], r.Artifacts[0]
			case "duplicate":
				r.Artifacts[1] = r.Artifacts[0]
			case "invalid artifact":
				r.Artifacts[0].Digest = "invalid"
			case "unpinned subject":
				r.Subject = "registry.example/product:latest"
				want = e.CodeUnverifiedRef
			}
			err := r.validate()
			if kind == "valid" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertCode(t, err, want)
			}
		})
	}
}

func TestEnvironmentRejectsInvalidExecutionAndPublicationInputs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Environment)
		want   e.Code
	}{
		{"schema", func(v *Environment) { v.APIVersion = "future" }, e.CodeInvalidCatalog},
		{"platform", func(v *Environment) { v.UtilityPlatform = "windows/amd64" }, e.CodeUnpinnedSource},
		{"mutable utility", func(v *Environment) { v.UtilityContainer = "golang:latest" }, e.CodeUnpinnedSource},
		{"runner newline", func(v *Environment) { v.RunsOn = "ubuntu\nother" }, e.CodeInvalidCatalog},
		{"tagged prefix", func(v *Environment) { v.Publication.RepositoryPrefix = "registry.example.test/product:latest" }, e.CodeInvalidCatalog},
		{"ambiguous credentials", func(v *Environment) { v.Publication.CredentialRef = "github:TOKEN" }, e.CodeInvalidCatalog},
		{"digest prefix", func(v *Environment) {
			v.Publication.RepositoryPrefix = "registry.example.test/product@sha256:" + repeat("a", 64)
		}, e.CodeInvalidCatalog},
		{"unknown signing", func(v *Environment) { v.Signing = &Signing{Mode: "unknown"} }, e.CodeUnsupported},
		{"missing signer identity", func(v *Environment) { v.Signing = &Signing{Mode: "cosign"} }, e.CodeTrustPolicyMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := testEnvironment()
			tc.change(&env)
			assertCode(t, env.Validate(), tc.want)
			digest, err := env.Digest()
			assertCode(t, err, tc.want)
			if digest != "" {
				t.Fatal("invalid environment received identity")
			}
		})
	}
}
