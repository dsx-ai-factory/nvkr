package cluster

import (
	"testing"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func code(err error) nkderr.Code {
	if e, ok := err.(*nkderr.Error); ok {
		return e.Code
	}
	return ""
}

const rsDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func def(t string) *ClusterDefinition {
	return &ClusterDefinition{
		SchemaVersion: SchemaVersion, ClusterType: t, KubernetesVersion: "1.32.0",
		ReleaseSetRef: rsDigest,
		RoleBindings:  []RoleBinding{{Role: "control-plane", NodeLevelCategories: []string{"kubernetes", "os"}}},
	}
}

func TestValidate(t *testing.T) {
	if err := def(TypeOperator).Validate(); err != nil {
		t.Fatalf("operator cluster must validate: %v", err)
	}
	if err := def(TypeTenant).Validate(); err != nil {
		t.Fatalf("tenant cluster must validate: %v", err)
	}
	bad := def("mystery")
	if code(bad.Validate()) != nkderr.CodeUnsupported {
		t.Fatal("unknown clusterType must fail closed")
	}
	notDigest := def(TypeTenant)
	notDigest.ReleaseSetRef = "v1.0-tag"
	if code(notDigest.Validate()) != nkderr.CodeUnverifiedRef {
		t.Fatal("non-digest releaseSetRef must fail closed")
	}
	noRB := def(TypeTenant)
	noRB.RoleBindings = nil
	if code(noRB.Validate()) != nkderr.CodeInvalidCatalog {
		t.Fatal("no role bindings must fail closed")
	}
	wrongSchema := def(TypeTenant)
	wrongSchema.SchemaVersion = "nkd.dev/cluster-definition/v2"
	if code(wrongSchema.Validate()) != nkderr.CodeInvalidCatalog {
		t.Fatal("unknown schema must fail closed before identity dispatch")
	}
	uppercase := def(TypeTenant)
	uppercase.ReleaseSetRef = "sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if code(uppercase.Validate()) != nkderr.CodeUnverifiedRef {
		t.Fatal("uppercase digest must fail closed")
	}
	duplicate := def(TypeTenant)
	duplicate.RoleBindings = append(duplicate.RoleBindings, duplicate.RoleBindings[0])
	if code(duplicate.Validate()) != nkderr.CodeInvalidCatalog {
		t.Fatal("duplicate role binding must fail closed")
	}
}

// The definition is deterministic and digest-addressable, and the
// recursion holds: operator and tenant definitions have distinct identities.
func TestDigestDeterministicAndRecursive(t *testing.T) {
	a, err := def(TypeOperator).Digest()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := def(TypeOperator).Digest()
	if a != b {
		t.Fatal("cluster definition digest not deterministic")
	}
	tenant, _ := def(TypeTenant).Digest()
	if tenant == a {
		t.Fatal("operator and tenant definitions must have distinct identities (the recursion)")
	}
	unsorted := def(TypeTenant)
	unsorted.RoleBindings[0].NodeLevelCategories = []string{"os", "kubernetes"}
	if _, err := unsorted.Digest(); code(err) != nkderr.CodeInvalidCatalog {
		t.Fatalf("non-canonical order must not silently normalize into an identity, got %v", err)
	}
	unsorted.Normalize()
	bytes, err := unsorted.Marshal()
	if err != nil || len(bytes) == 0 {
		t.Fatalf("normalized definition did not produce canonical bytes: %v", err)
	}
}
