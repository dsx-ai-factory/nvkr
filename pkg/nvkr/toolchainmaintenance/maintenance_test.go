// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package toolchainmaintenance

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

func digest(value string) string { return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(value))) }

var now = time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)

func recordFixture() Record {
	r := Record{SchemaVersion: Schema, SourceCommit: strings.Repeat("a", 40), RunID: 123, Attempt: 1}
	for _, key := range imageKeys {
		parts := strings.Split(key, "/")
		im := Image{Arch: parts[0], Kind: parts[1], ArchiveDigest: digest(key + "archive"), ImageID: digest(key + "config"), ManifestDigest: digest(key + "manifest"), MeasurementDigest: digest(parts[0] + "measurement"), Exception: Exception{Approver: Owner, Scope: Scope, Expires: now.Add(time.Hour).Format(time.RFC3339), LicenseReview: "https://review.example/license", SecurityReview: "https://review.example/security"}}
		if im.Kind == "inspector" {
			im.Executables = map[string]string{}
			for field := range executablePaths {
				im.Executables[field] = digest(key + field)
			}
		}
		r.Images = append(r.Images, im)
	}
	return r
}

func assertCode(t *testing.T, err error, code e.Code) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != code {
		t.Fatalf("got %v, want code %s", err, code)
	}
}

func TestExceptionAdmission(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Record)
		code   e.Code
	}{
		{"complete", func(*Record) {}, ""},
		{"missing arch", func(r *Record) { r.Images = r.Images[:2] }, e.CodeInvalidCatalog},
		{"duplicate subject", func(r *Record) { r.Images[3] = r.Images[1] }, e.CodeReleaseIncomplete},
		{"unsorted", func(r *Record) { r.Images[0], r.Images[1] = r.Images[1], r.Images[0] }, e.CodeReleaseIncomplete},
		{"tag source", func(r *Record) { r.SourceCommit = "main" }, e.CodeInvalidCatalog},
		{"unknown schema", func(r *Record) { r.SchemaVersion = "next" }, e.CodeInvalidCatalog},
		{"missing approver", func(r *Record) { r.Images[0].Exception.Approver = "" }, e.CodeGateUnmet},
		{"expiry", func(r *Record) { r.Images[3].Exception.Expires = now.Format(time.RFC3339) }, e.CodeGateUnmet},
		{"unscoped approval", func(r *Record) { r.Images[0].Exception.Scope = "all-images" }, e.CodeGateUnmet},
		{"missing license", func(r *Record) { r.Images[0].Exception.LicenseReview = "" }, e.CodeGateUnmet},
		{"missing security", func(r *Record) { r.Images[0].Exception.SecurityReview = "" }, e.CodeGateUnmet},
		{"missing measurement", func(r *Record) { r.Images[0].MeasurementDigest = "" }, e.CodeUnpinnedInput},
		{"missing helper", func(r *Record) { delete(r.Images[0].Executables, "helperDigest") }, e.CodeVerifyFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := recordFixture()
			test.change(&r)
			err := r.validate(now, true)
			if test.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertCode(t, err, test.code)
			}
		})
	}
}

func TestPreparationRunAdmission(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*preparationRun)
	}{
		{"wrong source", func(r *preparationRun) { r.SHA = strings.Repeat("b", 40) }},
		{"fork branch", func(r *preparationRun) { r.Branch = "feature" }},
		{"different workflow", func(r *preparationRun) { r.Path = ".github/workflows/other.yml" }},
		{"failed arch", func(r *preparationRun) { r.Conclusion = "failure" }},
		{"old attempt", func(r *preparationRun) { r.Attempt = 2 }},
		{"pull request", func(r *preparationRun) { r.Event = "pull_request" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := recordFixture()
			run := preparationRun{r.RunID, r.Attempt, r.SourceCommit, "main", Workflow, "schedule", "completed", "success"}
			if err := validateRun(r, run); err != nil {
				t.Fatal(err)
			}
			test.change(&run)
			assertCode(t, validateRun(r, run), e.CodeGateUnmet)
		})
	}
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func archiveFixture(t *testing.T, root string) Record {
	t.Helper()
	r := recordFixture()
	for _, arch := range []string{"amd64", "arm64"} {
		dir := filepath.Join(root, arch)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		m := measurement{SchemaVersion: "nvkr.dev/native-toolchain-measurement/v2", Scope: "unqualified-diagnostic", SourceCommit: r.SourceCommit, Platform: "linux/" + arch, Archives: map[string]string{}}
		for _, kind := range []string{"inspector", "packer"} {
			im, err := mutate.ConfigFile(empty.Image, &v1.ConfigFile{OS: "linux", Architecture: arch, Config: v1.Config{Labels: map[string]string{"kind": kind}}})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, kind+".tar")
			ref, err := name.NewTag("example.invalid/" + kind + ":fixture")
			if err != nil {
				t.Fatal(err)
			}
			if err := tarball.WriteToFile(path, ref, im); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			m.Archives[kind] = digest(string(raw))
			id, err := im.ConfigName()
			if err != nil {
				t.Fatal(err)
			}
			if kind == "packer" {
				m.PackerImageID = id.String()
			} else {
				m.InspectorImageID = id.String()
			}
		}
		for _, field := range inspectorFields() {
			m.ExecutableMeasurements += strings.TrimPrefix(digest(arch+field), "sha256:") + "  " + executablePaths[field] + "\n"
		}
		writeJSON(t, filepath.Join(dir, "measurement.json"), m)
		described, err := Describe(t.Context(), dir, arch, r.SourceCommit)
		if err != nil {
			t.Fatal(err)
		}
		for _, im := range described {
			for i := range r.Images {
				if r.Images[i].Key() == im.Key() {
					im.Exception = r.Images[i].Exception
					r.Images[i] = im
				}
			}
		}
	}
	return r
}

