// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/assemble"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"golang.org/x/sys/unix"
)

var imageIndexPlatforms = []string{"linux/amd64", "linux/arm64"}

func imageIndexInputs(t *testing.T) []ImageIndexInput {
	t.Helper()
	var inputs []ImageIndexInput
	for _, arch := range []string{"amd64", "arm64"} {
		directory := t.TempDir()
		binary := filepath.Join(directory, "worker")
		if err := os.WriteFile(binary, []byte("shared fixture worker"), 0o755); err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(directory, "image")
		digest, err := assemble.OCIImage(binary, "worker", 1, arch, root)
		if err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, ImageIndexInput{Platform: "linux/" + arch, LayoutPath: root, ManifestDigest: digest})
	}
	return inputs
}

func assembledImageIndex(t *testing.T, inputs []ImageIndexInput) *ImageIndex {
	t.Helper()
	index, err := AssembleImageIndex(context.Background(), imageIndexPlatforms, inputs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = index.Close() })
	return index
}

func writeIndexFixture(t *testing.T, index *ImageIndex) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "index")
	if err := index.WriteLayout(context.Background(), out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestImageIndexDeterministicAndStandardReader(t *testing.T) {
	t.Parallel()
	inputs := imageIndexInputs(t)
	index := assembledImageIndex(t, inputs)
	description, err := index.Description()
	if err != nil {
		t.Fatal(err)
	}
	reversed, err := AssembleImageIndex(context.Background(), []string{"linux/arm64", "linux/amd64"},
		[]ImageIndexInput{inputs[1], inputs[0]})
	if err != nil {
		t.Fatal(err)
	}
	defer reversed.Close()
	other, err := reversed.Description()
	if err != nil || !reflect.DeepEqual(description, other) {
		t.Fatalf("input order changed identity: %#v / %#v: %v", description, other, err)
	}
	root := writeIndexFixture(t, index)
	raw, err := os.ReadFile(filepath.Join(root, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var canonicalIndex runnableIndex
	if err := canonical.UnmarshalExact(raw, &canonicalIndex); err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(mustReadImageFile(t, filepath.Join(writeIndexFixture(t, reversed), "index.json"))) {
		t.Fatal("permuted inputs changed emitted index bytes")
	}
	blobs, err := os.ReadDir(filepath.Join(root, "blobs", "sha256"))
	if err != nil || len(blobs) != 5 { // Two manifests + two configs + one shared unchanged layer.
		t.Fatalf("shared layer not deduplicated: %d, %v", len(blobs), err)
	}
	path, err := layout.FromPath(root)
	if err != nil {
		t.Fatal(err)
	}
	standard, err := path.ImageIndex()
	if err != nil {
		t.Fatal(err)
	}
	digest, err := standard.Digest()
	if err != nil || digest.String() != description.Digest {
		t.Fatalf("standard index digest differs: %s, %v", digest, err)
	}
	manifest, err := standard.IndexManifest()
	if err != nil || len(manifest.Manifests) != 2 {
		t.Fatalf("standard index: %#v, %v", manifest, err)
	}
	for i, descriptor := range manifest.Manifests {
		image, err := standard.Image(descriptor.Digest)
		if err != nil {
			t.Fatal(err)
		}
		config, err := image.ConfigFile()
		if err != nil || descriptor.Platform == nil || descriptor.Platform.OS != config.OS || descriptor.Platform.Architecture != config.Architecture ||
			config.OS+"/"+config.Architecture != inputs[i].Platform {
			t.Fatalf("platform selection: %#v, %#v, %v", descriptor.Platform, config, err)
		}
		childRaw, err := image.RawManifest()
		if err != nil || !bytes.Equal(childRaw, mustReadImageFile(t, filepath.Join(inputs[i].LayoutPath, "blobs", "sha256", strings.TrimPrefix(inputs[i].ManifestDigest, "sha256:")))) {
			t.Fatalf("child manifest changed: %v", err)
		}
		for _, entry := range mustReadImageDirectory(t, filepath.Join(inputs[i].LayoutPath, "blobs", "sha256")) {
			before := mustReadImageFile(t, filepath.Join(inputs[i].LayoutPath, "blobs", "sha256", entry.Name()))
			after := mustReadImageFile(t, filepath.Join(root, "blobs", "sha256", entry.Name()))
			if !bytes.Equal(before, after) {
				t.Fatalf("child blob %s changed", entry.Name())
			}
		}
	}
	opened, err := OpenImageIndex(context.Background(), root, description.Digest, imageIndexPlatforms)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	observed, err := opened.Description()
	if err != nil || !reflect.DeepEqual(description, observed) {
		t.Fatalf("round trip: %#v, %v", observed, err)
	}
	// The old single-image contract must not silently broaden to aggregates.
	_, err = InspectContainerImage(context.Background(), root, "linux/amd64")
	assertRegistryCode(t, err, e.CodeVerifyFailed)
}

func TestImageIndexAdmissionBeforeFilesystem(t *testing.T) {
	t.Parallel()
	pin := "sha256:" + strings.Repeat("a", 64)
	base := ImageIndexInput{Platform: "linux/amd64", LayoutPath: "/nonexistent/image", ManifestDigest: pin}
	for _, tc := range []struct {
		name      string
		platforms []string
		inputs    []ImageIndexInput
		code      e.Code
	}{
		{"empty", nil, nil, e.CodeUsage},
		{"unsupported", []string{"linux/riscv64"}, nil, e.CodeEngineCapability},
		{"duplicate requirement", []string{"linux/amd64", "linux/amd64"}, nil, e.CodeUsage},
		{"missing", imageIndexPlatforms, []ImageIndexInput{base}, e.CodeUsage},
		{"duplicate input", imageIndexPlatforms, []ImageIndexInput{base, base}, e.CodeUsage},
		{"extra input", []string{"linux/arm64"}, []ImageIndexInput{base}, e.CodeUsage},
		{"empty path", []string{"linux/amd64"}, []ImageIndexInput{{Platform: "linux/amd64", ManifestDigest: pin}}, e.CodeUsage},
		{"invalid digest", []string{"linux/amd64"}, []ImageIndexInput{{Platform: "linux/amd64", LayoutPath: base.LayoutPath, ManifestDigest: "latest"}}, e.CodeUsage},
		{"duplicate digest", imageIndexPlatforms, []ImageIndexInput{base, {Platform: "linux/arm64", LayoutPath: base.LayoutPath, ManifestDigest: pin}}, e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := AssembleImageIndex(context.Background(), tc.platforms, tc.inputs)
			assertRegistryCode(t, err, tc.code)
		})
	}
	for _, tc := range []struct {
		name      string
		ctx       context.Context
		pin       string
		platforms []string
		code      e.Code
	}{
		{"nil context", nil, pin, imageIndexPlatforms, e.CodeUsage},
		{"empty platform set", context.Background(), pin, nil, e.CodeUsage},
		{"unsupported platform", context.Background(), pin, []string{"linux/ppc64le"}, e.CodeEngineCapability},
		{"invalid identity", context.Background(), "latest", imageIndexPlatforms, e.CodeUsage},
	} {
		t.Run("open "+tc.name, func(t *testing.T) {
			_, err := OpenImageIndex(tc.ctx, "/nonexistent/image", tc.pin, tc.platforms)
			assertRegistryCode(t, err, tc.code)
		})
	}
}

func TestImageIndexRejectsMalformedChildren(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, *ImageIndexInput)
	}{
		{"wrong manifest pin", func(_ *testing.T, input *ImageIndexInput) { input.ManifestDigest = "sha256:" + strings.Repeat("a", 64) }},
		{"wrong config platform", func(t *testing.T, input *ImageIndexInput) {
			rewriteImageChild(t, input, func(_ *ociManifest, config map[string]any) { config["architecture"] = "arm64" })
		}},
		{"wrong diff ID", func(t *testing.T, input *ImageIndexInput) {
			rewriteImageChild(t, input, func(_ *ociManifest, config map[string]any) {
				config["rootfs"].(map[string]any)["diff_ids"] = []string{"sha256:" + strings.Repeat("a", 64)}
			})
		}},
		{"attestation config", func(t *testing.T, input *ImageIndexInput) {
			rewriteImageChild(t, input, func(manifest *ociManifest, _ map[string]any) { manifest.Config.MediaType = MediaEmptyConfig })
		}},
		{"artifact manifest", func(t *testing.T, input *ImageIndexInput) {
			rewriteImageChild(t, input, func(manifest *ociManifest, _ map[string]any) {
				manifest.ArtifactType = "application/vnd.example.attestation"
			})
		}},
		{"subject-bearing manifest", func(t *testing.T, input *ImageIndexInput) {
			rewriteImageChild(t, input, func(manifest *ociManifest, _ map[string]any) { manifest.Subject = &manifest.Config })
		}},
		{"unsupported variant", func(t *testing.T, input *ImageIndexInput) {
			rewriteImageChild(t, input, func(_ *ociManifest, config map[string]any) { config["variant"] = "v3" })
		}},
		{"unreachable valid blob", func(t *testing.T, input *ImageIndexInput) {
			raw := []byte("extra")
			writeImageTestFile(t, filepath.Join(input.LayoutPath, "blobs", "sha256", strings.TrimPrefix(digestBytes(raw), "sha256:")), raw)
		}},
		{"corrupt layer", func(t *testing.T, input *ImageIndexInput) {
			_, manifest := fixtureManifest(t, input.LayoutPath)
			writeImageTestFile(t, filepath.Join(input.LayoutPath, "blobs", "sha256", strings.TrimPrefix(manifest.Layers[0].Digest, "sha256:")), []byte("corrupt"))
		}},
		{"missing blob", func(t *testing.T, input *ImageIndexInput) {
			_, manifest := fixtureManifest(t, input.LayoutPath)
			if err := os.Remove(filepath.Join(input.LayoutPath, "blobs", "sha256", strings.TrimPrefix(manifest.Layers[0].Digest, "sha256:"))); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlink blob", func(t *testing.T, input *ImageIndexInput) {
			_, manifest := fixtureManifest(t, input.LayoutPath)
			path := filepath.Join(input.LayoutPath, "blobs", "sha256", strings.TrimPrefix(manifest.Layers[0].Digest, "sha256:"))
			if err := os.Rename(path, filepath.Join(t.TempDir(), "layer")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("/nonexistent", path); err != nil {
				t.Fatal(err)
			}
		}},
		{"fifo index", func(t *testing.T, input *ImageIndexInput) {
			path := filepath.Join(input.LayoutPath, "index.json")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := unix.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"extra directory entry", func(t *testing.T, input *ImageIndexInput) {
			writeImageTestFile(t, filepath.Join(input.LayoutPath, "extra"), []byte("extra"))
		}},
		{"duplicate marker key", func(t *testing.T, input *ImageIndexInput) {
			writeImageTestFile(t, filepath.Join(input.LayoutPath, "oci-layout"), []byte(`{"imageLayoutVersion":"1.0.0","imageLayoutVersion":"1.0.0"}`))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inputs := imageIndexInputs(t)
			tc.mutate(t, &inputs[0])
			_, err := AssembleImageIndex(context.Background(), imageIndexPlatforms, inputs)
			assertRegistryCode(t, err, e.CodeVerifyFailed)
		})
	}
}

func TestImageIndexRejectsMalformedIndex(t *testing.T) {
	t.Parallel()
	index := assembledImageIndex(t, imageIndexInputs(t))
	for _, tc := range []struct {
		name   string
		mutate func(*runnableIndex)
	}{
		{"missing platform", func(index *runnableIndex) { index.Manifests[0].Platform = nil }},
		{"wrong platform", func(index *runnableIndex) { index.Manifests[0].Platform.Architecture = "arm64" }},
		{"extra platform", func(index *runnableIndex) { index.Manifests[0].Platform.Architecture = "riscv64" }},
		{"unsupported variant", func(index *runnableIndex) { index.Manifests[1].Platform.Variant = "v9" }},
		{"duplicate descriptor", func(index *runnableIndex) { index.Manifests[1] = index.Manifests[0] }},
		{"extra descriptor", func(index *runnableIndex) { index.Manifests = append(index.Manifests, index.Manifests[0]) }},
		{"missing descriptor", func(index *runnableIndex) { index.Manifests = index.Manifests[:1] }},
		{"artifact descriptor", func(index *runnableIndex) { index.Manifests[0].ArtifactType = "application/example" }},
		{"nested index", func(index *runnableIndex) { index.Manifests[0].MediaType = MediaOCIIndex }},
		{"artifact root", func(index *runnableIndex) { index.ArtifactType = MediaPublicationRoot }},
		{"wrong size", func(index *runnableIndex) { index.Manifests[0].Size++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := writeIndexFixture(t, index)
			var changed runnableIndex
			if err := json.Unmarshal(mustReadImageFile(t, filepath.Join(root, "index.json")), &changed); err != nil {
				t.Fatal(err)
			}
			tc.mutate(&changed)
			raw, err := canonical.Marshal(changed)
			if err != nil {
				t.Fatal(err)
			}
			writeImageTestFile(t, filepath.Join(root, "index.json"), raw)
			_, err = OpenImageIndex(context.Background(), root, digestBytes(raw), imageIndexPlatforms)
			assertRegistryCode(t, err, e.CodeVerifyFailed)
		})
	}
	for _, tc := range []struct{ name, prefix string }{
		{"duplicate JSON key", `"schemaVersion":2,`},
		{"unknown external URL field", `"urls":["https://example.invalid"],`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := writeIndexFixture(t, index)
			raw := mustReadImageFile(t, filepath.Join(root, "index.json"))
			raw = append([]byte("{"+tc.prefix), raw[1:]...)
			writeImageTestFile(t, filepath.Join(root, "index.json"), raw)
			_, err := OpenImageIndex(context.Background(), root, digestBytes(raw), imageIndexPlatforms)
			assertRegistryCode(t, err, e.CodeVerifyFailed)
		})
	}
	root := writeIndexFixture(t, index)
	_, err := OpenImageIndex(context.Background(), root, "sha256:"+strings.Repeat("a", 64), imageIndexPlatforms)
	assertRegistryCode(t, err, e.CodeVerifyFailed)
}

func TestImageIndexHoldsClosureAndOwnsLifetime(t *testing.T) {
	t.Parallel()
	inputs := imageIndexInputs(t)
	index := assembledImageIndex(t, inputs)
	before, _ := index.Description()
	for _, input := range inputs {
		if err := os.Rename(input.LayoutPath, input.LayoutPath+"-moved"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(input.LayoutPath, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	root := writeIndexFixture(t, index)
	opened, err := OpenImageIndex(context.Background(), root, before.Digest, imageIndexPlatforms)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	if err := os.Rename(root, root+"-moved"); err != nil {
		t.Fatal(err)
	}
	writeImageTestFile(t, root, []byte("replacement"))
	copyDescription, _ := opened.Description()
	copyDescription.Children[0].Platform = "other"
	copyDescription.Children[0].Entrypoint[0] = "other"
	after, _ := opened.Description()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("caller changed held description")
	}
	writeIndexFixture(t, opened)
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = opened.Description()
	assertRegistryCode(t, err, e.CodeVerifyFailed)
	out := filepath.Join(t.TempDir(), "closed")
	assertRegistryCode(t, opened.WriteLayout(context.Background(), out), e.CodeVerifyFailed)
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		t.Fatalf("closed writer created output: %v", err)
	}
	_, err = (&ImageIndex{}).Description()
	assertRegistryCode(t, err, e.CodeVerifyFailed)
	_, err = (*ImageIndex)(nil).Description()
	assertRegistryCode(t, err, e.CodeUsage)
}

func TestImageIndexNoReplaceCancellationAndConcurrentClose(t *testing.T) {
	t.Parallel()
	index := assembledImageIndex(t, imageIndexInputs(t))
	parent := t.TempDir()
	out := filepath.Join(parent, "index")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assertRegistryCode(t, index.WriteLayout(ctx, out), e.CodeTimeout)
	_, err := AssembleImageIndex(ctx, imageIndexPlatforms, nil)
	assertRegistryCode(t, err, e.CodeTimeout)
	_, err = OpenImageIndex(ctx, "/nonexistent", "", imageIndexPlatforms)
	assertRegistryCode(t, err, e.CodeTimeout)
	if entries := mustReadImageDirectory(t, parent); len(entries) != 0 {
		t.Fatal("cancellation created partial output")
	}
	secondWriter := assembledImageIndex(t, imageIndexInputs(t))
	const count = 6
	results := make(chan error, count)
	var writers sync.WaitGroup
	for i := 0; i < count; i++ {
		writers.Add(1)
		writer := index
		if i%2 != 0 {
			writer = secondWriter
		}
		go func() { defer writers.Done(); results <- writer.WriteLayout(context.Background(), out) }()
	}
	writers.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else {
			assertRegistryCode(t, err, e.CodeVerifyFailed)
		}
	}
	if successes != 1 {
		t.Fatalf("%d writers succeeded", successes)
	}
	if entries := mustReadImageDirectory(t, parent); len(entries) != 1 {
		t.Fatalf("failed writers leaked stages: %v", entries)
	}
	before := mustReadImageFile(t, filepath.Join(out, "index.json"))
	assertRegistryCode(t, index.WriteLayout(context.Background(), out), e.CodeVerifyFailed)
	if !bytes.Equal(before, mustReadImageFile(t, filepath.Join(out, "index.json"))) {
		t.Fatal("existing output replaced")
	}
	// Separate output races Close; either a complete held closure is installed or
	// admission rejects the closed capability. Never use-after-close or partial.
	description, err := index.Description()
	if err != nil {
		t.Fatal(err)
	}
	another := filepath.Join(t.TempDir(), "index")
	done := make(chan error, 1)
	go func() { done <- index.WriteLayout(context.Background(), another) }()
	_ = index.Close()
	if err := <-done; err != nil {
		assertRegistryCode(t, err, e.CodeVerifyFailed)
		if _, statErr := os.Lstat(another); !os.IsNotExist(statErr) {
			t.Fatal("closed race left partial output")
		}
	} else {
		opened, err := OpenImageIndex(t.Context(), another, description.Digest, imageIndexPlatforms)
		if err != nil {
			t.Fatalf("successful concurrent write installed invalid closure: %v", err)
		}
		defer opened.Close()
	}
}

func rewriteImageChild(t *testing.T, input *ImageIndexInput, mutate func(*ociManifest, map[string]any)) {
	t.Helper()
	index, manifest := fixtureManifest(t, input.LayoutPath)
	oldConfig, oldManifest := manifest.Config.Digest, input.ManifestDigest
	var config map[string]any
	if err := json.Unmarshal(mustReadImageFile(t, filepath.Join(input.LayoutPath, "blobs", "sha256", strings.TrimPrefix(oldConfig, "sha256:"))), &config); err != nil {
		t.Fatal(err)
	}
	mutate(&manifest, config)
	writer := layoutWriter{root: input.LayoutPath}
	var err error
	manifest.Config, err = writer.addJSONBlob(config, manifest.Config.MediaType)
	if err != nil {
		t.Fatal(err)
	}
	index.Manifests[0], err = writer.addJSONBlob(manifest, MediaOCIManifest)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := canonical.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	writeImageTestFile(t, filepath.Join(input.LayoutPath, "index.json"), raw)
	input.ManifestDigest = index.Manifests[0].Digest
	for _, old := range []string{oldConfig, oldManifest} {
		if old != manifest.Config.Digest && old != input.ManifestDigest {
			if err := os.Remove(filepath.Join(input.LayoutPath, "blobs", "sha256", strings.TrimPrefix(old, "sha256:"))); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func writeImageTestFile(t *testing.T, path string, raw []byte) {
	t.Helper()
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustReadImageFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mustReadImageDirectory(t *testing.T, path string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

// Cancel only after observing the private stage, independent of the number of
// pre-stage context checks. The embedded context owns Done and cancellation.
type imageCancelContext struct {
	context.Context
	cancel context.CancelFunc
	parent string
	staged atomic.Bool
}

func (ctx *imageCancelContext) Err() error {
	entries, err := os.ReadDir(ctx.parent)
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() && strings.HasPrefix(entry.Name(), ".nvkr-image-index-") {
				ctx.staged.Store(true)
				ctx.cancel()
				break
			}
		}
	}
	return ctx.Context.Err()
}

func TestImageIndexCancellationCleansPrivateStage(t *testing.T) {
	t.Parallel()
	index := assembledImageIndex(t, imageIndexInputs(t))
	parent := t.TempDir()
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &imageCancelContext{Context: base, cancel: cancel, parent: parent}
	assertRegistryCode(t, index.WriteLayout(ctx, filepath.Join(parent, "index")), e.CodeTimeout)
	if !ctx.staged.Load() {
		t.Fatal("cancellation did not observe a private stage")
	}
	if entries := mustReadImageDirectory(t, parent); len(entries) != 0 {
		t.Fatalf("canceled copy leaked stage: %v", entries)
	}
	// Cancellation does not consume the held result.
	writeIndexFixture(t, index)
}

func TestImageIndexExactSubsetAndBaselineVariant(t *testing.T) {
	t.Parallel()
	input := imageIndexInputs(t)[1]
	rewriteImageChild(t, &input, func(_ *ociManifest, config map[string]any) { config["variant"] = "v8" })
	index, err := AssembleImageIndex(context.Background(), []string{"linux/arm64"}, []ImageIndexInput{input})
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	root := writeIndexFixture(t, index)
	var parsed runnableIndex
	if err := json.Unmarshal(mustReadImageFile(t, filepath.Join(root, "index.json")), &parsed); err != nil {
		t.Fatal(err)
	}
	parsed.Manifests[0].Platform.Variant = "v8"
	raw, err := canonical.Marshal(parsed)
	if err != nil {
		t.Fatal(err)
	}
	writeImageTestFile(t, filepath.Join(root, "index.json"), raw)
	opened, err := OpenImageIndex(context.Background(), root, digestBytes(raw), []string{"linux/arm64"})
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	_, err = OpenImageIndex(context.Background(), root, digestBytes(raw), imageIndexPlatforms)
	assertRegistryCode(t, err, e.CodeVerifyFailed)
}

func TestImageIndexCanceledWriterDoesNotWaitForActiveCopy(t *testing.T) {
	t.Parallel()
	index := assembledImageIndex(t, imageIndexInputs(t))
	// Hold the exact operation gate a large active layout copy owns, without
	// introducing a large fixture or depending on disk throughput.
	index.operation <- struct{}{}
	defer func() { <-index.operation }()
	parent := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waiting := make(chan struct{})
	queued := &imageWaitContext{Context: ctx, waiting: waiting}
	returned := make(chan error, 1)
	go func() {
		returned <- index.WriteLayout(queued, filepath.Join(parent, "index"))
	}()
	<-waiting
	cancel()
	select {
	case err := <-returned:
		assertRegistryCode(t, err, e.CodeTimeout)
	case <-time.After(time.Second):
		t.Fatal("canceled writer waited for an unrelated active copy")
	}
	if entries := mustReadImageDirectory(t, parent); len(entries) != 0 {
		t.Fatal("canceled waiter created output")
	}
}

// Done is evaluated by WriteLayout only after its initial admission, at the operation gate.
type imageWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (ctx *imageWaitContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.waiting) })
	return ctx.Context.Done()
}
