//go:build darwin || linux

package aicr

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"golang.org/x/sys/unix"
)

func TestQualifiedResultRecheckRejectsFIFOWithoutBlocking(t *testing.T) {
	root := t.TempDir()
	for name, body := range map[string]string{
		"recipe.yaml":   "kind: Recipe\n",
		"checksums.txt": "verified\n",
		"payload":       "original\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	digest, err := descriptorBundleDigest(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	result := Result{
		Profile:         demo,
		RecipeFile:      filepath.Join(root, "recipe.yaml"),
		RecipeDigest:    strings.TrimPrefix(demo.Digest, "sha256:"),
		GeneratorDigest: demo.GeneratorDigest,
		BundleDigest:    digest,
		OutputDir:       root,
		ChecksumsFile:   filepath.Join(root, "checksums.txt"),
	}
	qualified := &QualifiedResult{result: result, qualification: &resultQualification{
		profile: demo, recipeDigest: result.RecipeDigest,
		generatorDigest: result.GeneratorDigest, bundleDigest: result.BundleDigest,
	}}
	if _, err := qualified.ValidateFor(context.Background(), demo); err != nil {
		t.Fatalf("valid qualified bundle failed recheck: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "payload")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(root, "payload"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := qualified.ValidateFor(ctx, demo); code(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("qualified FIFO recheck returned %v, want verify-failed", err)
	}
}

func TestDescriptorBundleDigestIsContextFirst(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	missing := filepath.Join(t.TempDir(), "must-not-be-opened")
	if _, err := descriptorBundleDigest(ctx, missing); code(err) != nkderr.CodeTimeout {
		t.Fatalf("pre-canceled qualified digest returned %v, want timeout", err)
	}
}