func TestArchivesBindCompleteApprovalBeforePublication(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*testing.T, string, *Record)
	}{
		{"changed archive", func(t *testing.T, dir string, _ *Record) {
			f, err := os.OpenFile(filepath.Join(dir, "arm64/packer.tar"), os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = f.WriteString("tamper")
			_ = f.Close()
		}},
		{"substituted subject", func(_ *testing.T, _ string, r *Record) { r.Images[3].ManifestDigest = digest("other") }},
		{"swapped measurement", func(_ *testing.T, _ string, r *Record) { r.Images[2].MeasurementDigest = r.Images[0].MeasurementDigest }},
		{"changed executable", func(_ *testing.T, _ string, r *Record) { r.Images[2].Executables["helperDigest"] = digest("other") }},
		{"missing archive", func(t *testing.T, dir string, _ *Record) {
			if err := os.Rename(filepath.Join(dir, "arm64/packer.tar"), filepath.Join(dir, "unused.tar")); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			r := archiveFixture(t, dir)
			images, err := openApprovedImages(t.Context(), dir, r)
			if err != nil {
				t.Fatal(err)
			}
			closeImages(images)
			test.change(t, dir, &r)
			images, err = openApprovedImages(t.Context(), dir, r)
			if len(images) != 0 {
				closeImages(images)
				t.Fatal("partial admitted set escaped")
			}
			assertCode(t, err, e.CodeVerifyFailed)
		})
	}
}

