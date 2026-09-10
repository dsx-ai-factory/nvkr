package release

import (
	"bytes"
	"reflect"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestAuthorizationProjectionBindsExactDocuments(t *testing.T) {
	inv := inventory()
	set := releaseSet(t, inv)
	request := authorizationRequest(t, inv, set)
	authorization, err := Authorize(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	setDigest, err := set.Digest()
	if err != nil {
		t.Fatal(err)
	}
	invDigest, err := inv.Digest()
	if err != nil {
		t.Fatal(err)
	}
	publication, err := set.PublicationIdentity()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		get  func() (string, error)
		want string
	}{
		{"release set", authorization.ReleaseSetDigest, setDigest},
		{"inventory", authorization.InventoryDigest, invDigest},
		{"publication", authorization.PublicationIdentity, publication},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := tc.get(); err != nil || got != tc.want {
				t.Fatalf("binding = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	raw, err := authorization.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	expected := append([]byte(nil), raw...)
	raw[0] = '!'
	again, err := authorization.Marshal()
	if err != nil || !bytes.Equal(again, expected) {
		t.Fatalf("caller changed clearance bytes: %v", err)
	}
	evidence, err := authorization.EvidenceDigests()
	if err != nil || len(evidence) == 0 {
		t.Fatalf("evidence = %v, %v", evidence, err)
	}
	wantEvidence := append([]string(nil), evidence...)
	evidence[0] = digest('f')
	againEvidence, err := authorization.EvidenceDigests()
	if err != nil || !reflect.DeepEqual(againEvidence, wantEvidence) {
		t.Fatalf("caller changed evidence membership: %v, %v", againEvidence, err)
	}
}

func TestAbsentAuthorizationCannotExposeBindings(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value *Authorization
	}{{"nil", nil}, {"zero", &Authorization{}}} {
		t.Run(tc.name, func(t *testing.T) {
			for _, get := range []func() (string, error){tc.value.ReleaseSetDigest, tc.value.InventoryDigest, tc.value.PublicationIdentity, tc.value.Digest} {
				if got, err := get(); got != "" || code(err) != e.CodeGateUnmet {
					t.Fatalf("absent authorization returned %q, %v", got, err)
				}
			}
			if got, err := tc.value.EvidenceDigests(); got != nil || code(err) != e.CodeGateUnmet {
				t.Fatalf("absent evidence = %v, %v", got, err)
			}
			if got, err := tc.value.Marshal(); got != nil || code(err) != e.CodeGateUnmet {
				t.Fatalf("absent bytes = %q, %v", got, err)
			}
		})
	}
}
