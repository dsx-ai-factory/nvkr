package catalog

import (
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestDigestQualifiedPrebuiltImageRejectedBeforeUpstream(t *testing.T) {
	for _, ref := range []string{
		"registry.example/tool@" + goodArtifactDigest,
		"registry.example/tool:v1@" + goodArtifactDigest,
		"oci://registry.example/tool@" + goodArtifactDigest,
		"registry.example/tool:{{.Version}}@" + goodArtifactDigest,
	} {
		t.Run(ref, func(t *testing.T) {
			raw := rawComponent{Platforms: []string{"linux/amd64"}, Source: "s", OutputType: "oci-image", Prebuilt: &Prebuilt{Image: ref, Evidence: &ExternalEvidence{None: true}}}
			reg := &registryDoc{Sources: map[string]Source{"s": {Repo: "https://example.invalid/source"}}, Components: map[string]rawComponent{"tool": raw}}
			if err := validateRegistryDeclarations(reg); codeOf(err) != e.CodeUnverifiedRef {
				t.Fatalf("raw reference admission: %v", err)
			}
			component := Component{Platforms: raw.Platforms, OutputType: raw.OutputType, Prebuilt: &Prebuilt{Image: ref, Digests: map[string]string{"amd64": goodArtifactDigest}, Evidence: raw.Prebuilt.Evidence}}
			if err := base(component).Validate(); codeOf(err) != e.CodeUnverifiedRef {
				t.Fatalf("resolved reference admission: %v", err)
			}
			if _, err := prebuiltTargetFor("tool", raw, Pin{Tag: "v1.2.3"}); codeOf(err) != e.CodeUnverifiedRef {
				t.Fatalf("maintenance target admission: %v", err)
			}
			_, err := resolvePrebuiltTarget(t.Context(), PrebuiltTarget{Name: "tool", Image: ref, Platforms: raw.Platforms}, "/nonexistent/nkd-no-upstream")
			if codeOf(err) != e.CodeUnverifiedRef {
				t.Fatalf("pin reached upstream rather than reference admission: %v", err)
			}
			_, err = resolveImageDigests(t.Context(), "/nonexistent/nkd-no-upstream", ref, []string{"amd64"})
			if codeOf(err) != e.CodeUnverifiedRef {
				t.Fatalf("image resolver reached upstream: %v", err)
			}
		})
	}
}

func TestPrebuiltImageTagAndTemplateReferenceAdmission(t *testing.T) {
	for _, ref := range []string{"registry.example/tool:v1", "oci://registry.example/tool:v1", "registry.example/tool:{{.Version}}"} {
		t.Run(ref, func(t *testing.T) {
			if err := validateAcquiredImageReference("tool", ref); err != nil {
				t.Fatal(err)
			}
		})
	}
}
