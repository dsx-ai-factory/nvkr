package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/material"
	"golang.org/x/sync/errgroup"
)

func testBuildMaterials(t *testing.T, observations []material.Observation) (*BuildMaterials, func()) {
	t.Helper()
	manifest, err := material.NewManifest(observations)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := manifest.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	heldManifest, err := heldfile.FromBytesContext(t.Context(), raw, heldfile.Options{
		Label: "test-build-manifest", MaxBytes: maxBuildMaterialManifestBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	inputs := make([]BuildMaterialInput, 0, len(observations))
	heldInputs := make([]*heldfile.File, 0, len(observations))
	for _, observation := range observations {
		payload := []byte("payload for " + observation.Binding + "\n")
		digest := FileDigest(payload)
		if observation.Digest != digest {
			_ = heldManifest.Close()
			t.Fatalf("test observation %q digest = %q, want %q", observation.Binding, observation.Digest, digest)
		}
		held, err := heldfile.FromBytesContext(t.Context(), payload, heldfile.Options{
			Label: "test-build-material-" + observation.Binding, MaxBytes: MaxBuildMaterialBytes,
			ExpectedDigest: digest,
		})
		if err != nil {
			t.Fatal(err)
		}
		path, err := material.StagingPath(observation.Binding)
		if err != nil {
			t.Fatal(err)
		}
		heldInputs = append(heldInputs, held)
		inputs = append(inputs, BuildMaterialInput{Path: path, SHA256: digest, Content: held})
	}
	result, err := NewBuildMaterials(heldManifest, inputs)
	if err != nil {
		t.Fatal(err)
	}
	return result, func() {
		for _, held := range heldInputs {
			_ = held.Close()
		}
		_ = heldManifest.Close()
	}
}

func TestBuildMaterialsStageNativeReadOnlyClosure(t *testing.T) {
	payloadDigest := FileDigest([]byte("payload for runtime-archive\n"))
	materials, closeMaterials := testBuildMaterials(t, []material.Observation{{
		Binding: "runtime-archive", Component: "runtime", Kind: "tarball",
		Architecture: "amd64", Digest: payloadDigest,
	}})
	defer closeMaterials()
	node := ir.Node{ID: "build-composer", BuilderType: ir.BuilderOCIHermetic, Platform: "linux/amd64",
		Materials: []ir.MaterialBinding{{Binding: "runtime-archive", Component: "runtime", Producer: "pull-runtime"}}}
	staged, err := materials.stage(t.Context(), node, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer staged.cleanup()
	if staged.manifestDigest != materials.manifest.Digest() {
		t.Fatalf("manifest digest = %q, want %q", staged.manifestDigest, materials.manifest.Digest())
	}
	for relative, want := range map[string]string{
		"material-manifest.json":                      material.ManifestSchemaVersion,
		filepath.Join("materials", "runtime-archive"): "payload for runtime-archive\n",
	} {
		path := filepath.Join(staged.root, relative)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), want) {
			t.Fatalf("staged %s = %q, want content containing %q", relative, raw, want)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o400 {
			t.Fatalf("staged %s mode = %o, want 0400", relative, info.Mode().Perm())
		}
	}
	wantMount := []string{"-v", staged.root + ":/nkd/inputs:ro"}
	if got := buildMaterialMountArgs(staged.root); !slices.Equal(got, wantMount) {
		t.Fatalf("material mount = %#v, want %#v", got, wantMount)
	}
}

func TestBuildMaterialsStageReportsCleanupFailure(t *testing.T) {
	payloadDigest := FileDigest([]byte("payload for runtime-archive\n"))
	materials, closeMaterials := testBuildMaterials(t, []material.Observation{{
		Binding: "runtime-archive", Component: "runtime", Kind: "tarball",
		Architecture: "amd64", Digest: payloadDigest,
	}})
	defer closeMaterials()
	node := ir.Node{ID: "build-composer", BuilderType: ir.BuilderOCIHermetic, Platform: "linux/amd64",
		Materials: []ir.MaterialBinding{{Binding: "runtime-archive", Component: "runtime", Producer: "pull-runtime"}}}
	stageFailure := e.New(e.CodeBuildFailed, "injected materialization failure")
	cleanupFailure := errors.New("injected cleanup failure")
	staged, err := materials.stageWithOps(t.Context(), node, t.TempDir(), buildMaterialStageOps{
		materializeManifest: func(context.Context, *heldfile.File, string) error { return stageFailure },
		stageInputs: func(context.Context, string, string, []BuildMaterialInput) (*stagedBuildMaterialInputs, error) {
			t.Fatal("stageInputs called after manifest materialization failed")
			return nil, nil
		},
		removeTree: func(string) error { return cleanupFailure },
	})
	if staged != nil {
		t.Fatalf("staged capability = %#v, want nil", staged)
	}
	if code(err) != e.CodeBuildFailed {
		t.Fatalf("stage error = %v, want %s", err, e.CodeBuildFailed)
	}
	if !errors.Is(err, cleanupFailure) {
		t.Fatalf("stage error = %v, want joined cleanup failure", err)
	}
}

func TestBuildMaterialsRejectsMismatchedConsumer(t *testing.T) {
	payloadDigest := FileDigest([]byte("payload for runtime-archive\n"))
	for _, test := range []struct {
		name   string
		mutate func(*ir.Node)
		code   e.Code
	}{
		{name: "wrong component", code: e.CodePipelineDrift, mutate: func(node *ir.Node) {
			node.Materials[0].Component = "other"
		}},
		{name: "wrong architecture", code: e.CodePipelineDrift, mutate: func(node *ir.Node) {
			node.Platform = "linux/arm64"
		}},
		{name: "missing binding", code: e.CodePipelineDrift, mutate: func(node *ir.Node) {
			node.Materials = nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			materials, closeMaterials := testBuildMaterials(t, []material.Observation{{
				Binding: "runtime-archive", Component: "runtime", Kind: "tarball",
				Architecture: "amd64", Digest: payloadDigest,
			}})
			defer closeMaterials()
			node := ir.Node{ID: "build-composer", BuilderType: ir.BuilderOCIHermetic, Platform: "linux/amd64",
				Materials: []ir.MaterialBinding{{Binding: "runtime-archive", Component: "runtime", Producer: "pull-runtime"}}}
			test.mutate(&node)
			_, _, err := materials.validateFor(t.Context(), node)
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != test.code {
				t.Fatalf("validation error = %v, want %s", err, test.code)
			}
		})
	}
}

func TestBuildMaterialAdmissionRejectsMissingAndExtraCapabilities(t *testing.T) {
	node := ir.Node{ID: "build-composer", BuilderType: ir.BuilderOCIHermetic,
		Materials: []ir.MaterialBinding{{Binding: "runtime", Component: "runtime", Producer: "pull-runtime"}}}
	if err := admitBuildMaterials(node, RunOptions{}); code(err) != e.CodeEngineCapability {
		t.Fatalf("missing material capability = %v", err)
	}
	if err := admitBuildMaterials(node, RunOptions{DryRun: true}); err != nil {
		t.Fatalf("dry-run required material bytes: %v", err)
	}
	node.Materials = nil
	if err := admitBuildMaterials(node, RunOptions{BuildMaterials: &BuildMaterials{}}); code(err) != e.CodePipelineDrift {
		t.Fatalf("extra material capability = %v", err)
	}
}

func TestBuildMaterialsStageDifferentConsumersConcurrently(t *testing.T) {
	type consumer struct {
		binding   string
		component string
		materials *BuildMaterials
	}
	consumers := make([]consumer, 0, 2)
	for _, item := range []struct{ binding, component string }{
		{binding: "agent-package", component: "agent"},
		{binding: "runtime-archive", component: "runtime"},
	} {
		digest := FileDigest([]byte("payload for " + item.binding + "\n"))
		materials, closeMaterials := testBuildMaterials(t, []material.Observation{{
			Binding: item.binding, Component: item.component, Kind: "tarball",
			Architecture: "amd64", Digest: digest,
		}})
		defer closeMaterials()
		consumers = append(consumers, consumer{
			binding: item.binding, component: item.component,
			materials: materials,
		})
	}
	parent := t.TempDir()
	roots := make(chan string, len(consumers))
	group, ctx := errgroup.WithContext(t.Context())
	for index := range consumers {
		item := consumers[index]
		group.Go(func() error {
			node := ir.Node{ID: "build-" + item.component, BuilderType: ir.BuilderOCIHermetic,
				Platform: "linux/amd64", Materials: []ir.MaterialBinding{{
					Binding: item.binding, Component: item.component, Producer: "pull-" + item.component,
				}}}
			staged, err := item.materials.stage(ctx, node, parent)
			if err != nil {
				return err
			}
			defer staged.cleanup()
			roots <- staged.root
			raw, err := os.ReadFile(filepath.Join(staged.root, "materials", item.binding))
			if err != nil {
				return err
			}
			if string(raw) != "payload for "+item.binding+"\n" {
				return fmt.Errorf("consumer %s received %q", item.component, raw)
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		t.Fatal(err)
	}
	close(roots)
	seen := make(map[string]bool, len(consumers))
	for root := range roots {
		if seen[root] {
			t.Fatalf("concurrent material consumers shared staging root %q", root)
		}
		seen[root] = true
	}
}
