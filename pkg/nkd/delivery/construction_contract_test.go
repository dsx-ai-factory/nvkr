package delivery

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestPublicationConstructionFailureCleansStaging(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*PublicationRequest)
		code e.Code
	}{
		{"empty objects", func(r *PublicationRequest) { r.Objects = nil }, e.CodeUsage},
		{"partial release binding", func(r *PublicationRequest) { r.InventoryDigest = digestBytes([]byte("inventory")) }, e.CodeUnverifiedRef},
		{"bad cluster binding", func(r *PublicationRequest) { r.ClusterDefinitionDigest = "latest" }, e.CodeUnverifiedRef},
		{"duplicate object", func(r *PublicationRequest) { r.Objects = append(r.Objects, r.Objects[0]) }, e.CodeInvalidCatalog},
		{"missing semantic pin", func(r *PublicationRequest) { r.Objects[0].SemanticDigest = "" }, e.CodeUnverifiedRef},
		{"wrong byte pin", func(r *PublicationRequest) { r.Objects[0].BlobDigest = digestBytes([]byte("different")) }, e.CodeVerifyFailed},
		{"missing file", func(r *PublicationRequest) { r.Objects[0].Bytes = nil; r.Objects[0].Path = "missing-file" }, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.TempDir()
			request := PublicationRequest{Objects: []ObjectInput{{Name: "record", Kind: "record", Bytes: []byte("payload"), SemanticDigest: digestBytes([]byte("payload"))}}}
			tc.edit(&request)
			result, err := BuildPublicationRoot(t.Context(), filepath.Join(parent, "publication"), request)
			assertRegistryCode(t, err, tc.code)
			if !reflect.DeepEqual(result, Result{}) {
				t.Fatalf("failure returned publication identity: %+v", result)
			}
			entries, err := os.ReadDir(parent)
			if err != nil || len(entries) != 0 {
				t.Fatalf("failed construction leaked state: %v, %v", entries, err)
			}
		})
	}
}

func TestBinaryBundleCaptureRejectsMissingAndSymlinkedMembers(t *testing.T) {
	for _, tc := range []struct {
		name               string
		duplicate, symlink bool
		code               e.Code
	}{
		{"missing", false, false, e.CodeVerifyFailed},
		{"symlink", false, true, e.CodeVerifyFailed},
		{"duplicate", true, false, e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "tool")
			if err := os.WriteFile(source, []byte("tool"), 0o755); err != nil {
				t.Fatal(err)
			}
			second := source + ".missing"
			if tc.symlink {
				if err := os.Symlink(source, second); err != nil {
					t.Fatal(err)
				}
			}
			files := []BinaryFileInput{{Name: "a", Path: source, InstallPath: "/usr/bin/a"}, {Name: "b", Path: second, InstallPath: "/usr/bin/b"}}
			if tc.duplicate {
				files[1] = files[0]
			}
			parent := t.TempDir()
			result, err := BuildBinaryBundle(t.Context(), filepath.Join(parent, "bundle"), BinaryBundleRequest{OS: "linux", Arch: "amd64", Purpose: "tools", Files: files})
			assertRegistryCode(t, err, tc.code)
			if !reflect.DeepEqual(result, Result{}) {
				t.Fatalf("failed capture returned identity: %+v", result)
			}
			entries, err := os.ReadDir(parent)
			if err != nil || len(entries) != 0 {
				t.Fatalf("failed capture created output: %v, %v", entries, err)
			}
		})
	}
}
