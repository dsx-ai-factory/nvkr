// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/ociclosure"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

// This tests actual OCI bytes, the observation renderer and transported consumer
// evidence together. Synthetic producer records do not issue live native
// production authority; that boundary is checked separately below and in pipeline.
func TestRuntimeObservationJoinsActualLayoutAndRelocatedConsumerEvidence(t *testing.T) {
	input, env, stack, stacks := runtimeConsumerInputsFixture(t, []string{"node-image"})
	source, err := filepath.Abs("../../../toolchains/nvkr-vm-runtime")
	if err != nil {
		t.Fatal(err)
	}
	fixture := t.TempDir()
	if err := os.CopyFS(fixture, os.DirFS(source)); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(fixture, "pins.lock.yaml")
	lock, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the complete selected graph, with small fixture tool and license bytes.
	for _, name := range []string{"packer-amd64", "packer-plugin-qemu-amd64"} {
		pin, err := stacks["internal/runtime"].Catalog.SourcePin(name)
		if err != nil {
			t.Fatal(err)
		}
		lock = bytes.ReplaceAll(lock, []byte(pin.Tool.ExecutableDigest), []byte(pipeline.FileDigest([]byte(name))))
		lock = bytes.ReplaceAll(lock, []byte(pin.Tool.Companions[0].Digest), []byte(pipeline.FileDigest([]byte("license "+name))))
	}
	if err := os.WriteFile(lockPath, lock, 0o600); err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.LoadContext(t.Context(), catalog.Dir(fixture))
	if err != nil {
		t.Fatal(err)
	}
	stacks["internal/runtime"], err = catalog.ApplyStack(cat)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := compileWithTargetStacks(t.Context(), input, env, stack, stacks, "test", "", "")
	if err != nil {
		t.Fatal(err)
	}
	before, err := canonical.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	const workspace = ".nvkr/relocated-runtime"
	relocated, err := relocateFactoryPlan(plan, workspace)
	if err != nil {
		t.Fatal(err)
	}
	_, artifacts := planIndex(relocated)
	tool := relocated.InternalTools[0]
	manifest, layer, size := writeObservationOCI(t, artifacts[tool.ArtifactID].Path)
	tree, err := ociclosure.Snapshot(t.Context(), artifacts[tool.ArtifactID].Path)
	if err != nil {
		t.Fatal(err)
	}
	oldDigest, err := digestFactoryPath(t.Context(), artifacts[tool.ArtifactID].Path)
	if err != nil || oldDigest == tree.Digest() {
		t.Fatalf("fixture does not distinguish digest families: %v", err)
	}
	writeRuntimeConsistencyFixture(t, relocated, artifacts, tool, manifest, layer, size)
	node := planNode(relocated, artifacts[tool.ObservationArtifactID].Producer)
	for _, id := range node.Inputs {
		artifact := artifacts[id]
		if artifact.Kind == "producer-result" {
			_, producer, err := relocated.producerInstruction(planNode(relocated, artifact.Producer))
			if err != nil {
				t.Fatal(err)
			}
			result := pipeline.ArtifactResult{Component: producer.Component, OutputType: producer.OutputType,
				SHA256: producer.Digest, Size: int64(len(producer.Component)), EngineClass: ir.EnginePrebuiltAcquire,
				Qualification: pipeline.ArtifactProfileFreeAcquisition}
			if artifact.ID == tool.ResultArtifactID {
				result = pipeline.ArtifactResult{Component: producer.Component, OutputType: producer.OutputType,
					SHA256: manifest, Size: size, BuilderType: producer.BuilderType, EngineClass: producer.BuilderType,
					Qualification: pipeline.ArtifactUnqualified, Path: producer.ArtifactPath, TransportPath: artifacts[tool.ArtifactID].Path,
					InputDigest: pipeline.FileDigest([]byte("fixture source")), MaterialManifestDigest: pipeline.FileDigest([]byte("fixture materials"))}
			}
			raw, err := canonical.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if err := writeFactoryFile(t.Context(), artifact.Path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
		} else if artifact.ID != tool.ArtifactID {
			raw := []byte(artifact.Component)
			if artifact.Kind == ArtifactReleaseCompanion {
				raw = []byte("license " + artifact.Component)
			}
			if err := writeFactoryFile(t.Context(), artifact.Path, raw, 0o444); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := executeObservationInputs(t.Context(), relocated, node, artifacts, nil,
		&runtimeObservation{artifact: tool.ArtifactID, layout: tree.Digest()}); err != nil {
		t.Fatal(err)
	}
	if err := validateInternalToolEvidence(t.Context(), relocated, tool, node, artifacts); err != nil {
		t.Fatal(err)
	}
	writeRuntimeConsumerDigests(t, relocated, artifacts, tree.Digest(), layer)
	_, originalArtifacts := planIndex(plan)
	local := func(path string) (string, error) { return relocatedFactoryPath(path, workspace) }
	if err := validateRuntimeConsistency(t.Context(), plan, originalArtifacts, local); err != nil {
		t.Fatal(err)
	}
	after, err := canonical.Marshal(plan)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("observation changed Build or Plan bytes")
	}
	unchanged, err := ociclosure.Snapshot(t.Context(), artifacts[tool.ArtifactID].Path)
	if err != nil || unchanged.Digest() != tree.Digest() {
		t.Fatal("observation changed OCI layout or layer bytes")
	}
	// A different digest family cannot be substituted, even with freshly rebound
	// observation-file evidence on the consumer's descriptive result.
	if err := executeObservationInputs(t.Context(), relocated, node, artifacts, nil,
		&runtimeObservation{artifact: tool.ArtifactID, layout: oldDigest}); err != nil {
		t.Fatal(err)
	}
	writeRuntimeConsumerDigests(t, relocated, artifacts, tree.Digest(), layer)
	assertFactoryErrorCode(t, validateRuntimeConsistency(t.Context(), plan, originalArtifacts, local), e.CodeVerifyFailed)
}

func TestRuntimeObservationRequiresLiveOwnerBeforeWriting(t *testing.T) {
	plan := runtimeConsumerPlanFixture(t)
	t.Chdir(t.TempDir())
	_, artifacts := planIndex(plan)
	tool := plan.InternalTools[0]
	node := planNode(plan, artifacts[tool.ObservationArtifactID].Producer)
	identity, err := plan.IdentityDigest()
	if err != nil {
		t.Fatal(err)
	}
	for _, closed := range []bool{false, true} {
		owner := &LocalRunResult{PlanDigest: identity, diskClosed: closed}
		assertFactoryErrorCode(t, owner.executeRuntimeObservation(t.Context(), plan, node, artifacts, tool.Name), e.CodeGateUnmet)
	}
	assertFactoryErrorCode(t, executeObservation(t.Context(), plan, node, artifacts), e.CodeEngineCapability)
	if _, err := os.Stat(artifacts[tool.ObservationArtifactID].Path); !os.IsNotExist(err) {
		t.Fatal("unowned observation wrote output")
	}
}

func TestRuntimeObservationPreservesOrdinaryDigestFamilies(t *testing.T) {
	root := t.TempDir()
	raw := []byte{'Q', 'F', 'I', 0xfb, 0, 0, 0, 3}
	file := filepath.Join(root, "disk.qcow2")
	if err := writeFactoryFile(t.Context(), file, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	fileDigest := pipeline.FileDigest(raw)
	treeDigest, err := canonical.Digest(canonical.KindFactoryObservation, []factoryTreeRow{
		{Path: "disk.qcow2", Kind: "file", Mode: 0o644, Digest: fileDigest},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ kind, path, want string }{
		{"oci-image", root, treeDigest}, {"disk-image", file, fileDigest},
	} {
		got, err := digestObservationInput(t.Context(), &Plan{}, Artifact{ID: "ordinary", Kind: tc.kind}, tc.path)
		if err != nil || got != tc.want {
			t.Fatalf("%s digest changed: %s %v", tc.kind, got, err)
		}
	}
	after, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(raw, after) {
		t.Fatal("observation changed disk bytes")
	}
}

func writeObservationOCI(t *testing.T, root string) (string, string, int64) {
	t.Helper()
	var size int64
	write := func(name string, raw []byte) {
		t.Helper()
		if err := writeFactoryFile(t.Context(), filepath.Join(root, name), raw, 0o644); err != nil {
			t.Fatal(err)
		}
		size += int64(len(raw))
	}
	encode := func(value any) []byte {
		raw, err := canonical.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	blob := func(raw []byte, media string) ociclosure.Descriptor {
		digest := pipeline.FileDigest(raw)
		write("blobs/sha256/"+strings.TrimPrefix(digest, "sha256:"), raw)
		return ociclosure.Descriptor{MediaType: media, Digest: digest, Size: int64(len(raw))}
	}
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	layer := blob(archive.Bytes(), "application/vnd.oci.image.layer.v1.tar")
	config := blob(encode(map[string]any{"os": "linux", "architecture": "amd64", "rootfs": map[string]any{"type": "layers", "diff_ids": []string{layer.Digest}}}), "application/vnd.oci.image.config.v1+json")
	manifest := blob(encode(ociclosure.ManifestDocument{SchemaVersion: 2, MediaType: "application/vnd.oci.image.manifest.v1+json", Config: config, Layers: []ociclosure.Descriptor{layer}}), "application/vnd.oci.image.manifest.v1+json")
	write("index.json", encode(ociclosure.IndexDocument{SchemaVersion: 2, MediaType: "application/vnd.oci.image.index.v1+json", Manifests: []ociclosure.Descriptor{manifest}}))
	write("oci-layout", []byte(`{"imageLayoutVersion":"1.0.0"}`))
	return manifest.Digest, layer.Digest, size
}
