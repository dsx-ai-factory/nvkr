package sbom

import (
	"errors"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestInstallerFilesystemOwnerAdmission(t *testing.T) {
	for _, test := range []struct {
		name, filesystem, diagnostic string
		accepted                     bool
	}{
		{"iso", "iso9660\n", "", true},
		{"raw-ext4", "ext4\n", "", false},
		{"squashfs-is-not-iso", "squashfs\n", "", false},
		{"ambiguous", "iso9660\next4\n", "", false},
		{"missing", "", "", false},
		{"diagnostic", "iso9660\n", "warning", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateInstallerFilesystemType(test.filesystem, test.diagnostic)
			var coded *e.Error
			if test.accepted {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
				t.Fatalf("expected capability error, got %v", err)
			}
		})
	}
}
