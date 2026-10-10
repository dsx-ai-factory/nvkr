// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/diskdiagnostic"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/google/go-containerregistry/pkg/v1/layout"
)

func TestDirectOCIMaterialAuthenticatesClosureBeforeScanner(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		foreign := map[string]string{"amd64": "arm64", "arm64": "amd64"}[architecture]
		mutateBlob := func(role string, missing bool) func(string) {
			return func(root string) {
				lp := layout.Path(root)
				index, err := lp.ImageIndex()
				if err != nil {
					t.Fatal(err)
				}
				membership, err := index.IndexManifest()
				if err != nil || len(membership.Manifests) != 1 {
					t.Fatalf("fixture membership: %v", err)
				}
				digest := membership.Manifests[0].Digest
				if role == "config" {
					image, err := lp.Image(digest)
					if err != nil {
						t.Fatal(err)
					}
					manifest, err := image.Manifest()
					if err != nil {
						t.Fatal(err)
					}
					digest = manifest.Config.Digest
				}
				filename := filepath.Join(root, "blobs", digest.Algorithm, digest.Hex)
				if missing {
					err = os.Remove(filename)
				} else {
					err = os.WriteFile(filename, []byte("corrupt"), 0o644)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
		}
		for _, test := range []struct {
			name       string
			images     []string
			change     func(string)
			markers    map[string]string
			compressed bool
			code       e.Code
			message    string
			scans      int
		}{
			{name: "native", images: []string{architecture}, scans: 1},
			{name: "multiple native images", images: []string{architecture, architecture}, scans: 2},
			{name: "foreign config", images: []string{foreign}, code: e.CodeVerifyFailed, message: "platform differs"},
			{name: "mixed native and foreign", images: []string{architecture, foreign}, code: e.CodeVerifyFailed, message: "platform differs"},
			{name: "missing manifest", images: []string{architecture}, change: mutateBlob("manifest", true), code: e.CodeVerifyFailed, message: "nested OCI closure"},
			{name: "corrupt manifest", images: []string{architecture}, change: mutateBlob("manifest", false), code: e.CodeVerifyFailed, message: "nested OCI closure"},
			{name: "missing config", images: []string{architecture}, change: mutateBlob("config", true), code: e.CodeVerifyFailed, message: "nested OCI closure"},
			{name: "corrupt config", images: []string{architecture}, change: mutateBlob("config", false), code: e.CodeVerifyFailed, message: "nested OCI closure"},
			{name: "unreferenced blob", images: []string{architecture}, change: func(root string) {
				payload := []byte("unreferenced OCI blob")
				if err := os.WriteFile(filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(rawDigest(payload), "sha256:")), payload, 0o644); err != nil {
					t.Fatal(err)
				}
			}, code: e.CodeVerifyFailed, message: "nested OCI closure"},
			{name: "mixed transport markers", markers: map[string]string{"oci-layout": `{"imageLayoutVersion":"1.0.0"}`, "manifest.json": "[]"}, code: e.CodeEngineCapability, message: "mixed OCI and Docker"},
			{name: "docker only", markers: map[string]string{"manifest.json": "[]"}, code: e.CodeVerifyFailed, message: "unmixed OCI layout"},
			{name: "markerless", markers: map[string]string{"ordinary": "payload"}, code: e.CodeVerifyFailed, message: "unmixed OCI layout"},
			{name: "gzip OCI layout", images: []string{architecture}, compressed: true, code: e.CodeVerifyFailed, message: "decoding compressed material tar headers"},
		} {
			t.Run(architecture+"/"+test.name, func(t *testing.T) {
				if test.scans > 0 && runtime.GOOS != "linux" {
					t.Skip("controlled held scanner execution requires Linux; rejection cases still run")
				}
				var archive *generatedInspectionFile
				if test.markers == nil {
					archive = nestedOCIFixture(t, test.images, test.compressed, test.change)
				} else {
					var raw bytes.Buffer
					writer := tar.NewWriter(&raw)
					for _, name := range []string{"oci-layout", "manifest.json", "ordinary"} {
						payload, present := test.markers[name]
						if !present {
							continue
						}
						if err := writer.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(payload))}); err != nil {
							t.Fatal(err)
						}
						if _, err := io.WriteString(writer, payload); err != nil {
							t.Fatal(err)
						}
					}
					if err := writer.Close(); err != nil {
						t.Fatal(err)
					}
					var err error
					archive, err = createGeneratedInspectionFile(t.Context(), 1<<20, func(dst io.Writer) error { _, err := dst.Write(raw.Bytes()); return err })
					if err != nil {
						t.Fatal(err)
					}
					defer archive.Close()
				}
				subject, err := heldfile.FromReaderContext(t.Context(), io.NewSectionReader(archive, 0, archive.Size()), heldfile.Options{Label: "direct-oci-closure", MaxBytes: 1 << 20})
				if err != nil {
					t.Fatal(err)
				}
				defer subject.Close()
				marker := filepath.Join(t.TempDir(), "calls")
				script := []byte("#!/bin/sh\nprintf x >> '" + marker + "'\nprintf '%s' '{\"schema\":{\"version\":\"16.1.10\"},\"artifacts\":[{\"name\":\"observed\",\"version\":\"1\",\"type\":\"deb\",\"metadata\":{\"architecture\":\"all\"}}]}'\n")
				filename := filepath.Join(t.TempDir(), "scanner")
				if err := os.WriteFile(filename, script, 0o755); err != nil {
					t.Fatal(err)
				}
				components, err := inspectContainerMaterial(t.Context(), subject, "linux/"+architecture, Scanner{Path: filename, Digest: rawDigest(script)}, 1<<20)
				if test.code == "" {
					if err != nil || len(components) != test.scans {
						t.Fatalf("complete native inventory: components=%d error=%v", len(components), err)
					}
				} else {
					assertDependencyCode(t, err, test.code)
					if !strings.Contains(err.Error(), test.message) {
						t.Fatalf("wrong rejection boundary: %v", err)
					}
					if components != nil {
						t.Fatal("failed admission returned partial inventory")
					}
				}
				calls, err := os.ReadFile(marker)
				if err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				if len(calls) != test.scans {
					t.Fatalf("scanner calls=%d want=%d", len(calls), test.scans)
				}
				for _, component := range components {
					class, err := diskComponentClass(component)
					if err != nil || class != diskClassContainer {
						t.Fatalf("OCI observation class=%q error=%v", class, err)
					}
				}
			})
		}
	}
}

