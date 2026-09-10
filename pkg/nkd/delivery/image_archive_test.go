package delivery

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
)

func imageArchiveFixture(t *testing.T, extra *tar.Header) ([]byte, ImageInspection) {
	t.Helper()
	root := imageFixture(t)
	image, err := InspectContainerImage(context.Background(), root, "linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	w := tar.NewWriter(&buffer)
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		name, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := w.WriteHeader(&tar.Header{Name: filepath.ToSlash(name), Mode: 0o644, Size: int64(len(raw)), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err = w.Write(raw)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if extra != nil {
		if err := w.WriteHeader(extra); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes(), image
}

func TestImportContainerImage(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		extra       *tar.Header
		wrongDigest bool
		existing    bool
		code        e.Code
	}{
		{name: "valid"},
		{name: "archive digest mismatch", wrongDigest: true, code: e.CodeVerifyFailed},
		{name: "existing output", existing: true, code: e.CodeImmutableViolation},
		{name: "traversal", extra: &tar.Header{Name: "../escape", Typeflag: tar.TypeReg}, code: e.CodeVerifyFailed},
		{name: "symlink", extra: &tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "index.json"}, code: e.CodeVerifyFailed},
		{name: "duplicate", extra: &tar.Header{Name: "index.json", Typeflag: tar.TypeReg}, code: e.CodeVerifyFailed},
		{name: "unexpected file", extra: &tar.Header{Name: "unexpected", Typeflag: tar.TypeReg}, code: e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, expected := imageArchiveFixture(t, tc.extra)
			root := t.TempDir()
			archive := filepath.Join(root, "image.tar")
			if err := os.WriteFile(archive, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(root, "imported")
			if tc.existing {
				if err := os.Mkdir(out, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			digest := digestBytes(raw)
			if tc.wrongDigest {
				digest = "sha256:" + strings.Repeat("a", 64)
			}
			actual, err := ImportContainerImage(context.Background(), archive, digest, "linux/amd64", out)
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				if actual.ManifestDigest != expected.ManifestDigest || actual.RootDigest != expected.RootDigest {
					t.Fatalf("import changed image: %#v", actual)
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != tc.code {
				t.Fatalf("error = %v, want %s", err, tc.code)
			}
			if !tc.existing {
				if _, err := os.Stat(out); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("failed import exposed output: %v", err)
				}
			}
		})
	}
}
