// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestReadHelmChartSharesExactExportAdmissionWithoutWrites(t *testing.T) {
	for _, tc := range []struct {
		name string
		code e.Code
	}{
		{"exact", ""}, {"empty config", e.CodeVerifyFailed}, {"wrong layer media", e.CodeVerifyFailed},
		{"config differs", e.CodeVerifyFailed}, {"wrong manifest", e.CodeVerifyFailed},
		{"missing pin", e.CodeUsage}, {"wrong name", e.CodeVerifyFailed}, {"wrong version", e.CodeVerifyFailed},
		{"canceled", e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			layout := filepath.Join(root, "layout")
			data := helmChartArchiveFixture(t, map[string]string{"demo/Chart.yaml": "apiVersion: v2\nname: demo\nversion: 1.2.3\n"})
			if _, err := BuildHelmChartArtifact(t.Context(), layout, "application/vnd.nvkr.helm-chart.v1", data); err != nil {
				t.Fatal(err)
			}
			descriptor, err := PayloadDescriptor(t.Context(), layout)
			if err != nil {
				t.Fatal(err)
			}
			targetDiagnostic := ""
			if tc.name == "empty config" || tc.name == "wrong layer media" || tc.name == "config differs" {
				manifest, err := flatManifest(t.Context(), layout, PublishedObject{DescriptorDigest: descriptor.Digest, Size: descriptor.Size})
				if err != nil {
					t.Fatal(err)
				}
				oldManifest, oldConfig := descriptor.Digest, manifest.Config.Digest
				writer := layoutWriter{root: layout}
				targetDiagnostic = "selected artifact is not an exact single-chart Helm OCI payload"
				switch tc.name {
				case "empty config":
					manifest.Config, err = writer.addBlobBytes([]byte("{}"), MediaEmptyConfig)
				case "wrong layer media":
					manifest.Layers[0].MediaType = MediaOctetStream
				case "config differs":
					targetDiagnostic = "chart config and archive metadata differ"
					config, readErr := helmChartMetadata(data)
					if readErr != nil {
						t.Fatal(readErr)
					}
					config.Name = "sibling"
					manifest.Config, err = writer.addJSONBlob(config, MediaHelmConfig)
				}
				if err != nil {
					t.Fatal(err)
				}
				descriptor, err = writer.addJSONBlob(manifest, MediaOCIManifest)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = writer.writeIndex(ociIndex{SchemaVersion: 2, MediaType: MediaOCIIndex, Manifests: []Descriptor{descriptor}}); err != nil {
					t.Fatal(err)
				}
				// Remove only this fixture's now-unreachable old records so these
				// negative cases reach Helm media/metadata admission, not inventory.
				obsolete := []string{oldManifest}
				if manifest.Config.Digest != oldConfig {
					obsolete = append(obsolete, oldConfig)
				}
				for _, digest := range obsolete {
					if err := os.Remove(filepath.Join(layout, "blobs", "sha256", strings.TrimPrefix(digest, "sha256:"))); err != nil {
						t.Fatal(err)
					}
				}
				admitted, err := PayloadDescriptor(t.Context(), layout)
				if err != nil || admitted.Digest != descriptor.Digest {
					t.Fatalf("mutated fixture is not a valid complete OCI closure: %+v %v", admitted, err)
				}
			}
			pin, chart, version := descriptor.Digest, "demo", "1.2.3"
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch tc.name {
			case "wrong manifest":
				pin = digestBytes([]byte("substituted"))
			case "missing pin":
				pin = ""
			case "wrong name":
				chart = "sibling"
			case "wrong version":
				version = "2.0.0"
			case "canceled":
				cancel()
			}
			result, held, err := ReadHelmChart(ctx, layout, pin, chart, version)
			if tc.code != "" {
				assertRegistryCode(t, err, tc.code)
				if targetDiagnostic != "" && !strings.Contains(err.Error(), targetDiagnostic) {
					t.Fatalf("fixture did not reach its intended Helm admission: %v", err)
				}
				if held != nil || result != (HelmChartExport{}) {
					t.Fatal("failed admission returned a successful description or bytes")
				}
			} else if err != nil || !bytes.Equal(held, data) || result.ManifestDigest != pin || result.ArchiveDigest != digestBytes(data) || result.Size != int64(len(data)) {
				t.Fatalf("read did not return exact verified diagnostic bytes: %+v %v", result, err)
			}
			entries, readErr := os.ReadDir(root)
			if readErr != nil || len(entries) != 1 || entries[0].Name() != "layout" {
				t.Fatalf("read-only admission created output: %v %v", entries, readErr)
			}
			if tc.code != e.CodeUsage {
				out := filepath.Join(root, "export", "demo.tgz")
				exported, exportErr := ExportHelmChart(ctx, layout, pin, chart, version, out)
				if tc.code != "" {
					assertRegistryCode(t, exportErr, tc.code)
					if targetDiagnostic != "" && !strings.Contains(exportErr.Error(), targetDiagnostic) {
						t.Fatalf("export did not share its intended Helm admission: %v", exportErr)
					}
					if _, err := os.Stat(filepath.Dir(out)); !os.IsNotExist(err) {
						t.Fatalf("failed export created output: %v", err)
					}
				} else if exportErr != nil || exported != result {
					t.Fatalf("read/export admission diverged: %+v %+v %v", result, exported, exportErr)
				}
			}
		})
	}
}

