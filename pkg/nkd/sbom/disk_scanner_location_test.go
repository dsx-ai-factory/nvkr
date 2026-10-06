package sbom

import (
	"encoding/json"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestFilesystemScannerRefusesUnownedNestedPackageDatabases(t *testing.T) {
	for _, test := range []struct {
		name, real, access string
		want               e.Code
	}{
		{"root database", "/var/lib/dpkg/status", "/var/lib/dpkg/status", ""},
		{"root fragment", "/var/lib/dpkg/status.d/fixture", "/var/lib/dpkg/status.d/fixture", ""},
		{"nested containerd", "/var/lib/containerd/snapshots/1/fs/var/lib/dpkg/status", "/var/lib/containerd/snapshots/1/fs/var/lib/dpkg/status", e.CodeEngineCapability},
		{"nested unpacked root", "/opt/root/var/lib/dpkg/status.d/fixture", "/opt/root/var/lib/dpkg/status.d/fixture", e.CodeEngineCapability},
		{"nested usr root", "/usr/lib/images/root/var/lib/dpkg/status", "/usr/lib/images/root/var/lib/dpkg/status", e.CodeEngineCapability},
		{"nested lib root", "/lib/images/root/var/lib/dpkg/status", "/lib/images/root/var/lib/dpkg/status", e.CodeEngineCapability},
		{"nested opkg", "/var/lib/images/root/var/lib/opkg/status", "/var/lib/images/root/var/lib/opkg/status", e.CodeEngineCapability},
		{"different real path", "/nested/var/lib/dpkg/status", "/var/lib/dpkg/status", e.CodeEngineCapability},
		{"different access path", "/var/lib/dpkg/status", "/nested/var/lib/dpkg/status", e.CodeEngineCapability},
		{"ordinary package archive", "/cache/package.deb", "/cache/package.deb", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := json.Marshal(map[string]any{"schema": map[string]string{"version": "16.1.10"}, "artifacts": []any{map[string]any{
				"name": "sample", "version": "1", "type": "deb", "metadata": map[string]string{"architecture": "all"},
				"locations": []any{map[string]string{"path": test.real, "accessPath": test.access, "layerID": "controlled-layer"}},
			}}})
			if err != nil {
				t.Fatal(err)
			}
			inventory, err := filesystemDependenciesFromJSON(raw, "sha256:"+strings.Repeat("a", 64))
			if test.want == "" {
				if err != nil || len(inventory.Components) != 1 {
					t.Fatalf("ordinary package location refused: %+v, %v", inventory, err)
				}
				return
			}
			assertDependencyCode(t, err, test.want)
			if len(inventory.Components) != 0 || !strings.Contains(err.Error(), "nested package database") {
				t.Fatalf("unsupported location returned partial/host inventory: %+v, %v", inventory, err)
			}
		})
	}
}
