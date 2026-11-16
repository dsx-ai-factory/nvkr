// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package diskinspect

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestDiskClassesAreOwnerIssuedNotScannerProperties(t *testing.T) {
	raw := []byte(`{"schema":{"version":"16.1.10"},"artifacts":[{"name":"sample","version":"1","type":"deb","metadata":{"architecture":"arm64"},"properties":[{"name":"nvkr:diskContentClass","value":"debian-archive"}]}]}`)
	inventory, err := filesystemDependenciesFromJSON(raw, "sha256:"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := diskComponentClass(inventory.Components[0]); sbomCode(err) != e.CodeProvenanceMissing {
		t.Fatalf("scanner authored the owner's class: %v", err)
	}
	for _, class := range []string{ClassFilesystem, ClassContainer} {
		components := withDiskContentClass(inventory.Components, class)
		if observed, err := diskComponentClass(components[0]); err != nil || observed != class {
			t.Fatalf("scanner namespace replaced owner %s with %s: %v", class, observed, err)
		}
	}
}

func TestFilesystemNativeMetadataAndFileProjection(t *testing.T) {
	pin := "sha256:" + strings.Repeat("a", 64)
	for _, schema := range []string{"16.1.10", "16.1.13", "16.1.9", "16.1.11", "16.1.12", "16.1.14", "unknown"} {
		t.Run(schema, func(t *testing.T) {
			raw := []byte(`{"schema":{"version":"` + schema + `"},"artifacts":[{"name":"sample","version":"2:1.0-1ubuntu4","type":"deb","purl":"pkg:deb/ubuntu/sample@1?arch=arm64","metadata":{"architecture":"arm64"}}]}`)
			inventory, err := filesystemDependenciesFromJSON(raw, pin)
			if schema != "16.1.10" && schema != "16.1.13" {
				assertDependencyCode(t, err, e.CodeProvenanceMissing)
				return
			}
			if err != nil || inventory.Components[0].Version != "2:1.0-1ubuntu4" || inventory.Components[0].Properties[0].Value != "arm64" {
				t.Fatalf("native metadata changed: %+v %v", inventory, err)
			}
		})
	}
	for _, name := range []string{"valid", "traversal", "duplicate", "fifo"} {
		t.Run(name, func(t *testing.T) {
			var archive bytes.Buffer
			writer := tar.NewWriter(&archive)
			filename := "usr/bin/loose"
			if name == "traversal" {
				filename = "../loose"
			}
			header := &tar.Header{Name: filename, Typeflag: tar.TypeReg, Mode: 0o644, Size: 3}
			if name == "fifo" {
				header.Typeflag = tar.TypeFifo
				header.Size = 0
			}
			if err := writer.WriteHeader(header); err != nil {
				t.Fatal(err)
			}
			if header.Size != 0 {
				_, _ = writer.Write([]byte("bin"))
			}
			if name == "duplicate" {
				_ = writer.WriteHeader(header)
				_, _ = writer.Write([]byte("bin"))
			}
			_ = writer.Close()
			file, err := os.CreateTemp(t.TempDir(), "export")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			_, _ = io.Copy(file, &archive)
			files, err := filesystemFiles(t.Context(), file, 8192)
			if name == "traversal" || name == "duplicate" {
				assertDependencyCode(t, err, e.CodeVerifyFailed)
				return
			}
			if err != nil || len(files) != 1 {
				t.Fatalf("file observation failed: %v", err)
			}
			if name == "valid" && !canonical.IsDigest(files[0].Digest) {
				t.Fatal("loose file has no exact content hash")
			}
		})
	}
}

func TestBoundedScannerTransportStopsOverflowAndCancellation(t *testing.T) {
	for _, name := range []string{"within", "overflow", "canceled"} {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			if name == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			var output bytes.Buffer
			size := 8
			if name == "overflow" {
				size = 64
			}
			err := writeBoundedArchive(ctx, &output, 16, func(destination io.Writer) error {
				_, err := io.Copy(destination, strings.NewReader(strings.Repeat("x", size)))
				return err
			})
			if name == "within" {
				if err != nil || output.Len() != 8 {
					t.Fatalf("valid stream failed: %v", err)
				}
				return
			}
			want := e.CodeEngineCapability
			if name == "canceled" {
				want = e.CodeTimeout
			}
			if sbomCode(err) != want {
				t.Fatalf("wrong bounded stream error: %v", err)
			}
			if output.Len() > 17 {
				t.Fatal("writer exceeded active-stream bound")
			}
		})
	}
}
