// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
)

func TestBinaryBundlePublicationRootAndExtraction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	first := filepath.Join(root, "kubectl")
	second := filepath.Join(root, "nkdctl")
	if err := os.WriteFile(first, []byte("kubectl-exact\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("nkdctl-exact\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(root, "bundle")
	bundle, err := BuildBinaryBundle(ctx, bundlePath, BinaryBundleRequest{OS: "linux", Arch: "amd64",
		Purpose: "operator-tools", Epoch: 1_748_736_000, Files: []BinaryFileInput{
			{Name: "nkdctl", Path: second, InstallPath: "/usr/local/bin/nkdctl", Mode: 0o755},
			{Name: "kubectl", Path: first, InstallPath: "/usr/local/bin/kubectl", Mode: 0o755},
		}})
	if err != nil {
		t.Fatal(err)
	}
	definition := filepath.Join(root, "build-record.json")
	definitionRaw := []byte(`{"schemaVersion":"example/v1","release":"exact"}`)
	if err := os.WriteFile(definition, definitionRaw, 0o644); err != nil {
		t.Fatal(err)
	}
	publicationPath := filepath.Join(root, "publication")
	publication, err := BuildPublicationRoot(ctx, publicationPath, PublicationRequest{Objects: []ObjectInput{
		{Name: "build-record", Kind: "build-record", Path: definition,
			ArtifactType: MediaDeliveryObject, LayerMediaType: MediaDeliveryObject, SemanticDigest: digestBytes(definitionRaw)},
		{Name: "operator-tools-linux-amd64", Kind: "binary-bundle", Path: bundlePath,
			ArtifactType: MediaBinaryBundle, SemanticDigest: bundle.SemanticDigest,
			OS: "linux", Arch: "amd64", Layout: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := Inspect(ctx, publicationPath)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.RootDigest != publication.RootDigest || inspection.Root.Qualification != QualificationDiagnostic || len(inspection.Root.Objects) != 2 {
		t.Fatalf("unexpected publication inspection: %#v", inspection)
	}
	extracted := filepath.Join(root, "extracted")
	config, err := ExtractBinaryBundle(ctx, publicationPath, "operator-tools-linux-amd64", extracted)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Entries) != 2 {
		t.Fatalf("got %d bundle entries", len(config.Entries))
	}
	for name, want := range map[string]string{"kubectl": "kubectl-exact\n", "nkdctl": "nkdctl-exact\n"} {
		got, err := os.ReadFile(filepath.Join(extracted, name))
		if err != nil || string(got) != want {
			t.Fatalf("extracted %s = %q, %v", name, got, err)
		}
	}
	extractedDefinition := filepath.Join(root, "consumer", "build-record.json")
	object, err := ExtractObject(ctx, publicationPath, "build-record", extractedDefinition)
	if err != nil {
		t.Fatal(err)
	}
	gotDefinition, err := os.ReadFile(extractedDefinition)
	if err != nil || string(gotDefinition) != string(definitionRaw) || object.SemanticDigest != digestBytes(definitionRaw) {
		t.Fatalf("extracted definition = %q, object=%#v, err=%v", gotDefinition, object, err)
	}
	if _, err := ExtractObject(ctx, publicationPath, "build-record", extractedDefinition); err == nil {
		t.Fatal("object extraction overwrote an existing destination")
	}
}

func TestBundleLayerRequiresExactDeclaredFiles(t *testing.T) {
	const payload = "tool bytes"
	for _, tc := range []struct {
		name    string
		headers []tar.Header
		payload string
		digest  string
		code    e.Code
	}{
		{"exact", []tar.Header{{Name: "files/tool", Mode: 0o755, Typeflag: tar.TypeReg, Size: int64(len(payload))}}, payload, digestBytes([]byte(payload)), ""},
		{"missing", nil, "", digestBytes([]byte(payload)), e.CodeReleaseIncomplete},
		{"undeclared", []tar.Header{{Name: "files/other", Mode: 0o755, Typeflag: tar.TypeReg}}, "", digestBytes(nil), e.CodeVerifyFailed},
		{"symlink", []tar.Header{{Name: "files/tool", Mode: 0o755, Typeflag: tar.TypeSymlink, Linkname: "../outside"}}, "", digestBytes(nil), e.CodeVerifyFailed},
		{"wrong mode", []tar.Header{{Name: "files/tool", Mode: 0o644, Typeflag: tar.TypeReg}}, "", digestBytes(nil), e.CodeVerifyFailed},
		{"wrong digest", []tar.Header{{Name: "files/tool", Mode: 0o755, Typeflag: tar.TypeReg, Size: int64(len(payload))}}, payload, digestBytes([]byte("other")), e.CodeVerifyFailed},
		{"duplicate", []tar.Header{{Name: "files/tool", Mode: 0o755, Typeflag: tar.TypeReg}, {Name: "files/tool", Mode: 0o755, Typeflag: tar.TypeReg}}, "", digestBytes(nil), e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			gz := gzip.NewWriter(&buf)
			tw := tar.NewWriter(gz)
			for _, header := range tc.headers {
				if err := tw.WriteHeader(&header); err != nil {
					t.Fatal(err)
				}
				if header.Size > 0 {
					if _, err := tw.Write([]byte(tc.payload)); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "layer.gz")
			if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			held, err := heldfile.OpenContext(t.Context(), path, heldfile.Options{Label: "test-layer", MaxBytes: 1 << 20})
			if err != nil {
				t.Fatal(err)
			}
			defer held.Close()
			out := t.TempDir()
			bundle := BinaryBundle{Entries: []BinaryEntry{{Name: "tool", Mode: 0o755, SHA256: tc.digest, Size: int64(len(tc.payload))}}}
			err = extractBundleLayer(context.Background(), held, out, bundle)
			if tc.code != "" {
				assertRegistryCode(t, err, tc.code)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(out, "tool"))
			if err != nil || string(got) != payload {
				t.Fatalf("extracted = %q, %v", got, err)
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
