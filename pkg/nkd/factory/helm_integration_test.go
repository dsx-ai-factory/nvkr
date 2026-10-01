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

	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
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
	root, relative, cleanup, err := helmPackageTree(ctx, Artifact{ID: "charts", Kind: "tarball", BuildTarget: "nke", Path: archive})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	tree := filepath.Join(root, relative)
	binding := testHelmBinding("package/cwe", ".")
	if err := bindHelmImages(ctx, tree, *binding, map[string]string{"controller": image.ManifestDigest}); err != nil {
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
	chart := tree
	for _, repository := range []string{"ghcr.io/nvidia/nke-recipes/cwe", "mirror.example.test/cwe"} {
		raw, err := exec.CommandContext(ctx, helm, "template", "controller", chart, "--set-string", "image.repository="+repository).CombinedOutput()
		if err != nil {
			t.Fatalf("Helm render: %v\n%s", err, raw)
		}
		if !strings.Contains(string(raw), "@"+image.ManifestDigest) || strings.Contains(string(raw), "nke-controller:0.0.0-dev") {
			t.Fatalf("rendered image does not bind produced digest: %s", raw)
		}
	}
	t.Logf("rendered source chart with produced image digest %s", image.ManifestDigest)
}
