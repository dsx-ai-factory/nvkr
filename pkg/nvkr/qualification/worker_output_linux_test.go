// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package qualification

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

func workerFilesFixture() smokeWorkerFiles {
	return smokeWorkerFiles{document: []byte("document"), signature: []byte("signature"), certificate: []byte("certificate"), bundle: []byte("bundle")}
}

func TestSmokeWorkerOutputCommitsCompletePrivateImmutableSet(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "worker")
	files := workerFilesFixture()
	calls := 0
	if err := commitSmokeWorkerFiles(t.Context(), workspace, files, func() error { calls++; return nil }); err != nil {
		t.Fatal(err)
	}
	if calls < 3 {
		t.Fatal("missing live receipt checks before/after commit")
	}
	directory := filepath.Join(workspace, "evidence")
	t.Cleanup(func() { _ = os.Chmod(directory, 0o700) })
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatal("partial or extra committed evidence")
	}
	for _, member := range files.members() {
		path := filepath.Join(directory, member.name)
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o400 {
			t.Fatalf("unprotected member: %v", info.Mode())
		}
		raw, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(raw, member.raw) {
			t.Fatalf("wrong bytes: %v", err)
		}
	}
	err = commitSmokeWorkerFiles(t.Context(), workspace, files, func() error { return nil })
	requireCode(t, err, e.CodeImmutableViolation)
	if _, err := os.Stat(filepath.Join(workspace, ".worker-pending")); !os.IsNotExist(err) {
		t.Fatal("successful commit left stage")
	}
}

func TestSmokeWorkerOutputRefusesUnsafeOrInterruptedState(t *testing.T) {
	for _, kind := range []string{"shared permissions", "workspace symlink", "existing evidence symlink", "stale stage", "held lock"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			workspace := filepath.Join(root, "worker")
			if err := os.Mkdir(workspace, 0o700); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "shared permissions":
				if err := os.Chmod(workspace, 0o755); err != nil {
					t.Fatal(err)
				}
			case "workspace symlink":
				workspace = filepath.Join(root, "alias")
				if err := os.Symlink(filepath.Join(root, "worker"), workspace); err != nil {
					t.Fatal(err)
				}
			case "existing evidence symlink":
				if err := os.Symlink(root, filepath.Join(workspace, "evidence")); err != nil {
					t.Fatal(err)
				}
			case "stale stage":
				if err := os.Mkdir(filepath.Join(workspace, ".worker-pending"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "held lock":
				file, err := os.Open(workspace)
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
					t.Fatal(err)
				}
			}
			err := commitSmokeWorkerFiles(t.Context(), workspace, workerFilesFixture(), func() error { return nil })
			requireCode(t, err, e.CodeImmutableViolation)
		})
	}
}

func TestSmokeWorkerOutputFailureBeforeCommitLeavesNoEvidence(t *testing.T) {
	for _, kind := range []string{"nil authority", "revoked at entry", "revoked after stage", "cancel after stage", "workspace replacement", "missing bundle", "oversize signature"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			workspace := filepath.Join(root, "worker")
			files := workerFilesFixture()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			require := func() error {
				calls++
				if kind == "revoked at entry" || kind == "revoked after stage" && calls == 2 {
					return e.New(e.CodeGateUnmet, "revoked")
				}
				if calls == 2 && kind == "cancel after stage" {
					cancel()
				}
				if calls == 2 && kind == "workspace replacement" {
					if err := os.Rename(workspace, workspace+".original"); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(workspace, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				return nil
			}
			want := e.CodeGateUnmet
			switch kind {
			case "nil authority":
				require = nil
			case "cancel after stage":
				want = e.CodeTimeout
			case "workspace replacement":
				want = e.CodeImmutableViolation
			case "missing bundle":
				files.bundle = nil
				want = e.CodeSignatureInvalid
			case "oversize signature":
				files.signature = make([]byte, (64<<10)+1)
				want = e.CodeSignatureInvalid
			}
			err := commitSmokeWorkerFiles(ctx, workspace, files, require)
			requireCode(t, err, want)
			if _, err := os.Stat(filepath.Join(workspace, "evidence")); !os.IsNotExist(err) {
				t.Fatal("failed operation committed evidence")
			}
			if _, err := os.Stat(filepath.Join(workspace, ".worker-pending")); !os.IsNotExist(err) {
				t.Fatal("failed operation retained partial stage")
			}
		})
	}
}

func TestSmokeWorkerOutputRefusesStageSubstitutionWithoutRemovingReplacement(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "worker")
	stage := filepath.Join(workspace, ".worker-pending")
	calls := 0
	err := commitSmokeWorkerFiles(t.Context(), workspace, workerFilesFixture(), func() error {
		calls++
		if calls == 2 {
			if err := os.Rename(stage, stage+".original"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(stage, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	})
	requireCode(t, err, e.CodeImmutableViolation)
	if _, err := os.Stat(filepath.Join(workspace, "evidence")); !os.IsNotExist(err) {
		t.Fatal("substituted stage was committed")
	}
	if _, err := os.Stat(stage); err != nil {
		t.Fatal("failure cleanup removed the substituted directory")
	}
	entries, err := os.ReadDir(stage + ".original")
	if err != nil || len(entries) != 0 {
		t.Fatalf("held stage cleanup failed: %v", err)
	}
}
