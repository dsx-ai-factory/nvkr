// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package diskinspect

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

const diskPackageXML = `<operatingsystems><operatingsystem><name>linux</name><package_format>deb</package_format><applications><application><name>pkg</name><version>2.0</version><release>1ubuntu1</release><arch>arm64</arch></application></applications></operatingsystem></operatingsystems>`

func assertDependencyCode(t *testing.T, err error, want e.Code) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != want {
		t.Fatalf("error=%v, want %s", err, want)
	}
}

func TestDiskInspectorInvocation(t *testing.T) {
	if got := diskInspectorArguments("/private/subject.qcow2"); !slices.Equal(got,
		[]string{"--format=qcow2", "--keys-from-stdin", "--no-icon", "-a", "/private/subject.qcow2"}) {
		t.Fatalf("unexpected inspector arguments: %v", got)
	}
	want := []string{"PATH=/usr/bin:/bin", "HOME=/private", "TMPDIR=/private", "XDG_CONFIG_HOME=/private",
		"XDG_RUNTIME_DIR=/private", "LIBGUESTFS_CACHEDIR=/private",
		"LC_ALL=C", "TZ=UTC", "LIBGUESTFS_BACKEND=direct", "LIBGUESTFS_BACKEND_SETTINGS=force_tcg"}
	if got := diskInspectorEnvironment("/private"); !slices.Equal(got, want) {
		t.Fatalf("unexpected inspector environment: %v", got)
	}
}

func TestDiskInspectionCancellationBeforeSnapshotVerification(t *testing.T) {
	subject, err := heldfile.FromBytesContext(t.Context(), []byte("test subject"), heldfile.Options{Label: "cancellation fixture", MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	defer subject.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	inventory, err := inspectHeldDiskPackages(ctx, subject, "sha256:"+strings.Repeat("a", 64),
		func(_ context.Context, _, _ string, output, _ io.Writer) error {
			cancel()
			_, err := io.WriteString(output, diskPackageXML)
			return err
		})
	assertDependencyCode(t, err, e.CodeTimeout)
	if inventory != nil {
		t.Fatal("canceled snapshot verification issued an inventory")
	}
}

func TestDiskInspectionSnapshotProtocol(t *testing.T) {
	for _, tc := range []struct {
		name string
		code e.Code
		run  diskInspectCommand
	}{
		{"valid", "", func(_ context.Context, path, _ string, out, _ io.Writer) error {
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0o400 {
				return fmt.Errorf("snapshot is not read-only: %v", err)
			}
			_, err = io.WriteString(out, diskPackageXML)
			return err
		}},
		{"tool failed", e.CodeEnvelopeDecode, func(context.Context, string, string, io.Writer, io.Writer) error {
			return fmt.Errorf("failed fixture inspector")
		}},
		{"warning", e.CodeEnvelopeDecode, func(_ context.Context, _, _ string, out, diagnostic io.Writer) error {
			_, _ = io.WriteString(out, diskPackageXML)
			_, err := io.WriteString(diagnostic, "incomplete mount")
			return err
		}},
		{"snapshot substitution", e.CodeVerifyFailed, func(_ context.Context, path, _ string, out, _ io.Writer) error {
			if err := os.Chmod(path, 0o600); err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
				return err
			}
			_, err := io.WriteString(out, diskPackageXML)
			return err
		}},
		{"equal-size restored-mode mutation", e.CodeVerifyFailed, func(_ context.Context, path, _ string, out, _ io.Writer) error {
			if err := os.Chmod(path, 0o600); err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte("fake subject"), 0o600); err != nil {
				return err
			}
			if err := os.Chmod(path, 0o400); err != nil {
				return err
			}
			_, err := io.WriteString(out, diskPackageXML)
			return err
		}},
		{"permissions-only mutation", e.CodeVerifyFailed, func(_ context.Context, path, _ string, out, _ io.Writer) error {
			if err := os.Chmod(path, 0o600); err != nil {
				return err
			}
			_, err := io.WriteString(out, diskPackageXML)
			return err
		}},
		{"malformed report", e.CodeEnvelopeDecode, func(_ context.Context, _, _ string, out, _ io.Writer) error {
			_, err := io.WriteString(out, "broken")
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			held, err := heldfile.FromBytesContext(t.Context(), []byte("test subject"), heldfile.Options{Label: "disk-fixture", MaxBytes: 1024})
			if err != nil {
				t.Fatal(err)
			}
			defer held.Close()
			inventory, err := inspectHeldDiskPackages(t.Context(), held, "sha256:"+strings.Repeat("a", 64), tc.run)
			if tc.code != "" {
				assertDependencyCode(t, err, tc.code)
				if inventory != nil {
					t.Fatal("failed scan issued inventory")
				}
				return
			}
			if err != nil || len(inventory.Packages()) != 1 {
				t.Fatalf("inventory: %+v, %v", inventory, err)
			}
			digest, size, tool, _ := inventory.Binding()
			if digest != held.Digest() || size != held.Size() || tool != "sha256:"+strings.Repeat("a", 64) {
				t.Fatal("inventory is not bound to inspected bytes")
			}
			copy := inventory.Packages()
			copy[0].Name = "substituted"
			if inventory.Packages()[0].Name != "pkg" {
				t.Fatal("caller mutation changed inventory")
			}
		})
	}
}

func TestDiskInspectorDiagnosticBounds(t *testing.T) {
	subject, err := heldfile.FromBytesContext(t.Context(), []byte("test subject"), heldfile.Options{Label: "diagnostic fixture", MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	defer subject.Close()
	var workspace string
	_, err = inspectHeldDiskPackages(t.Context(), subject, "sha256:"+strings.Repeat("a", 64),
		func(_ context.Context, _, dir string, _, diagnostic io.Writer) error {
			workspace = dir
			_, _ = io.WriteString(diagnostic, "\x1b[31m"+strings.Repeat("x", MaxDiagnosticBytes+1))
			return fmt.Errorf("fixture failure")
		})
	assertDependencyCode(t, err, e.CodeEnvelopeDecode)
	if len(err.Error()) > MaxDiagnosticBytes+512 || strings.ContainsRune(err.Error(), '\x1b') || !strings.Contains(err.Error(), "truncated=true") {
		t.Fatalf("unsafe or unbounded diagnostic: bytes=%d", len(err.Error()))
	}
	if _, statErr := os.Stat(workspace); !os.IsNotExist(statErr) {
		t.Fatalf("private workspace survived inspection: %v", statErr)
	}
}
