// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package toolchainmaintenance

import (
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

func TestRepinRebindsOverlayAndInspectorProfile(t *testing.T) {
	root := t.TempDir()
	r := recordFixture()
	registry := map[string]any{"apiVersion": "nvkr.dev/catalog/v3", "kind": "SourceBuildRegistry", "metadata": map[string]any{"name": "fixture"}, "sources": map[string]any{"tool": map[string]any{"repo": "https://example.invalid/tool", "constraint": "1.x", "toolchainImagesByArch": map[string]string{"arm64": Registry + "/packer-native@" + digest("old-arm")}}}, "components": map[string]any{"tool": map[string]any{"fromSource": true, "source": "tool", "platforms": []string{"linux/arm64"}, "outputType": "host-package", "artifactPath": "tool", "buildCommand": []string{"go", "build", "-o", "tool", "."}}}}
	baseDir := filepath.Join(root, "inputs/catalogs/fixture")
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
	overlayPath := filepath.Join(root, "inputs/catalogs/variants/overlay.json")
	writeJSON(t, overlayPath, overlay)
	profile := map[string]string{"image": Registry + "/inspector-native@" + digest("old-inspector")}
	for field := range executablePaths {
		if field != "helperDigest" {
			profile[field] = digest("old" + field)
		}
	}
	envPath := filepath.Join(root, "inputs/environments/test.json")
	writeJSON(t, envPath, map[string]any{"diskInspectionProfiles": map[string]any{"linux/arm64": profile}})
	if _, err := command(t.Context(), root, "git", "init", "--quiet"); err != nil {
		t.Fatal(err)
	}
	if _, err := command(t.Context(), root, "git", "add", "inputs"); err != nil {
		t.Fatal(err)
	}
	changed, err := repin(t.Context(), root, r, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 3 || !slices.IsSorted(changed) {
		t.Fatalf("incomplete changes: %v", changed)
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
	if stack.Catalog.Components["tool"].ToolchainImages["arm64"] != packer.Reference() {
		t.Fatal("overlay did not move with base")
	}
	raw, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	inspector, _ := selectedImage(r, "arm64", "inspector")
	if !bytesContainAll(raw, inspector.Reference(), inspector.Executables["scannerDigest"]) {
		t.Fatal("profile did not bind image and measurement")
	}
	changed, err = repin(t.Context(), root, r, true)
	if err != nil || len(changed) != 0 {
		t.Fatalf("repin not idempotent: %v %v", changed, err)
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