func TestDirectOCIMaterialAdmissionBeforeWork(t *testing.T) {
	subject, err := heldfile.FromBytesContext(t.Context(), []byte("subject"), heldfile.Options{Label: "invalid-oci-admission", MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	defer subject.Close()
	for _, test := range []struct {
		name     string
		subject  *heldfile.File
		max      int64
		platform string
	}{
		{"nil", nil, 1024, "linux/amd64"},
		{"empty subject", &heldfile.File{}, 1024, "linux/amd64"},
		{"zero budget", subject, 0, "linux/amd64"},
		{"negative budget", subject, -1, "linux/amd64"},
		{"oversize", subject, 1, "linux/amd64"},
		{"unbounded", subject, (16 << 30) + 1, "linux/amd64"},
		{"unsupported linux", subject, 1024, "linux/s390x"},
		{"unsupported OS", subject, 1024, "darwin/arm64"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var progress bytes.Buffer
			components, err := inspectContainerMaterial(diskdiagnostic.WithWriter(t.Context(), &progress), test.subject, test.platform, Scanner{Path: "must-not-execute", Digest: ""}, test.max)
			assertDependencyCode(t, err, e.CodeEngineCapability)
			if components != nil || progress.Len() != 0 || !strings.Contains(err.Error(), "bounded held subject and consumer platform") {
				t.Fatalf("admission did work: %v", err)
			}
		})
	}
}

func TestOCIMarkerCancellationPreservesTimeout(t *testing.T) {
	archive := nestedOCIFixture(t, []string{"amd64"}, false, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	docker, oci, err := compressedImageMarkers(ctx, archive)
	assertDependencyCode(t, err, e.CodeTimeout)
	if docker || oci {
		t.Fatal("canceled marker scan returned selection")
	}
}

type cancelOCIPhaseWriter struct {
	bytes.Buffer
	phase     string
	remaining int
	cancel    context.CancelFunc
}

func (writer *cancelOCIPhaseWriter) Write(raw []byte) (int, error) {
	if strings.Contains(string(raw), `"phase":"`+writer.phase+`","event":"start"`) {
		writer.remaining--
		if writer.remaining == 0 {
			writer.cancel()
		}
	}
	return writer.Buffer.Write(raw)
}

func TestDirectOCIMaterialPhaseCancellation(t *testing.T) {
	for _, test := range []struct {
		name, phase, message string
		occurrence           int
	}{
		{"copy admission", "subject-copy", "building verified SBOM evidence", 1},
		{"marker", "subject-revalidation", "compressed material marker scan canceled", 1},
		{"extraction", "filesystem-extraction", "extracting nested OCI layout canceled", 1},
		{"per-image sealing", "scanner-transport", "sealing nested OCI scan transport canceled", 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			archive := nestedOCIFixture(t, []string{"amd64"}, false, nil)
			subject, err := heldfile.FromReaderContext(t.Context(), io.NewSectionReader(archive, 0, archive.Size()), heldfile.Options{Label: "canceled-oci-material", MaxBytes: 1 << 20})
			if err != nil {
				t.Fatal(err)
			}
			defer subject.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			writer := &cancelOCIPhaseWriter{phase: test.phase, remaining: test.occurrence, cancel: cancel}
			components, err := inspectContainerMaterial(diskdiagnostic.WithWriter(ctx, writer), subject, "linux/amd64", Scanner{Path: "must-not-execute"}, 1<<20)
			assertDependencyCode(t, err, e.CodeTimeout)
			if writer.remaining != 0 || components != nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("did not cancel the intended phase: %v", err)
			}
		})
	}
}
