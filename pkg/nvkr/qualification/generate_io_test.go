// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"bytes"
	"context"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

func TestQualificationGeneratedOwnerAndRuntime(t *testing.T) {
	bundle, policy := generationBundle(t)
	root := t.TempDir()
	requireCode(t, CheckWorkflows(t.Context(), root, bundle), e.CodePipelineDrift)
	requireCode(t, CheckGeneratedPolicy(t.Context(), root, policy), e.CodePipelineDrift)
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("check mutated empty root")
	}
	for range 2 {
		if err := WriteWorkflows(t.Context(), root, bundle); err != nil {
			t.Fatal(err)
		}
		if err := CheckWorkflows(t.Context(), root, bundle); err != nil {
			t.Fatal(err)
		}
		if err := CheckGeneratedPolicy(t.Context(), root, policy); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := qualificationTreeSnapshot(t, root)
	if err := CheckGeneratedPolicy(t.Context(), root, policy); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snapshot, qualificationTreeSnapshot(t, root)) {
		t.Fatal("runtime admission modified files")
	}
	// A developer changing independently authored policy must regenerate the pair.
	policy.Cosign.PlatformDigests["linux/amd64"] = "sha256:" + strings.Repeat("f", 64)
	requireCode(t, CheckGeneratedPolicy(t.Context(), root, policy), e.CodePipelineDrift)
}
func TestQualificationGeneratedOwnerRejectsConflicts(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(*testing.T, string, *GeneratedWorkflows)
		code    e.Code
	}{
		{"unowned workflow", func(t *testing.T, root string, b *GeneratedWorkflows) {
			writeQualificationTestFile(t, root, b.manifest.QualificationWorkflow, []byte("name: handwritten\n"))
		}, e.CodeWriteConflict},
		{"modified owned workflow", func(t *testing.T, root string, b *GeneratedWorkflows) {
			mustWriteQualification(t, root, b)
			writeQualificationTestFile(t, root, b.manifest.SmokeWorkflow, []byte("name: changed\n"))
		}, e.CodeWriteConflict},
		{"unknown owner file", func(t *testing.T, root string, b *GeneratedWorkflows) {
			writeQualificationTestFile(t, root, qualificationOwnerDirectory+"/notes", []byte("keep me"))
		}, e.CodePipelineDrift},
		{"workflow symlink", func(t *testing.T, root string, b *GeneratedWorkflows) {
			writeQualificationTestFile(t, root, "operator.yml", []byte("keep me"))
			if err := os.MkdirAll(filepath.Join(root, ".github/workflows"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(root, "operator.yml"), filepath.Join(root, b.manifest.SmokeWorkflow)); err != nil {
				t.Fatal(err)
			}
		}, e.CodeWriteConflict},
		{"directory symlink", func(t *testing.T, root string, _ *GeneratedWorkflows) {
			if err := os.Symlink(t.TempDir(), filepath.Join(root, ".github")); err != nil {
				t.Fatal(err)
			}
		}, e.CodeWriteConflict},
		{"manifest symlink", func(t *testing.T, root string, b *GeneratedWorkflows) {
			mustWriteQualification(t, root, b)
			if err := os.Remove(filepath.Join(root, QualificationManifestPath)); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(root, b.manifest.SmokeWorkflow), filepath.Join(root, QualificationManifestPath)); err != nil {
				t.Fatal(err)
			}
		}, e.CodeWriteConflict},
		{"lock symlink", func(t *testing.T, root string, _ *GeneratedWorkflows) {
			writeQualificationTestFile(t, root, "operator", nil)
			if err := os.MkdirAll(filepath.Join(root, qualificationOwnerDirectory), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(root, "operator"), filepath.Join(root, QualificationManifestPath+".lease")); err != nil {
				t.Fatal(err)
			}
		}, e.CodeWriteConflict},
		{"workflow executable", func(t *testing.T, root string, b *GeneratedWorkflows) {
			mustWriteQualification(t, root, b)
			if err := os.Chmod(filepath.Join(root, b.manifest.SmokeWorkflow), 0o755); err != nil {
				t.Fatal(err)
			}
		}, e.CodeWriteConflict},
		{"arbitrary stage", func(t *testing.T, root string, _ *GeneratedWorkflows) {
			writeQualificationTestFile(t, root, qualificationOwnerDirectory+"/qualifier.stage", []byte("keep operator file"))
		}, e.CodeWriteConflict},
		{"corrupt manifest", func(t *testing.T, root string, b *GeneratedWorkflows) {
			mustWriteQualification(t, root, b)
			writeQualificationTestFile(t, root, QualificationManifestPath, []byte("{}\n"))
		}, e.CodePipelineDrift},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bundle, _ := generationBundle(t)
			root := t.TempDir()
			tc.prepare(t, root, bundle)
			before := qualificationTreeSnapshot(t, root)
			requireCode(t, WriteWorkflows(t.Context(), root, bundle), tc.code)
			after := qualificationTreeSnapshot(t, root)
			delete(after, QualificationManifestPath+".lease")
			delete(before, QualificationManifestPath+".lease")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("failed write overwrote user bytes")
			}
		})
	}
}
func TestQualificationGeneratedRuntimeRejectsDrift(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*testing.T, string, *GeneratedWorkflows, *lifecycle.QualificationPolicy)
		code e.Code
	}{
		{"policy change", func(_ *testing.T, _ string, _ *GeneratedWorkflows, p *lifecycle.QualificationPolicy) {
			p.OutsideMVPSuites = []string{"integration"}
		}, e.CodePipelineDrift},
		{"workflow hash", func(t *testing.T, r string, b *GeneratedWorkflows, _ *lifecycle.QualificationPolicy) {
			writeQualificationTestFile(t, r, b.manifest.SmokeWorkflow, append(b.files[b.manifest.SmokeWorkflow], []byte("# changed\n")...))
		}, e.CodePipelineDrift},
		{"mode", func(t *testing.T, r string, b *GeneratedWorkflows, _ *lifecycle.QualificationPolicy) {
			if err := os.Chmod(filepath.Join(r, b.manifest.SmokeWorkflow), 0o600); err != nil {
				t.Fatal(err)
			}
		}, e.CodeWriteConflict},
		{"symlink", func(t *testing.T, r string, b *GeneratedWorkflows, _ *lifecycle.QualificationPolicy) {
			if err := os.Remove(filepath.Join(r, b.manifest.SmokeWorkflow)); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(r, b.manifest.QualificationWorkflow), filepath.Join(r, b.manifest.SmokeWorkflow)); err != nil {
				t.Fatal(err)
			}
		}, e.CodeWriteConflict},
		{"extra owner file", func(t *testing.T, r string, _ *GeneratedWorkflows, _ *lifecycle.QualificationPolicy) {
			writeQualificationTestFile(t, r, qualificationOwnerDirectory+"/unowned", nil)
		}, e.CodePipelineDrift},
		{"missing manifest", func(t *testing.T, r string, _ *GeneratedWorkflows, _ *lifecycle.QualificationPolicy) {
			if err := os.Remove(filepath.Join(r, QualificationManifestPath)); err != nil {
				t.Fatal(err)
			}
		}, e.CodePipelineDrift},
		{"missing worker", func(t *testing.T, r string, b *GeneratedWorkflows, _ *lifecycle.QualificationPolicy) {
			if err := os.Remove(filepath.Join(r, b.manifest.SmokeWorkflow)); err != nil {
				t.Fatal(err)
			}
		}, e.CodePipelineDrift},
		{"incomplete generation", func(t *testing.T, r string, b *GeneratedWorkflows, _ *lifecycle.QualificationPolicy) {
			writeQualificationTestFile(t, r, qualificationOwnerDirectory+"/smoke.stage", b.files[b.manifest.SmokeWorkflow][:12])
		}, e.CodePipelineDrift},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bundle, policy := generationBundle(t)
			root := t.TempDir()
			mustWriteQualification(t, root, bundle)
			tc.edit(t, root, bundle, &policy)
			before := qualificationTreeSnapshot(t, root)
			requireCode(t, CheckGeneratedPolicy(t.Context(), root, policy), tc.code)
			if !reflect.DeepEqual(before, qualificationTreeSnapshot(t, root)) {
				t.Fatal("runtime refusal mutated checkout")
			}
		})
	}
}
func TestQualificationGeneratedManifestRejectsAmbiguity(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*qualificationManifest)
	}{
		{"unowned path", func(m *qualificationManifest) { m.Members[0].Path = ".github/workflows/operator.yml" }},
		{"traversal", func(m *qualificationManifest) { m.Members[0].Path = "../operator" }},
		{"duplicate member", func(m *qualificationManifest) { m.Members[1] = m.Members[0] }},
		{"producer ownership", func(m *qualificationManifest) { m.SmokeWorkflow = m.ProducerWorkflow }},
		{"extra member", func(m *qualificationManifest) { m.Members = append(m.Members, m.Members[0]) }},
		{"unknown schema", func(m *qualificationManifest) { m.SchemaVersion = "future" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, p := generationBundle(t)
			root := t.TempDir()
			mustWriteQualification(t, root, b)
			m := b.manifest
			m.Members = append(m.Members[:0:0], m.Members...)
			tc.edit(&m)
			raw, err := canonical.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			writeQualificationTestFile(t, root, QualificationManifestPath, append(raw, '\n'))
			requireCode(t, CheckGeneratedPolicy(t.Context(), root, p), e.CodePipelineDrift)
			requireCode(t, WriteWorkflows(t.Context(), root, b), e.CodePipelineDrift)
		})
	}
}

