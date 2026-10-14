// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestDebianContentAdmissionBeforePathIO(t *testing.T) {
	validDigest := "sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name   string
		digest string
		size   int64
		ctx    context.Context
		want   e.Code
	}{
		{"nil context", validDigest, 1, nil, e.CodeUsage},
		{"invalid digest", "tag", 1, t.Context(), e.CodeUnverifiedRef},
		{"zero length", validDigest, 0, t.Context(), e.CodeEngineCapability},
		{"negative length", validDigest, -1, t.Context(), e.CodeEngineCapability},
		{"oversized", validDigest, MaxDebianPackageBytes + 1, t.Context(), e.CodeEngineCapability},
		{"no runtime", validDigest, 1, t.Context(), e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var container *DiskInspectionContainer
			result, err := container.InspectDebianPackageContent(tc.ctx, "absent-package", tc.digest, tc.size, "linux/amd64")
			if code(err) != tc.want || result != nil {
				t.Fatalf("admission = %v, %v; want %s and no observation", result, err, tc.want)
			}
		})
	}
	container := &DiskInspectionContainer{selection: testDiskInspectionSelection(t)}
	result, err := container.InspectDebianPackageContent(t.Context(), "absent-package", validDigest, 1, "linux/amd64")
	if code(err) != e.CodeEngineCapability || result != nil || !strings.Contains(err.Error(), "complete inspection profile") {
		t.Fatalf("incomplete profile reached package path: %v, %v", result, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err = container.InspectDebianPackageContent(ctx, "absent-package", validDigest, 1, "linux/amd64")
	if code(err) != e.CodeTimeout || result != nil {
		t.Fatalf("canceled request reached input work: %v, %v", result, err)
	}
}

// Capture tests byte identity and lifetime, not Debian format or scanner coverage.
func TestDebianPackageCaptureOwnsExactBytes(t *testing.T) {
	raw := []byte("controlled capture bytes")
	digest := FileDigest(raw)
	for _, filename := range []string{"package.deb", "package-without-extension"} {
		t.Run(filename, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), filename)
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			held, err := captureDebianPackage(t.Context(), path, digest, int64(len(raw)))
			if err != nil {
				t.Fatal(err)
			}
			defer held.Close()
			if err := os.WriteFile(path, []byte("replacement bytes"), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := held.ReadAllContext(t.Context(), int64(len(raw)))
			if err != nil || string(got) != string(raw) || held.Digest() != digest || held.Size() != int64(len(raw)) {
				t.Fatalf("caller mutation replaced captured bytes: %q (%v)", got, err)
			}
			if err := held.AssertUnchangedContext(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDebianPackageCaptureRejectsSubstitution(t *testing.T) {
	raw := []byte("controlled capture bytes")
	for _, tc := range []struct {
		name string
		edit func(string) (string, string, int64)
	}{
		{"digest", func(p string) (string, string, int64) { return p, "sha256:" + strings.Repeat("b", 64), int64(len(raw)) }},
		{"short size", func(p string) (string, string, int64) { return p, FileDigest(raw), int64(len(raw) - 1) }},
		{"long size", func(p string) (string, string, int64) { return p, FileDigest(raw), int64(len(raw) + 1) }},
		{"symlink", func(p string) (string, string, int64) {
			link := p + "-link"
			if err := os.Symlink(p, link); err != nil {
				t.Fatal(err)
			}
			return link, FileDigest(raw), int64(len(raw))
		}},
		{"directory", func(p string) (string, string, int64) { return filepath.Dir(p), FileDigest(raw), int64(len(raw)) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "package")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			path, digest, size := tc.edit(path)
			result, err := captureDebianPackage(t.Context(), path, digest, size)
			if code(err) != e.CodeVerifyFailed || result != nil {
				t.Fatalf("substitution = %v, %v; want verify-failed and no snapshot", result, err)
			}
		})
	}
}
