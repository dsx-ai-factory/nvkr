package factory

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nhensley/nkdctl/pkg/nkd/delivery"
)

// This opt-in integration test uses real produced inputs and a local Helm binary.
// The ordinary contract suite never treats a skipped external test as evidence.
func TestPinnedNKEHelmBinding(t *testing.T) {
	archive, layout := os.Getenv("NKD_HELM_ARCHIVE"), os.Getenv("NKD_CWE_IMAGE_LAYOUT")
	if archive == "" && layout == "" {
		t.Skip("set NKD_HELM_ARCHIVE and NKD_CWE_IMAGE_LAYOUT for real-input Helm validation")
	}
	if archive == "" || layout == "" {
		t.Fatal("both real input paths are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	image, err := delivery.InspectContainerImage(ctx, layout, "linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	root, relative, cleanup, err := helmPackageTree(ctx, &Plan{}, Artifact{ID: "charts", Kind: "tarball", RecipeTarget: "nke", Path: archive})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	tree := filepath.Join(root, relative)
	if err := bindHelmImage(ctx, tree, "007-controller", image.ManifestDigest); err != nil {
		t.Fatal(err)
	}
	checksums, err := os.ReadFile(filepath.Join(tree, "checksums.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(checksums)), "\n") {
		fields := strings.SplitN(line, "  ", 2)
		if len(fields) != 2 {
			t.Fatalf("invalid checksum line %q", line)
		}
		raw, err := os.ReadFile(filepath.Join(tree, fields[1]))
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprintf("%x", sha256.Sum256(raw)) != fields[0] {
			t.Fatalf("stale checksum: %s", fields[1])
		}
	}
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Fatal(err)
	}
	chart := filepath.Join(tree, "007-controller")
	if raw, err := exec.CommandContext(ctx, helm, "template", "controller", chart).CombinedOutput(); err == nil || !strings.Contains(string(raw), "set image.repository") {
		t.Fatalf("missing repository must fail: %v\n%s", err, raw)
	}
	for _, repository := range []string{"ghcr.io/njhensley/nke-recipes/cwe", "mirror.example.test/cwe"} {
		raw, err := exec.CommandContext(ctx, helm, "template", "controller", chart, "--set-string", "image.repository="+repository,
			"--set-string", "image.digest=sha256:"+strings.Repeat("f", 64)).CombinedOutput()
		if err != nil {
			t.Fatalf("Helm render: %v\n%s", err, raw)
		}
		if !strings.Contains(string(raw), repository+"@"+image.ManifestDigest) || strings.Contains(string(raw), "nke-controller:0.0.0-dev") {
			t.Fatalf("rendered image does not bind produced digest: %s", raw)
		}
	}
	for _, local := range []string{"002-postgresql", "004-spire-crds-post"} {
		if raw, err := exec.CommandContext(ctx, helm, "template", "fixture", filepath.Join(tree, local)).CombinedOutput(); err != nil {
			t.Fatalf("rendering %s: %v\n%s", local, err, raw)
		}
	}
	t.Logf("bound and rendered all three local charts; controller image %s; verified %d checksum entries", image.ManifestDigest, len(strings.Split(strings.TrimSpace(string(checksums)), "\n")))
}
