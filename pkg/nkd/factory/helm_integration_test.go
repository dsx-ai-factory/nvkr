package factory

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	"github.com/google/go-containerregistry/pkg/name"
	"go.yaml.in/yaml/v3"
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
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Fatal(err)
	}
	chart := tree
	for _, repository := range []string{"ghcr.io/nvidia/nke-recipes/cwe", "mirror.example.test/cwe"} {
		raw, err := exec.CommandContext(ctx, helm, "template", "controller", chart, "--set-string", "image.repository="+repository, "--set-string", "nameOverride=nkd-binding-controller", "--set-string", "nodeAgent.spireAttestationMethod=tpm").CombinedOutput()
		if err != nil {
			t.Fatalf("Helm render: %v\n%s", err, raw)
		}
		if err := verifyRenderedControllerImage(raw, repository, image.ManifestDigest); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("rendered source chart with produced image digest %s", image.ManifestDigest)
}

// verifyRenderedControllerImage checks the selected container, not unrelated YAML fields.
func verifyRenderedControllerImage(raw []byte, repository, digest string) error {
	decoder := yaml.NewDecoder(strings.NewReader(string(raw)))
	count := 0
	for {
		var workload struct {
			Kind string `yaml:"kind"`
			Spec struct {
				Template struct {
					Spec struct {
						Containers []struct {
							Name  string `yaml:"name"`
							Image string `yaml:"image"`
						} `yaml:"containers"`
					} `yaml:"spec"`
				} `yaml:"template"`
			} `yaml:"spec"`
		}
		err := decoder.Decode(&workload)
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("decode rendered workload: %w", err)
		}
		if workload.Kind != "Deployment" {
			continue
		}
		for _, container := range workload.Spec.Template.Spec.Containers {
			if container.Name != "nkd-binding-controller" {
				continue
			}
			count++
			ref, err := name.NewDigest(container.Image, name.StrictValidation)
			if err != nil {
				return fmt.Errorf("controller image is not digest-pinned: %q: %w", container.Image, err)
			}
			if ref.Context().Name() != repository || ref.DigestStr() != digest {
				return fmt.Errorf("controller image %q does not match %s@%s", container.Image, repository, digest)
			}
		}
	}
	if count != 1 {
		return fmt.Errorf("found %d controller containers, want exactly one", count)
	}
	return nil
}

func TestRenderedControllerImage(t *testing.T) {
	repository := "registry.example.test/nke/cwe"
	digest := "sha256:" + strings.Repeat("a", 64)
	fixture := func(image string) string {
		return fmt.Sprintf("apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  annotations:\n    expected: '%s@%s'\nspec:\n  template:\n    spec:\n      containers:\n      - name: nkd-binding-controller\n        image: %s\n", repository, digest, image)
	}
	for _, tc := range []struct {
		name, raw string
		valid     bool
	}{
		{"tag and digest", fixture(repository + ":dev@" + digest), true},
		{"digest only", fixture(repository + "@" + digest), true},
		{"digest only in annotation", fixture(repository + ":dev"), false},
		{"wrong repository", fixture("other.example.test/cwe@" + digest), false},
		{"wrong digest", fixture(repository + "@sha256:" + strings.Repeat("b", 64)), false},
		{"missing controller", strings.ReplaceAll(fixture(repository+"@"+digest), "nkd-binding-controller", "sidecar"), false},
		{"ambiguous controller", fixture(repository+"@"+digest) + "---\n" + fixture(repository+"@"+digest), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := verifyRenderedControllerImage([]byte(tc.raw), repository, digest)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%t, error=%v", tc.valid, err)
			}
		})
	}
}
