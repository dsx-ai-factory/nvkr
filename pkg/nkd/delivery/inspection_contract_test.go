package delivery

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/internal/heldfile"
)

func objectPublicationFixture(t *testing.T) (string, []byte) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "publication")
	payload := []byte(`{"schemaVersion":"example/v1","value":"exact"}`)
	_, err := BuildPublicationRoot(t.Context(), root, PublicationRequest{Objects: []ObjectInput{{Name: "record", Kind: "record", ArtifactType: MediaDeliveryObject, LayerMediaType: MediaOctetStream, SemanticDigest: digestBytes(payload), Bytes: payload}}})
	if err != nil {
		t.Fatal(err)
	}
	return root, payload
}

func TestReadPublicationObjectReturnsDetachedExactPayload(t *testing.T) {
	root, want := objectPublicationFixture(t)
	object, got, err := ReadObject(t.Context(), root, "record")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) || object.BlobDigest != digestBytes(want) || object.Name != "record" {
		t.Fatalf("object = %+v, bytes = %q", object, got)
	}
	got[0] = '!'
	_, again, err := ReadObject(t.Context(), root, "record")
	if err != nil || !bytes.Equal(again, want) {
		t.Fatalf("caller mutated stored payload: %q, %v", again, err)
	}
	for _, name := range []string{"absent", ""} {
		_, _, err := ReadObject(t.Context(), root, name)
		assertRegistryCode(t, err, e.CodeUnverifiedRef)
		out := filepath.Join(t.TempDir(), "output")
		_, err = ExtractObject(t.Context(), root, name, out)
		assertRegistryCode(t, err, e.CodeUnverifiedRef)
		_, err = ExtractBinaryBundle(t.Context(), root, name, out)
		assertRegistryCode(t, err, e.CodeUnverifiedRef)
		if _, err := os.Stat(out); !os.IsNotExist(err) {
			t.Fatalf("failed selection created output: %v", err)
		}
	}
}

func TestPublicationIndexRejectsSubstitutedBindings(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*ociIndex)
	}{
		{"wrong artifact type", func(i *ociIndex) { i.ArtifactType = MediaBinaryBundle }},
		{"wrong semantic digest", func(i *ociIndex) { i.Annotations["dev.nkd.semantic.digest"] = digestBytes([]byte("other")) }},
		{"duplicate root", func(i *ociIndex) {
			for _, d := range i.Manifests {
				if d.ArtifactType == MediaPublicationRoot {
					i.Manifests = append(i.Manifests, d)
					break
				}
			}
		}},
		{"wrong object annotation", func(i *ociIndex) {
			for j := range i.Manifests {
				if i.Manifests[j].ArtifactType != MediaPublicationRoot {
					i.Manifests[j].Annotations["dev.nkd.object.kind"] = "other"
				}
			}
		}},
		{"wrong descriptor size", func(i *ociIndex) { i.Manifests[0].Size++ }},
		{"unsupported descriptor media", func(i *ociIndex) { i.Manifests[0].MediaType = "application/unknown" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, _ := objectPublicationFixture(t)
			index, _, err := readIndex(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			tc.change(&index)
			raw, err := json.Marshal(index)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "index.json"), raw, 0o644); err != nil {
				t.Fatal(err)
			}
			_, err = Inspect(t.Context(), root)
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			out := filepath.Join(t.TempDir(), "output")
			_, err = ExtractObject(t.Context(), root, "record", out)
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatalf("invalid publication exposed output: %v", err)
			}
		})
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
