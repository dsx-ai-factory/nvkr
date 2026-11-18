// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// This opt-in diagnostic copies descriptions after native execution. It cannot
// restore the completed runtime-use receipt or grant publication authority.
func TestNativeRuntimeExport(t *testing.T) {
	if os.Getenv("NVKR_TEST_RUNTIME_PHASE") != "export" {
		t.Skip("explicit native runtime evidence export only")
	}
	workspace, destination := os.Getenv("NVKR_TEST_RUNTIME_WORKSPACE"), os.Getenv("NVKR_TEST_RUNTIME_EVIDENCE")
	if !filepath.IsAbs(workspace) || !filepath.IsAbs(destination) {
		t.Fatal("export requires absolute workspace and evidence directories")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	plan, err := LoadPlanContext(ctx, filepath.Join(workspace, "plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := readProducerResult(ctx, filepath.Join(destination, "disk-result.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.InternalTools) != 1 || result.RuntimeUse == nil {
		t.Fatal("proof export requires its single runtime and completed use description")
	}
	_, artifacts := planIndex(plan)
	tool := plan.InternalTools[0]
	for _, item := range []struct{ id, name, digest string }{
		{tool.SBOMArtifactID, "runtime-sbom", result.RuntimeUse.SBOM},
		{tool.ObservationArtifactID, "runtime-observation.json", result.RuntimeUse.Observation},
	} {
		path := artifacts[item.id].Path
		if !filepath.IsLocal(path) {
			t.Fatalf("evidence %s has no workspace-relative path", item.id)
		}
		if err := copyRuntimeProofEvidence(ctx, filepath.Join(workspace, path), filepath.Join(destination, item.name), item.digest); err != nil {
			t.Fatal(err)
		}
		t.Logf("exported complete %s: %s", item.name, item.digest)
	}
}

// Use the factory's existing file/tree digest, including relative paths and
// modes. Hashing only an SBOM index would omit the per-artifact documents.
func copyRuntimeProofEvidence(ctx context.Context, source, destination, expected string) error {
	check := func(path string) error {
		digest, err := digestFactoryPath(ctx, path)
		if err != nil {
			return err
		}
		if digest != expected {
			return fmt.Errorf("proof evidence %s differs from completed runtime use", path)
		}
		return nil
	}
	if err := check(source); err != nil {
		return err
	}
	var directories []struct {
		path string
		mode fs.FileMode
	}
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if info.IsDir() {
			if err := os.Mkdir(target, 0o700); err != nil {
				return err
			}
			directories = append(directories, struct {
				path string
				mode fs.FileMode
			}{target, info.Mode().Perm()})
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("proof evidence contains non-regular file %s", path)
		}
		raw, err := readFactorySBOMFile(ctx, path, "runtime proof evidence")
		if err != nil {
			return err
		}
		file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, writeErr := file.Write(raw)
		modeErr := file.Chmod(info.Mode().Perm())
		closeErr := file.Close()
		for _, err := range []error{writeErr, modeErr, closeErr} {
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Apply restrictive directory modes only after their children are copied.
	for i := len(directories) - 1; i >= 0; i-- {
		if err := os.Chmod(directories[i].path, directories[i].mode); err != nil {
			return err
		}
	}
	if err := check(source); err != nil {
		return err
	}
	return check(destination)
}

func TestRuntimeProofEvidenceCopy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tree   bool
		change string
		large  bool
	}{
		{name: "observation file"},
		{name: "complete nested SBOM set", tree: true},
		{name: "inventory above small-record limit", tree: true, large: true},
		{name: "changed document", tree: true, change: "content"},
		{name: "changed mode", tree: true, change: "mode"},
		{name: "symlink", tree: true, change: "symlink"},
		{name: "canceled", tree: true, change: "cancel"},
		{name: "existing destination", tree: true, change: "destination"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			source, destination := filepath.Join(root, "source"), filepath.Join(root, "export")
			file := source
			if tc.tree {
				file = filepath.Join(source, "nested", "runtime.cdx.json")
				if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(source, "index.json"), []byte("index"), 0o640); err != nil {
					t.Fatal(err)
				}
			}
			content := []byte("document")
			if tc.large {
				content = bytes.Repeat([]byte("x"), int(maxSigningRecordBytes)+1)
			}
			if err := os.WriteFile(file, content, 0o640); err != nil {
				t.Fatal(err)
			}
			want, err := digestFactoryPath(t.Context(), source)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch tc.change {
			case "content":
				err = os.WriteFile(file, []byte("changed"), 0o640)
			case "mode":
				err = os.Chmod(file, 0o600)
			case "symlink":
				err = os.Symlink(file, filepath.Join(source, "alias"))
			case "cancel":
				cancel()
			case "destination":
				err = os.Mkdir(destination, 0o700)
			}
			if err != nil {
				t.Fatal(err)
			}
			err = copyRuntimeProofEvidence(ctx, source, destination, want)
			if tc.change != "" {
				if err == nil {
					t.Fatal("export accepted changed evidence or destination")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := digestFactoryPath(t.Context(), destination)
			if err != nil || got != want {
				t.Fatalf("complete exported evidence = %s, want %s: %v", got, want, err)
			}
		})
	}
}
