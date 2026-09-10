package lifecycle

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nhensley/nkdctl/pkg/nkd/delivery"
	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
)

func TestInspectReleaseLayoutReturnsExactSortedObjects(t *testing.T) {
	layout, digest := testPublicationLayout(t)
	inspection, err := delivery.Inspect(t.Context(), layout)
	if err != nil {
		t.Fatal(err)
	}
	got, err := InspectReleaseLayout(t.Context(), "registry.example.test/product@"+digest, layout)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "nkd-lifecycle" || got[1].Name != "tools" {
		t.Fatalf("wrong inspection scope: %+v", got)
	}
	for _, artifact := range got {
		found := false
		for _, object := range inspection.Root.Objects {
			if object.Name == artifact.Name {
				found = artifact.Kind == object.Kind && artifact.Digest == object.BlobDigest
			}
		}
		if !found || artifact.RecipeDigest != "" {
			t.Fatalf("inspection fabricated artifact binding: %+v", artifact)
		}
	}
	for _, tc := range []struct {
		name, ref, path string
		code            e.Code
	}{
		{"wrong digest", "registry.example.test/product@sha256:" + repeat("a", 64), layout, e.CodeVerifyFailed},
		{"unpinned reference", "registry.example.test/product:latest", layout, e.CodeUnverifiedRef},
	} {
		t.Run(tc.name, func(t *testing.T) {
			artifacts, err := InspectReleaseLayout(t.Context(), tc.ref, tc.path)
			assertCode(t, err, tc.code)
			if artifacts != nil {
				t.Fatalf("failed inspection returned artifacts: %+v", artifacts)
			}
		})
	}
}

func TestConsumerMaterializationFailureCleansWorkspace(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(map[bool]string{false: "invalid reference cleanup", true: "root is file"}[blocked], func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "verification")
			if blocked {
				if err := os.WriteFile(root, []byte("preserve"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			consumer, err := NewConsumer(ConsumerOptions{WorkspaceRoot: root})
			if err != nil {
				t.Fatal(err)
			}
			materialized, err := consumer.materialize(t.Context(), "invalid")
			if err == nil || materialized.path != "" || materialized.cleanup != nil {
				t.Fatalf("failed materialization returned state: %+v, %v", materialized, err)
			}
			if blocked {
				assertCode(t, err, e.CodeImmutableViolation)
				raw, err := os.ReadFile(root)
				if err != nil || string(raw) != "preserve" {
					t.Fatalf("existing root changed: %q, %v", raw, err)
				}
			} else {
				entries, err := os.ReadDir(root)
				if err != nil || len(entries) != 0 {
					t.Fatalf("failed pull left staging: %v, %v", entries, err)
				}
			}
		})
	}
}

func TestInspectionRejectsInvalidObjectIdentity(t *testing.T) {
	for _, object := range []delivery.PublishedObject{
		{Name: "../escape", Kind: "binary-bundle", BlobDigest: "sha256:" + repeat("a", 64)},
		{Name: "tools", Kind: "binary-bundle", BlobDigest: "invalid"},
	} {
		inspection := delivery.Inspection{Root: delivery.PublicationRoot{Objects: []delivery.PublishedObject{object}}}
		got, err := artifactsFromInspection(inspection)
		assertCode(t, err, e.CodeVerifyFailed)
		if got != nil {
			t.Fatalf("invalid object returned artifacts: %+v", got)
		}
	}
}
