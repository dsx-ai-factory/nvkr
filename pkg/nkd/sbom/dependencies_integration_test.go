package sbom

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nhensley/nkdctl/pkg/nkd/catalog"
)

func TestRealArtifactDependencies(t *testing.T) {
	dir := os.Getenv("NKD_SBOM_ARTIFACT_DIR")
	if dir == "" {
		t.Skip("set NKD_SBOM_ARTIFACT_DIR to exercise downloaded real build outputs")
	}
	scanner := Scanner{Path: os.Getenv("NKD_SBOM_SCANNER"), Digest: os.Getenv("NKD_SBOM_SCANNER_DIGEST")}
	for _, tc := range []struct{ name, file, kind, prefix string }{
		{"nkectl", "nkectl", "host-package", "pkg:golang/"},
		{"cwe", "cwe.oci.tar", "oci-image", "pkg:apk/"},
		{"nke-helm", "nke-helm.tar", "tarball", "pkg:helm/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(dir, tc.file)
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			digest := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
			inventory, err := InspectDependencies(t.Context(), file, digest, tc.kind, scanner)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, component := range inventory.Components {
				if strings.HasPrefix(component.PURL, tc.prefix) {
					found = true
				}
			}
			if !found {
				t.Fatalf("no %s dependencies among %d components", tc.prefix, len(inventory.Components))
			}
			component := catalog.NamedComponent{Name: tc.name}
			component.FromSource = true
			component.OutputType = tc.kind
			component.Repo = "https://example.test/integration-fixture"
			component.Commit = strings.Repeat("a", 40)
			component.TreeHash = strings.Repeat("b", 40)
			component.ToolchainImage = "fixture@sha256:" + strings.Repeat("c", 64)
			bom := FromComponent(component, "sha256:"+strings.Repeat("a", 64), digest)
			populated, err := WithDependencies(bom, inventory.Components, inventory.Method, inventory.ScannerDigest)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := populated.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			if out := os.Getenv("NKD_SBOM_OUTPUT_DIR"); out != "" {
				if err := os.WriteFile(filepath.Join(out, tc.name+".cdx.json"), encoded, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			t.Logf("%s: %d unique dependency components; subject %s", tc.name, len(populated.Components), digest)
		})
	}
}
