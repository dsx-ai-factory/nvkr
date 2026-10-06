package sbom

import (
	"strconv"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// Modeled content observations exercise base-vs-staged semantics, not native
// ISO9660/SquashFS discovery or a complete original-owner evidence join.
func TestStagedInstallerCannotSubstituteDeclaredBase(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		for _, base := range []bool{false, true} {
			t.Run(architecture+"/base="+strconv.FormatBool(base), func(t *testing.T) {
				root := contentFixture("disk-image", architecture, "b")
				inputs := []*DiskContentInspection{contentFixture("installer-content", architecture, "c"), contentFixture("apt-acquisition", architecture, "d")}
				if base {
					inputs = append(inputs, contentFixture("installer-iso", architecture, "e"))
				}
				bom, err := ReconcileDiskContents(t.Context(), "sha256:"+strings.Repeat("f", 64), "node", root, inputs)
				if !base {
					assertDependencyCode(t, err, e.CodeProvenanceMissing)
					if bom != nil {
						t.Fatal("staged ISO supplied base authority")
					}
					return
				}
				if err != nil || bom == nil {
					t.Fatalf("declared base plus staged ISO rejected: %v", err)
				}
				for _, component := range bom.Components {
					if component.Type == "file" && !strings.Contains(component.Properties[0].Value, inputs[0].report.Subject) {
						t.Fatal("transitive ISO inventory disappeared")
					}
				}
			})
		}
	}
}
