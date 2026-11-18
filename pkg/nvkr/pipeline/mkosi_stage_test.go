// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/material"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/mkosi"
)

func mkosiStageFixture(t *testing.T) (ir.Node, string, string, string) {
	t.Helper()
	spec, contents := mkosiAcquisitionFixture()
	source, inputs, work := t.TempDir(), t.TempDir(), t.TempDir()
	item := spec.Roots[0].Inputs.Packages[0]
	archive := filepath.Join(source, mkosiInputDirectory, "runtime/packages", strings.TrimPrefix(item.Digest, "sha256:")+".deb")
	if err := os.MkdirAll(filepath.Dir(archive), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archive, []byte(contents["/archive"]), 0o444); err != nil {
		t.Fatal(err)
	}
	return ir.Node{Mkosi: spec, Platform: "linux/amd64", BuilderType: ir.BuilderMkosi}, source, inputs, work
}

func TestMkosiStagingUsesExactPrivateInputs(t *testing.T) {
	node, source, inputs, work := mkosiStageFixture(t)
	operations, digest, err := prepareMkosiWork(t.Context(), node, source, inputs, work, nil)
	if err != nil {
		t.Fatal(err)
	}
	if digest != "" || len(operations) != 2 {
		t.Fatalf("unexpected material binding or operations: %q, %d", digest, len(operations))
	}
	for relative, want := range map[string]string{
		"roots/runtime/packages/base-files_1.0_amd64.deb":     "pinned archive bytes",
		"roots/runtime/project/mkosi.conf":                    node.Mkosi.Configuration,
		"roots/runtime/project/mkosi.conf.d/10-packages.conf": "[Content]\nPackages=\n    base-files=1.0\n",
	} {
		raw, err := os.ReadFile(filepath.Join(inputs, relative))
		if err != nil || string(raw) != want {
			t.Fatalf("staged %s: %q, %v", relative, raw, err)
		}
		info, err := os.Stat(filepath.Join(inputs, relative))
		if err != nil || info.Mode().Perm() != 0o444 {
			t.Fatalf("staged %s is not read-only: %v", relative, err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(inputs, "evidence/nvkr/mkosi/recipe.json"))
	if err != nil || strings.Contains(string(raw), "https:") {
		t.Fatalf("runtime recipe contains transport locations: %s, %v", raw, err)
	}
	if _, _, err := prepareMkosiWork(t.Context(), node, source, inputs, work, nil); err == nil {
		t.Fatal("accepted previously occupied workspace")
	}
}

func TestMkosiStagingRejectsPackageSubstitution(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(map[bool]string{false: "bytes", true: "symlink"}[symlink], func(t *testing.T) {
			node, source, inputs, work := mkosiStageFixture(t)
			item := node.Mkosi.Roots[0].Inputs.Packages[0]
			archive := filepath.Join(source, mkosiInputDirectory, "runtime/packages", strings.TrimPrefix(item.Digest, "sha256:")+".deb")
			if symlink {
				if err := os.Rename(archive, archive+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(archive+".original", archive); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Chmod(archive, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(archive, []byte(strings.Repeat("x", int(item.Size))), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			_, _, err := prepareMkosiWork(t.Context(), node, source, inputs, work, nil)
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed {
				t.Fatalf("got %v; want exact-input rejection", err)
			}
			if _, err := os.Stat(filepath.Join(inputs, "evidence/nvkr/mkosi/recipe.json")); !os.IsNotExist(err) {
				t.Fatal("failed staging retained a completed recipe")
			}
		})
	}
}

func TestMkosiStagingCancellationBeforeWrites(t *testing.T) {
	node, source, inputs, work := mkosiStageFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err := prepareMkosiWork(ctx, node, source, inputs, work, nil)
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != e.CodeTimeout {
		t.Fatalf("got %v; want canceled admission", err)
	}
	entries, err := os.ReadDir(work)
	if err != nil || len(entries) != 0 {
		t.Fatalf("canceled staging wrote workspace: %v", err)
	}
}

func TestMkosiOriginalReportRetention(t *testing.T) {
	for _, scenario := range []string{"exact", "different release", "evidence symlink"} {
		t.Run(scenario, func(t *testing.T) {
			node, source, inputs, work := mkosiStageFixture(t)
			operations, _, err := prepareMkosiWork(t.Context(), node, source, inputs, work, nil)
			if err != nil {
				t.Fatal(err)
			}
			report := material.MkosiManifest{ManifestVersion: 1,
				Config:   material.MkosiConfig{Name: "runtime", Distribution: "ubuntu", Release: "noble", Architecture: "x86-64", OutputFormat: "directory"},
				Packages: []material.InstalledPackage{{Type: "deb", Name: "base-files", Version: "1.0", Architecture: "amd64"}}}
			if scenario == "different release" {
				report.Config.Release = "resolute"
			}
			raw, err := json.MarshalIndent(report, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(mkosiWorkPath(work, operations[0].Manifest), raw, 0o644); err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			if scenario == "evidence symlink" {
				directory := filepath.Join(inputs, "evidence"+mkosi.EvidenceDirectory)
				if err := os.Rename(directory, directory+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, directory); err != nil {
					t.Fatal(err)
				}
			}
			err = retainMkosiReport(t.Context(), work, inputs, operations[0], node.Mkosi.Roots[0])
			if scenario != "exact" {
				var coded *e.Error
				if !errors.As(err, &coded) || (coded.Code != e.CodeBuildFailed && coded.Code != e.CodeVerifyFailed) {
					t.Fatalf("unsafe report retention: %v", err)
				}
				entries, err := os.ReadDir(outside)
				if err != nil || len(entries) != 0 {
					t.Fatalf("report escaped owned evidence tree: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(inputs, "evidence/nvkr/mkosi/runtime.manifest.json"))
			if err != nil || string(got) != string(raw) {
				t.Fatalf("original report bytes changed: %v", err)
			}
		})
	}
}

func TestMkosiPackageCopiesPreserveMeasuredBytes(t *testing.T) {
	for _, scenario := range []string{"exact", "substituted", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			root, inputs := t.TempDir(), t.TempDir()
			relative := "opt/inspection/boot/kernel"
			name := filepath.Join(root, relative)
			if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(name, []byte("pinned kernel"), 0o600); err != nil {
				t.Fatal(err)
			}
			spec := &mkosi.Spec{Copies: []mkosi.PackageFile{{Source: "/" + relative, Path: "/nvkr/inspection/kernel", Mode: 0o444, Digest: FileDigest([]byte("pinned kernel"))}}}
			switch scenario {
			case "substituted":
				spec.Copies[0].Digest = FileDigest([]byte("other kernel"))
			case "symlink":
				if err := os.Rename(name, name+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(name+".original", name); err != nil {
					t.Fatal(err)
				}
			}
			err := stageMkosiPackageCopies(t.Context(), spec, root, inputs)
			if scenario != "exact" {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed {
					t.Fatalf("unmeasured kernel copy accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			digest, _, info, err := hashRegularBeneathContext(t.Context(), inputs, "file-materials/nvkr/inspection/kernel")
			if err != nil || digest != spec.Copies[0].Digest || info.Mode().Perm() != 0o444 {
				t.Fatalf("kernel copy changed its bytes or mode: %s %v", digest, err)
			}
			original, err := os.Stat(name)
			if err != nil || original.Mode().Perm() != 0o600 {
				t.Fatalf("original package mode changed: %v", err)
			}
		})
	}
}
