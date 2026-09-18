package release

import (
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestReleaseWireRejectsRemovedDeploymentRoles(t *testing.T) {
	for _, test := range []struct {
		name  string
		value map[string]any
		into  any
	}{
		{"inventory", map[string]any{"schemaVersion": InventorySchemaVersion, "components": []any{map[string]any{"name": "node", "role": "worker"}}}, new(Inventory)},
		{"release set", map[string]any{"schemaVersion": ReleaseSetSchemaVersion, "members": []any{map[string]any{"component": "node", "role": "worker"}}}, new(ReleaseSet)},
		{"reproduction", map[string]any{"schemaVersion": ReproductionSchemaVersion, "role": "worker"}, new(ReproductionResult)},
		{"waiver", map[string]any{"schemaVersion": EvidenceWaiverSchemaVersion, "role": "worker"}, new(evidenceWaiverDocument)},
		{"clearance member", map[string]any{"schemaVersion": AuthorizationSchemaVersion, "members": []any{map[string]any{"role": "worker"}}}, new(authorizationRecord)},
		{"clearance evidence", map[string]any{"schemaVersion": AuthorizationSchemaVersion, "evidence": []any{map[string]any{"role": "worker"}}}, new(authorizationRecord)},
		{"clearance waiver", map[string]any{"schemaVersion": AuthorizationSchemaVersion, "waivers": []any{map[string]any{"role": "worker"}}}, new(authorizationRecord)},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := canonical.Marshal(test.value)
			if err != nil {
				t.Fatal(err)
			}
			if err := canonical.UnmarshalExact(raw, test.into); code(err) != e.CodeVerifyFailed {
				t.Fatalf("removed deployment role accepted: %v", err)
			}
		})
	}
}

func TestReleaseWireRejectsEarlierTopologySchemas(t *testing.T) {
	inv := inventory()
	set := releaseSet(t, inv)
	for _, test := range []struct {
		name  string
		check func() error
	}{
		{"inventory", func() error { old := *inv; old.SchemaVersion = "nkd.dev/inventory/v3"; return old.Validate() }},
		{"release set", func() error { old := *set; old.SchemaVersion = "nkd.dev/release-set/v3"; return old.Validate() }},
		{"reproduction", func() error { return (ReproductionResult{SchemaVersion: "nkd.dev/reproduction-result/v2"}).Validate() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.check(); code(err) != e.CodeInvalidCatalog {
				t.Fatalf("earlier topology schema accepted: %v", err)
			}
		})
	}
}
