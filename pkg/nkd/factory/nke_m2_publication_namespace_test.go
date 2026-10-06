package factory

import (
	"reflect"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	"github.com/google/go-containerregistry/pkg/name"
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
	if candidate.Publication.CredentialRef != production.Publication.CredentialRef || !reflect.DeepEqual(candidate.Signing, production.Signing) {
		t.Fatal("namespace separation invented a different credential or approved signer identity")
	}
	for _, output := range []string{"releases", "cwe", "nkectl"} {
		ref, err := name.NewRepository(prefix + "/" + output)
		if err != nil || ref.Name() != prefix+"/"+output {
			t.Fatalf("candidate route %s: %v", output, err)
		}
	}
	if len(candidate.ExecutionClasses) != 0 || len(candidate.DiskInspectionProfiles) != 0 {
		t.Fatal("unmeasured native admission values were fabricated")
	}
}