func TestSnapshotKeepsOriginalAfterReplacement(t *testing.T) {
	dir := t.TempDir()
	r := archiveFixture(t, dir)
	im, err := openImage(t.Context(), filepath.Join(dir, "amd64"), "amd64", "packer", r.SourceCommit)
	if err != nil {
		t.Fatal(err)
	}
	defer im.file.Close()
	if err := os.WriteFile(filepath.Join(dir, "amd64/packer.tar"), []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyImage(t.Context(), im.image, im.description); err != nil {
		t.Fatal(err)
	}
}

func TestReadBackChecksNativeConfiguration(t *testing.T) {
	dir := t.TempDir()
	r := archiveFixture(t, dir)
	im, err := openImage(t.Context(), filepath.Join(dir, "amd64"), "amd64", "packer", r.SourceCommit)
	if err != nil {
		t.Fatal(err)
	}
	defer im.file.Close()
	for _, field := range []string{"manifest", "config", "platform"} {
		t.Run(field, func(t *testing.T) {
			expected := im.description
			switch field {
			case "manifest":
				expected.ManifestDigest = digest("different")
			case "config":
				expected.ImageID = digest("different")
			case "platform":
				expected.Arch = "arm64"
			}
			assertCode(t, verifyImage(t.Context(), im.image, expected), e.CodeVerifyFailed)
		})
	}
}

type readbackImage struct {
	v1.Image
	layer v1.Layer
}

func (im readbackImage) Layers() ([]v1.Layer, error) { return []v1.Layer{im.layer}, nil }

type readbackLayer struct {
	v1.Layer
	body *readbackBody
}

func (layer readbackLayer) Compressed() (io.ReadCloser, error) { return layer.body, nil }

type readbackBody struct {
	*bytes.Reader
	readErr, closeErr error
	read              int
	closed            bool
}

func (body *readbackBody) Read(p []byte) (int, error) {
	if body.readErr != nil {
		return 0, body.readErr
	}
	n, err := body.Reader.Read(p)
	body.read += n
	return n, err
}

func (body *readbackBody) Close() error {
	body.closed = true
	return body.closeErr
}

func TestReadBackChecksCompressedLayerBytes(t *testing.T) {
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	if err := tw.WriteHeader(&tar.Header{Name: "fixture", Mode: 0600, Size: 4}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("data")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(archive.Bytes())), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := layer.Compressed()
	if err != nil {
		t.Fatal(err)
	}
	compressed, err := io.ReadAll(body)
	closeErr := body.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("read compressed fixture: %v, close: %v", err, closeErr)
	}
	im, err := mutate.ConfigFile(empty.Image, &v1.ConfigFile{OS: "linux", Architecture: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	im, err = mutate.AppendLayers(im, layer)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := im.RawManifest()
	if err != nil {
		t.Fatal(err)
	}
	config, err := im.RawConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	expected := Image{Arch: "amd64", ManifestDigest: digest(string(manifest)), ImageID: digest(string(config))}
	for _, test := range []struct {
		name              string
		change            func([]byte) []byte
		readErr, closeErr error
	}{
		{"valid", func(b []byte) []byte { return b }, nil, nil},
		{"truncated", func(b []byte) []byte { return b[:len(b)-1] }, nil, nil},
		{"altered", func(b []byte) []byte { b[len(b)/2] ^= 1; return b }, nil, nil},
		{"oversized", func(b []byte) []byte { return append(b, make([]byte, 64)...) }, nil, nil},
		{"read error", func(b []byte) []byte { return b }, errors.New("synthetic read failure"), nil},
		{"close error", func(b []byte) []byte { return b }, nil, errors.New("synthetic close failure")},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := &readbackBody{Reader: bytes.NewReader(test.change(bytes.Clone(compressed))), readErr: test.readErr, closeErr: test.closeErr}
			err := verifyImage(t.Context(), readbackImage{im, readbackLayer{layer, body}}, expected)
			if test.name == "valid" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertCode(t, err, e.CodeVerifyFailed)
			}
			if !body.closed || body.read > len(compressed)+1 {
				t.Fatalf("readback did not close or bound the stream: closed=%v bytes=%d", body.closed, body.read)
			}
		})
	}
}

func TestUnsupportedPublicationHasNoInputWork(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "")
	assertCode(t, Publish(t.Context(), "/does-not-exist", "123", "/does-not-exist"), e.CodeEngineCapability)
	_, err := Repin(t.Context(), "/does-not-exist", "123")
	assertCode(t, err, e.CodeEngineCapability)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := snapshotArchive(ctx, "/does-not-exist"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation before file work: %v", err)
	}
}