func TestQualificationGeneratedRecovery(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(*testing.T, string, *GeneratedWorkflows)
	}{
		{"partial stage", func(t *testing.T, r string, b *GeneratedWorkflows) {
			writeQualificationTestFile(t, r, qualificationOwnerDirectory+"/qualifier.stage", b.files[b.manifest.QualificationWorkflow][:19])
		}},
		{"first workflow before initial manifest", func(t *testing.T, r string, b *GeneratedWorkflows) {
			writeQualificationTestFile(t, r, b.manifest.QualificationWorkflow, b.files[b.manifest.QualificationWorkflow])
		}},
		{"both workflows before initial manifest", func(t *testing.T, r string, b *GeneratedWorkflows) {
			for name, raw := range b.files {
				writeQualificationTestFile(t, r, name, raw)
			}
		}},
		{"stale prior manifest", func(t *testing.T, r string, b *GeneratedWorkflows) {
			mustWriteQualification(t, r, b)
			var manifest qualificationManifest
			raw, err := b.manifestBytes()
			if err != nil {
				t.Fatal(err)
			}
			if err := canonical.UnmarshalExact(bytes.TrimSuffix(raw, []byte{'\n'}), &manifest); err != nil {
				t.Fatal(err)
			}
			manifest.PolicyDigest = "sha256:" + strings.Repeat("e", 64)
			raw, err = canonical.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			writeQualificationTestFile(t, r, QualificationManifestPath, append(raw, '\n'))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, p := generationBundle(t)
			root := t.TempDir()
			tc.prepare(t, root, b)
			requireCode(t, CheckWorkflows(t.Context(), root, b), e.CodePipelineDrift)
			mustWriteQualification(t, root, b)
			if err := CheckGeneratedPolicy(t.Context(), root, p); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestQualificationGeneratedRecoveryRequiresOriginalPolicy(t *testing.T) {
	plan, policy := generationFixture(t)
	original, err := RenderWorkflows(t.Context(), plan, policy, "qualification.yaml")
	if err != nil {
		t.Fatal(err)
	}
	nextPolicy := policy
	nextPolicy.Cosign.PlatformDigests = maps.Clone(policy.Cosign.PlatformDigests)
	nextPolicy.Cosign.PlatformDigests["linux/amd64"] = "sha256:" + strings.Repeat("f", 64)
	next, err := RenderWorkflows(t.Context(), plan, nextPolicy, "qualification.yaml")
	if err != nil {
		t.Fatal(err)
	}
	originalManifest, err := original.manifestBytes()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name             string
		manifestComplete bool
	}{
		{name: "partial manifest before payload publication"},
		{name: "complete manifest after payload publication", manifestComplete: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			mustWriteQualification(t, root, original)
			stagedManifest := originalManifest
			if !tc.manifestComplete {
				// The writer stages both workflows before staging the manifest.
				for name, raw := range original.files {
					writeQualificationTestFile(t, root, qualificationStage(name, original), raw)
				}
				stagedManifest = stagedManifest[:len(stagedManifest)-1]
			}
			writeQualificationTestFile(t, root, qualificationStage(QualificationManifestPath, original), stagedManifest)
			before := qualificationTreeSnapshot(t, root)
			requireCode(t, CheckWorkflows(t.Context(), root, original), e.CodePipelineDrift)
			requireCode(t, WriteWorkflows(t.Context(), root, next), e.CodeWriteConflict)
			if !reflect.DeepEqual(before, qualificationTreeSnapshot(t, root)) {
				t.Fatal("cross-generation refusal changed staged or published bytes or modes")
			}

			// Restore the interrupted generation first; never discard its stages.
			mustWriteQualification(t, root, original)
			if err := CheckWorkflows(t.Context(), root, original); err != nil {
				t.Fatal(err)
			}
			if err := CheckGeneratedPolicy(t.Context(), root, policy); err != nil {
				t.Fatal(err)
			}

			// Once recovery completes, the changed policy is an ordinary update.
			mustWriteQualification(t, root, next)
			if err := CheckWorkflows(t.Context(), root, next); err != nil {
				t.Fatal(err)
			}
			if err := CheckGeneratedPolicy(t.Context(), root, nextPolicy); err != nil {
				t.Fatal(err)
			}
			requireCode(t, CheckGeneratedPolicy(t.Context(), root, policy), e.CodePipelineDrift)
		})
	}
}

func TestQualificationGeneratedSafeRetirement(t *testing.T) {
	plan, policy := generationFixture(t)
	prior, err := RenderWorkflows(t.Context(), plan, policy, "qualification.yaml")
	if err != nil {
		t.Fatal(err)
	}
	policy.Smoke.Workflow = ".github/workflows/new-smoke.yml"
	next, err := RenderWorkflows(t.Context(), plan, policy, "qualification.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, modified := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged", true: "modified"}[modified], func(t *testing.T) {
			root := t.TempDir()
			mustWriteQualification(t, root, prior)
			if modified {
				writeQualificationTestFile(t, root, prior.manifest.SmokeWorkflow, []byte("keep changed file"))
				requireCode(t, WriteWorkflows(t.Context(), root, next), e.CodeWriteConflict)
				return
			}
			mustWriteQualification(t, root, next)
			if _, err := os.Lstat(filepath.Join(root, prior.manifest.SmokeWorkflow)); !os.IsNotExist(err) {
				t.Fatal("retired owned file remains")
			}
			if err := CheckGeneratedPolicy(t.Context(), root, policy); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestQualificationGeneratedConcurrentOwnerAndCancellation(t *testing.T) {
	b, _ := generationBundle(t)
	root := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	requireCode(t, WriteWorkflows(ctx, root, b), e.CodeTimeout)
	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Fatal("canceled write created files")
	}
	var wg sync.WaitGroup
	failures := make(chan error, 4)
	for range 4 {
		wg.Go(func() { failures <- WriteWorkflows(t.Context(), root, b) })
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	fs, err := openQualificationRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	release, err := acquireQualificationLock(t.Context(), fs, QualificationManifestPath+".lease")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel = context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	requireCode(t, WriteWorkflows(ctx, root, b), e.CodeTimeout)
}
func mustWriteQualification(t *testing.T, root string, bundle *GeneratedWorkflows) {
	t.Helper()
	if err := WriteWorkflows(t.Context(), root, bundle); err != nil {
		t.Fatal(err)
	}
}
func qualificationTreeSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(name)
			if err != nil {
				return err
			}
			result[filepath.ToSlash(relative)] = "symlink:" + target
			return nil
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		result[filepath.ToSlash(relative)] = info.Mode().String() + ":" + string(raw)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
