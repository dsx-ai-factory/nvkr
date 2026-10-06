package sbom

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
)

func TestDirectOCIMaterialAuthenticatesClosureBeforeScanner(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		foreign := map[string]string{"amd64": "arm64", "arm64": "amd64"}[architecture]
		for _, test := range []struct {
			name   string
			images []string
			change func(string)
			code   e.Code
			scans  int
		}{
			{"native", []string{architecture}, nil, "", 1},
			{"multiple native images", []string{architecture, architecture}, nil, "", 2},
			{"foreign config", []string{foreign}, nil, e.CodeVerifyFailed, 0},
			{"mixed native and foreign", []string{architecture, foreign}, nil, e.CodeVerifyFailed, 0},
			{"missing blob", []string{architecture}, func(root string) {
				entries, err := os.ReadDir(filepath.Join(root, "blobs", "sha256"))
				if err != nil || len(entries) == 0 {
					t.Fatalf("fixture has no blob: %v", err)
				}
				if err := os.Remove(filepath.Join(root, "blobs", "sha256", entries[0].Name())); err != nil {
					t.Fatal(err)
				}
			}, e.CodeVerifyFailed, 0},
			{"corrupt blob", []string{architecture}, func(root string) {
				entries, err := os.ReadDir(filepath.Join(root, "blobs", "sha256"))
				if err != nil || len(entries) == 0 {
					t.Fatalf("fixture has no blob: %v", err)
				}
				if err := os.WriteFile(filepath.Join(root, "blobs", "sha256", entries[0].Name()), []byte("corrupt"), 0o644); err != nil {
					t.Fatal(err)
				}
			}, e.CodeVerifyFailed, 0},
			{"unreferenced blob", []string{architecture}, func(root string) {
				payload := []byte("unreferenced OCI blob")
				if err := os.WriteFile(filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(rawDigest(payload), "sha256:")), payload, 0o644); err != nil {
					t.Fatal(err)
				}
			}, e.CodeVerifyFailed, 0},
			{"mixed transport markers", []string{architecture}, nil, e.CodeVerifyFailed, 0},
		} {
			t.Run(architecture+"/"+test.name, func(t *testing.T) {
				if test.scans > 0 && runtime.GOOS != "linux" {
					t.Skip("controlled held scanner execution requires Linux; rejection cases still run")
				}
				archive := nestedOCIFixture(t, test.images, false, test.change)
				if test.name == "mixed transport markers" {
					// The fixture's standard packer selects only OCI members. Add
					// the Docker marker using the existing tar fixture owner.
					archive = compressedMaterialFixture(t, map[string]string{"oci-layout": `{"imageLayoutVersion":"1.0.0"}`, "manifest.json": "[]"})
				}
				subject, err := heldfile.FromReaderContext(t.Context(), io.NewSectionReader(archive, 0, archive.Size()), heldfile.Options{Label: "direct-oci-closure", MaxBytes: 1 << 20})
				if err != nil {
					t.Fatal(err)
				}
				defer subject.Close()
				// The marker counts actual scans. Rejection must precede every
				// child, and multiple native members must all reach the scanner.
				marker := filepath.Join(t.TempDir(), "calls")
				script := []byte("#!/bin/sh\nprintf x >> '" + marker + "'\nprintf '%s' '{\"schema\":{\"version\":\"16.1.10\"},\"artifacts\":[{\"name\":\"observed\",\"version\":\"1\",\"type\":\"deb\",\"metadata\":{\"architecture\":\"all\"}}]}'\n")
				filename := filepath.Join(t.TempDir(), "scanner")
				if err := os.WriteFile(filename, script, 0o755); err != nil {
					t.Fatal(err)
				}
				components, err := inspectContainerMaterial(t.Context(), subject, "linux/"+architecture, Scanner{Path: filename, Digest: rawDigest(script)}, 1<<20)
				if test.code == "" {
					if err != nil || len(components) != test.scans {
						t.Fatalf("complete native inventory: components=%d error=%v", len(components), err)
					}
				} else {
					assertDependencyCode(t, err, test.code)
				}
				if test.code != "" && components != nil {
					t.Fatal("failed material admission returned partial inventory")
				}
				calls, readErr := os.ReadFile(marker)
				if readErr != nil && !os.IsNotExist(readErr) {
					t.Fatal(readErr)
				}
				if len(calls) != test.scans {
					t.Fatalf("scanner calls=%d want=%d", len(calls), test.scans)
				}
				for _, component := range components {
					class, err := diskComponentClass(component)
					if err != nil || class != diskClassContainer {
						t.Fatalf("OCI observation class=%q error=%v", class, err)
					}
				}
			})
		}
	}
}