func TestApprovalParserRejectsAmbiguousData(t *testing.T) {
	for _, test := range []struct{ name, raw string }{
		{"unknown field", `{"unknown":true}`},
		{"trailing value", `{} {}`},
		{"duplicate field", `{"runID":1,"runID":2}`},
		{"case alias", `{"runID":1,"runid":2}`},
		{"escaped case alias", `{"runID":1,"\u0072unid":2}`},
		{"image in array", `{"images":[{"arch":"amd64","ARCH":"arm64"}]}`},
		{"nested exception", `{"images":[{"exception":{"scope":"internal-m2-candidate-only","SCOPE":"other"}}]}`},
		{"unicode fold", `{"images":[{"exception":{"scope":"internal-m2-candidate-only","ſcope":"other"}}]}`},
		{"measurement map", `{"images":[{"executables":{"codecDigest":"one","CODECDIGEST":"two"}}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var r Record
			assertCode(t, decode([]byte(test.raw), &r), e.CodeInvalidCatalog)
		})
	}
	raw, err := json.MarshalIndent(recordFixture(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	var r Record
	if err := decode(raw, &r); err != nil {
		t.Fatal(err)
	}
	if err := r.validate(now, true); err != nil {
		t.Fatal(err)
	}
}

func isolateGitEnvironment(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GIT_") {
			t.Setenv(key, "") // Restore the caller's value at test cleanup.
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
}

func TestRepinFixturePreservesForeignIndex(t *testing.T) {
	isolateGitEnvironment(t)
	foreign := t.TempDir()
	if err := os.WriteFile(filepath.Join(foreign, "sentinel"), []byte("keep staged\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "--quiet"}, {"add", "sentinel"}} {
		if _, err := command(t.Context(), foreign, "git", args...); err != nil {
			t.Fatal(err)
		}
	}
	gitDir := filepath.Join(foreign, ".git")
	indexPath := filepath.Join(gitDir, "index")
	before, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		env  map[string]string
	}{
		{"git dir only", map[string]string{"GIT_DIR": gitDir}},
		{"index only", map[string]string{"GIT_INDEX_FILE": indexPath}},
		{"all routing", map[string]string{"GIT_DIR": gitDir, "GIT_WORK_TREE": foreign, "GIT_COMMON_DIR": gitDir, "GIT_INDEX_FILE": indexPath, "GIT_OBJECT_DIRECTORY": filepath.Join(gitDir, "objects"), "GIT_ALTERNATE_OBJECT_DIRECTORIES": filepath.Join(gitDir, "objects")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Run("fixture", func(t *testing.T) {
				for key, value := range test.env {
					t.Setenv(key, value)
				}
				t.Run("isolated setup", TestRepinRebindsOverlayAndInspectorProfile)
			})
			t.Run("runtime", func(t *testing.T) {
				testRepinFixture(t, func() {
					for key, value := range test.env {
						t.Setenv(key, value)
					}
				})
			})
			after, err := os.ReadFile(indexPath)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("fixture changed foreign index: %v", err)
			}
		})
	}
}

func TestRepinRebindsOverlayAndInspectorProfile(t *testing.T) {
	testRepinFixture(t, nil)
}

func TestRepinRebindsSharedToolchainDeclaration(t *testing.T) {
	testRepinFixture(t, nil, true)
}

func testRepinFixture(t *testing.T, beforeRepin func(), shared ...bool) {
	t.Helper()
	isolateGitEnvironment(t)
	root := t.TempDir()
	r := recordFixture()
	registry := map[string]any{"apiVersion": "nvkr.dev/catalog/v3", "kind": "SourceBuildRegistry", "metadata": map[string]any{"name": "fixture"}, "sources": map[string]any{"tool": map[string]any{"repo": "https://example.invalid/tool", "constraint": "1.x", "toolchainImagesByArch": map[string]string{"arm64": Registry + "/packer-native@" + digest("old-arm")}}}, "components": map[string]any{"tool": map[string]any{"fromSource": true, "source": "tool", "platforms": []string{"linux/arm64"}, "outputType": "host-package", "artifactPath": "tool", "buildCommand": []string{"go", "build", "-o", "tool", "."}}}}
	baseDir := filepath.Join(root, "inputs/catalogs/fixture")
	wantChanges := 3
	if len(shared) > 0 && shared[0] {
		fragmentPath := filepath.Join(root, "inputs/catalogs/shared/toolchain.yaml")
		writeJSON(t, fragmentPath, map[string]any{"apiVersion": "nvkr.dev/catalog-fragment/v1", "kind": "CatalogFragment", "sources": registry["sources"]})
		fragment, err := os.ReadFile(fragmentPath)
		if err != nil {
			t.Fatal(err)
		}
		delete(registry, "sources")
		registry["imports"] = []any{map[string]any{"path": "../shared/toolchain.yaml", "sha256": fmt.Sprintf("%x", sha256.Sum256(fragment)), "sources": map[string]string{"tool": "tool"}}}
		wantChanges++
	}
	writeJSON(t, filepath.Join(baseDir, "registry.yaml"), registry)
	writeJSON(t, filepath.Join(baseDir, "pins.lock.yaml"), map[string]any{"apiVersion": "nvkr.dev/lock/v1", "kind": "PinLock", "pins": map[string]any{"tool": map[string]any{"commit": strings.Repeat("a", 40), "tag": "v1.0.0", "treeHash": strings.Repeat("b", 40), "sourceDateEpoch": 1}}})
	base, err := catalog.LoadContext(t.Context(), catalog.Dir(baseDir))
	if err != nil {
		t.Fatal(err)
	}
	baseDigest, err := base.SnapshotDigest()
	if err != nil {
		t.Fatal(err)
	}
	component := base.Components["tool"]
	overlay := catalog.Overlay{APIVersion: catalog.OverlayAPIVersion, Kind: catalog.OverlayKind, Name: "replacement", RequiresCatalogDigest: baseDigest, Components: map[string]catalog.ComponentReplacement{"tool": {Component: &component}}}
	overlayPath := filepath.Join(root, "inputs/catalogs/variants/overlay.yaml")
	writeJSON(t, overlayPath, overlay)
	prependComment(t, overlayPath, "predecessor "+baseDigest)
	profile := map[string]string{"image": Registry + "/inspector-native@" + digest("old-inspector")}
	for field := range executablePaths {
		if field != "helperDigest" {
			profile[field] = digest("old" + field)
		}
	}
	oldInspector := profile["image"]
	envPath := filepath.Join(root, "inputs/environments/test.yaml")
	writeJSON(t, envPath, map[string]any{"diskInspectionProfiles": map[string]any{"linux/arm64": profile}, "note": "Inspector reference: " + oldInspector})
	prependComment(t, envPath, "inspector "+oldInspector)
	if _, err := command(t.Context(), root, "git", "init", "--quiet"); err != nil {
		t.Fatal(err)
	}
	if _, err := command(t.Context(), root, "git", "add", "inputs"); err != nil {
		t.Fatal(err)
	}
	if beforeRepin != nil {
		beforeRepin()
	}
	// Planning must evaluate proposed fragments without writing them or the lock.
	beforeFiles := map[string][]byte{}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		raw, err := os.ReadFile(path)
		beforeFiles[path] = raw
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	planned, err := repin(t.Context(), root, r, false)
	if err != nil {
		t.Fatal(err)
	}
	for path, before := range beforeFiles {
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("planning changed %s: %v", path, err)
		}
	}
	changed, err := repin(t.Context(), root, r, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != wantChanges || !slices.IsSorted(changed) || !slices.Equal(planned, changed) {
		t.Fatalf("incomplete changes: %v", changed)
	}
	lockPath := filepath.Join(baseDir, catalog.LockFile)
	lockAfter, err := os.ReadFile(lockPath)
	if err != nil || !bytes.Equal(beforeFiles[lockPath], lockAfter) {
		t.Fatalf("repin changed source lock: %v", err)
	}
	updated, err := catalog.LoadContext(t.Context(), catalog.Dir(baseDir))
	if err != nil {
		t.Fatal(err)
	}
	updatedOverlay, err := catalog.LoadOverlayContext(t.Context(), overlayPath)
	if err != nil {
		t.Fatal(err)
	}
	stack, err := catalog.ApplyStack(updated, updatedOverlay)
	if err != nil {
		t.Fatal(err)
	}
	packer, _ := selectedImage(r, "arm64", "packer")
	if updated.Components["tool"].ToolchainImages["arm64"] != packer.Reference() {
		t.Fatal("base did not consume the proposed shared toolchain declaration")
	}
	if stack.Catalog.Components["tool"].ToolchainImages["arm64"] != packer.Reference() {
		t.Fatal("overlay did not move with base")
	}
	raw, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	inspector, _ := selectedImage(r, "arm64", "inspector")
	if !bytesContainAll(raw, inspector.Reference(), inspector.Executables["scannerDigest"], "# inspector "+inspector.Reference(), "Inspector reference: "+inspector.Reference()) || bytes.Contains(raw, []byte(oldInspector)) {
		t.Fatal("profile did not bind image and measurement")
	}
	overlayRaw, err := os.ReadFile(overlayPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(overlayRaw, []byte("# predecessor "+updatedOverlay.RequiresCatalogDigest)) || bytes.Contains(overlayRaw, []byte(baseDigest)) {
		t.Fatal("predecessor comment did not move with the authenticated digest")
	}
	changed, err = repin(t.Context(), root, r, true)
	if err != nil || len(changed) != 0 {
		t.Fatalf("repin not idempotent: %v %v", changed, err)
	}
}

func prependComment(t *testing.T, path, comment string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append([]byte("# "+comment+"\n"), raw...), 0600); err != nil {
		t.Fatal(err)
	}
}

func bytesContainAll(raw []byte, values ...string) bool {
	for _, value := range values {
		if !strings.Contains(string(raw), value) {
			return false
		}
	}
	return true
}

func TestCommandOutputCannotBypassBoundThroughReaderFrom(t *testing.T) {
	var output limitedBuffer
	_, err := io.Copy(&output, strings.NewReader(strings.Repeat("x", maxRecordBytes+1)))
	if err == nil || len(output.Bytes()) > maxRecordBytes {
		t.Fatal("command output bypassed bound")
	}
}

// This repository inventory also authenticates source pin sets and can require
// private NVKE access; ordinary contract tests stay offline.
func TestMaintainedInputsRepinPreview(t *testing.T) {
	if os.Getenv("NVKR_TEST_MAINTAINED_TOOLCHAINS") != "1" {
		t.Skip("requires private catalog source access")
	}
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	changed, err := repin(t.Context(), root, recordFixture(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) < 10 {
		t.Fatalf("incomplete maintained input discovery: %v", changed)
	}
	t.Logf("would update %d authored files", len(changed))
}
