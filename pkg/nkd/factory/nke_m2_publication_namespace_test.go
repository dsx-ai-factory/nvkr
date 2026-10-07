package factory

import (
	"regexp"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
)

func TestMaintainedM2CandidatePublicationNamespace(t *testing.T) {
	candidate, err := definition.LoadEnvironment(t.Context(), "../../../definitions/nke-m2/environment-github.yaml")
	if err != nil {
		t.Fatal(err)
	}
	production, err := definition.LoadEnvironment(t.Context(), "../../../definitions/nke-v1/environment-github.yaml")
	if err != nil {
		t.Fatal(err)
	}
	const prefix = "ghcr.io/dsx-ai-factory/nke-recipes/m2-candidate"
	if candidate.Publication.RepositoryPrefix != prefix || candidate.Publication.RepositoryPrefix == production.Publication.RepositoryPrefix {
		t.Fatal("candidate namespace overlaps production outputs")
	}
	if candidate.Publication.CredentialRef != production.Publication.CredentialRef || candidate.Signing.Mode != production.Signing.Mode || candidate.Signing.Issuer != production.Signing.Issuer {
		t.Fatal("candidate changed registry credential or signing mode/issuer")
	}
	// The maintained Environment must not carry a candidate-test branch into main.
	if len(candidate.GitHubDispatchBranches) != 0 {
		t.Fatal("maintained candidate execution must remain default-branch-only")
	}
	identity := regexp.MustCompile(candidate.Signing.Identity)
	for _, test := range []struct {
		ref     string
		allowed bool
	}{
		{"refs/heads/main", true},
		{"refs/heads/codex/m2-native-arc-foundation", false},
		{"refs/heads/codex/m2-native-arc-foundation-other", false},
		{"refs/heads/codex/m2-arc-validation", false},
		{"refs/heads/codex/m2-arc-validation-other", false},
		{"refs/heads/feat/m2-native-arc", false},
		{"refs/heads/feat/nke-deploy-pipeline", false},
		{"refs/heads/main-other", false},
		{"refs/heads/other", false},
		{"refs/tags/codex/m2-arc-validation", false},
		{"refs/tags/codex/m2-native-arc-foundation", false},
		{"refs/pull/1/merge", false},
	} {
		if got := identity.MatchString("https://github.com/dsx-ai-factory/nke-recipes/.github/workflows/nkd-run.yml@" + test.ref); got != test.allowed {
			t.Fatalf("signing ref %s: allowed=%v", test.ref, got)
		}
	}
	if candidate.GitHubARC != "native-kvm-v1" || len(candidate.ExecutionClasses) != 2 || len(candidate.DiskInspectionProfiles) != 2 {
		t.Fatal("candidate must retain both native ARC routes and inspection profiles")
	}
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		profile, ok := candidate.DiskInspectionProfiles[platform]
		if !ok {
			t.Fatalf("missing native inspection profile %s", platform)
		}
		if err := profile.Validate(platform); err != nil {
			t.Fatal(err)
		}
	}
}