func TestExportHelmChart(t *testing.T) {
	for _, tc := range []struct {
		name, chart, version string
		code                 e.Code
	}{
		{"exact bytes", "demo", "1.2.3", ""},
		{"wrong chart", "sibling", "1.2.3", e.CodeVerifyFailed},
		{"wrong version", "demo", "2.0.0", e.CodeVerifyFailed},
		{"wrong manifest", "demo", "1.2.3", e.CodeVerifyFailed},
		{"missing pin", "demo", "1.2.3", e.CodeUsage},
		{"canceled", "demo", "1.2.3", e.CodeTimeout},
		{"substituted bytes", "demo", "1.2.3", e.CodeVerifyFailed},
		{"existing output", "demo", "1.2.3", e.CodeImmutableViolation},
		{"symlink output", "demo", "1.2.3", e.CodeImmutableViolation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			data := helmChartArchiveFixture(t, map[string]string{"demo/Chart.yaml": "apiVersion: v2\nname: demo\nversion: 1.2.3\n"})
			layout := filepath.Join(root, "layout")
			if _, err := BuildHelmChartArtifact(t.Context(), layout, "application/vnd.nvkr.helm-chart.v1", data); err != nil {
				t.Fatal(err)
			}
			descriptor, err := PayloadDescriptor(t.Context(), layout)
			if err != nil {
				t.Fatal(err)
			}
			pin := descriptor.Digest
			out := filepath.Join(root, "out", "chart.tgz")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch tc.name {
			case "wrong manifest":
				pin = digestBytes([]byte("different"))
			case "missing pin":
				pin = ""
			case "canceled":
				cancel()
			case "substituted bytes":
				blob := filepath.Join(layout, "blobs", "sha256", strings.TrimPrefix(digestBytes(data), "sha256:"))
				if err := os.Chmod(blob, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(blob, []byte("tampered"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "existing output", "symlink output":
				if err := os.Mkdir(filepath.Dir(out), 0o700); err != nil {
					t.Fatal(err)
				}
				if tc.name == "existing output" {
					err = os.WriteFile(out, []byte("retain"), 0o600)
				} else {
					err = os.Symlink(filepath.Join(root, "must-not-exist"), out)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			result, err := ExportHelmChart(ctx, layout, pin, tc.chart, tc.version, out)
			if tc.code != "" {
				assertRegistryCode(t, err, tc.code)
				if result != (HelmChartExport{}) {
					t.Fatal("failure returned a successful observation")
				}
				if tc.name == "existing output" {
					got, err := os.ReadFile(out)
					if err != nil || string(got) != "retain" {
						t.Fatalf("existing output changed: %q %v", got, err)
					}
				} else if tc.name == "symlink output" {
					if _, err := os.Stat(filepath.Join(root, "must-not-exist")); !os.IsNotExist(err) {
						t.Fatalf("followed output symlink: %v", err)
					}
				} else if _, err := os.Stat(filepath.Dir(out)); !os.IsNotExist(err) {
					t.Fatalf("rejected chart created output directory: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(out)
			if err != nil || !bytes.Equal(got, data) || result.ManifestDigest != pin || result.ArchiveDigest != digestBytes(data) || result.Size != int64(len(data)) || result.Name != tc.chart || result.Version != tc.version {
				t.Fatalf("not exact unchanged chart: %+v, %v", result, err)
			}
		})
	}
}

func TestExportHelmChartRejectsUnsafeArchive(t *testing.T) {
	for _, tc := range []struct {
		name, root, member string
		link, crc          bool
	}{
		{"checksum", "demo", "demo/templates/deployment.yaml", false, true},
		{"root mismatch", "other", "other/templates/deployment.yaml", false, false},
		{"traversal", "demo", "demo/../escape", false, false},
		{"link", "demo", "demo/linked", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buffer bytes.Buffer
			gz := gzip.NewWriter(&buffer)
			tw := tar.NewWriter(gz)
			metadata := "apiVersion: v2\nname: demo\nversion: 1.2.3\n"
			if err := tw.WriteHeader(&tar.Header{Name: tc.root + "/Chart.yaml", Typeflag: tar.TypeReg, Size: int64(len(metadata)), Mode: 0o644}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write([]byte(metadata)); err != nil {
				t.Fatal(err)
			}
			header := &tar.Header{Name: tc.member, Typeflag: tar.TypeReg, Mode: 0o644}
			if tc.link {
				header.Typeflag, header.Linkname = tar.TypeSymlink, "/outside"
			}
			if err := tw.WriteHeader(header); err != nil {
				t.Fatal(err)
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			data := buffer.Bytes()
			if tc.crc {
				data[len(data)-8] ^= 1
			}
			root := t.TempDir()
			layout := filepath.Join(root, "layout")
			// The legacy producer accepts this framing. Export must apply the
			// stricter existing archive owner before materializing any bytes.
			if _, err := BuildHelmChartArtifact(t.Context(), layout, "application/vnd.nvkr.helm-chart.v1", data); err != nil {
				t.Fatal(err)
			}
			descriptor, err := PayloadDescriptor(t.Context(), layout)
			if err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(root, "out", "chart.tgz")
			_, err = ExportHelmChart(t.Context(), layout, descriptor.Digest, "demo", "1.2.3", out)
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			if _, err := os.Stat(filepath.Dir(out)); !os.IsNotExist(err) {
				t.Fatalf("unsafe archive created output: %v", err)
			}
		})
	}
}
