// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/assemble"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
)

// Byte-distinct copies of the same ELF test program stand in for both tools. Unlike a fake callback,
// this exercises production descriptor execution without adding a tool lookup,
// ambient helper environment variable or a new compiled fixture program.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && (os.Args[1] == "info" || os.Args[1] == "--format=qcow2") {
		if err := diskInspectorProcessHelper(); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func diskInspectorProcessHelper() error {
	if os.Args[1] == "info" {
		if len(os.Args) != 6 || !slices.Equal(os.Args[1:5], []string{"info", "-f", "qcow2", "--output=json"}) || os.Args[5] != "/proc/self/fd/4" {
			return fmt.Errorf("unexpected probe argv: %q", os.Args)
		}
		raw, err := os.ReadFile(os.Args[5])
		if err != nil {
			return err
		}
		if bytes.Equal(raw, []byte("external")) {
			_, err = fmt.Fprint(os.Stdout, `{"format":"qcow2","virtual-size":12,"backing-filename":"base.raw"}`)
		} else {
			_, err = fmt.Fprint(os.Stdout, `{"format":"qcow2","virtual-size":12}`)
		}
		return err
	}
	if len(os.Args) != 6 || !slices.Equal(os.Args[1:5], []string{"--format=qcow2", "--keys-from-stdin", "--no-icon", "-a"}) {
		return fmt.Errorf("unexpected inspector argv: %q", os.Args)
	}
	workspace, err := os.Getwd()
	if err != nil {
		return err
	}
	if os.Args[5] != filepath.Join(workspace, "subject.qcow2") {
		return fmt.Errorf("snapshot is not beneath private cwd")
	}
	want, got := diskInspectorEnvironment(workspace), os.Environ()
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		return fmt.Errorf("unexpected inspector environment: %q", got)
	}
	// Tool mode bypasses m.Run, so Go's coverage exit hooks need their own
	// writable directory. Set it only after checking the exact inherited
	// production environment, beneath the caller-owned private workspace.
	if testing.CoverMode() != "" {
		if err := os.Setenv("GOCOVERDIR", workspace); err != nil {
			return err
		}
	}
	stdin, err := io.ReadAll(os.Stdin)
	if err != nil || len(stdin) != 0 {
		return fmt.Errorf("key input not empty: %v", err)
	}
	raw, err := os.ReadFile(os.Args[5])
	if err != nil {
		return err
	}
	if bytes.Equal(raw, []byte("external")) {
		return fmt.Errorf("inspector helper received an externally dependent subject")
	}
	if bytes.Equal(raw, []byte("wait")) {
		time.Sleep(time.Minute)
	}
	_, err = io.WriteString(os.Stdout, diskPackageXML)
	return err
}

func TestDiskInspectorProductionProcess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("held ELF execution requires Linux")
	}
	if os.Geteuid() == 0 {
		t.Skip("production diagnostic capability requires a non-root sandbox caller")
	}
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	toolDigest := rawDigest(raw)
	codecBytes := append(bytes.Clone(raw), 0)
	codecPath := filepath.Join(t.TempDir(), "codec-fixture")
	if err := os.WriteFile(codecPath, codecBytes, 0o500); err != nil {
		t.Fatal(err)
	}
	codecToolDigest := rawDigest(codecBytes)
	if codecToolDigest == toolDigest {
		t.Fatal("fixture tool identities must differ")
	}
	for _, tc := range []struct {
		name, subject string
		closed        bool
		code          e.Code
	}{
		{"success", "test subject", false, ""},
		{"external envelope rejected before inspector", "external", false, e.CodeEnvelopeDecode},
		{"closed inspector", "test subject", true, e.CodeEngineCapability},
		{"empty subject", "", false, e.CodeEngineCapability},
		{"canceled subprocess", "wait", false, e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			codec, err := assemble.OpenQemuImgCodec(ctx, codecPath, codecToolDigest)
			if err != nil {
				t.Fatal(err)
			}
			defer codec.Close()
			inspector, err := OpenDiskInspector(ctx, path, toolDigest, codec)
			if err != nil {
				t.Fatal(err)
			}
			defer inspector.Close()
			subject, err := heldfile.FromBytesContext(ctx, []byte(tc.subject), heldfile.Options{Label: "process fixture", MaxBytes: 1024})
			if err != nil {
				t.Fatal(err)
			}
			defer subject.Close()
			if tc.closed {
				if err := inspector.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if tc.name == "canceled subprocess" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 3*time.Second)
				defer cancel()
			}
			started := time.Now()
			inventory, err := inspector.InspectHeldInstalledPackages(ctx, subject, "qcow2", "none")
			if tc.code != "" {
				assertDependencyCode(t, err, tc.code)
				if tc.name == "canceled subprocess" && time.Since(started) >= 10*time.Second {
					t.Fatal("cancellation did not promptly join the sixty-second inspector helper")
				}
				if tc.subject == "external" && !strings.Contains(err.Error(), "not self-contained") {
					t.Fatalf("external subject was not rejected by the envelope gate: %v", err)
				}
				if tc.name == "empty subject" && !strings.Contains(err.Error(), "must be nonempty and within its diagnostic byte budget") {
					t.Fatalf("empty subject diagnostic describes the wrong admission failure: %v", err)
				}
				if inventory != nil {
					t.Fatal("failed production path issued inventory")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			digest, size, inspectorDigest, codecDigest := inventory.Binding()
			if digest != subject.Digest() || size != subject.Size() || inspectorDigest != toolDigest || codecDigest != codecToolDigest {
				t.Fatalf("inventory binding=%s/%d/%s/%s", digest, size, inspectorDigest, codecDigest)
			}
		})
	}
	codec, err := assemble.OpenQemuImgCodec(t.Context(), codecPath, codecToolDigest)
	if err != nil {
		t.Fatal(err)
	}
	defer codec.Close()
	_, err = OpenDiskInspector(t.Context(), path, "sha256:"+strings.Repeat("f", 64), codec)
	assertDependencyCode(t, err, e.CodeUnverifiedRef)
}

func TestDiskInspectorRejectsRootBeforeWork(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root admission negative requires uid zero")
	}
	var inspector DiskInspector
	_, err := inspector.InspectHeldInstalledPackages(t.Context(), nil, "qcow2", "none")
	assertDependencyCode(t, err, e.CodeEngineCapability)
	if !strings.Contains(err.Error(), "non-root sandbox") {
		t.Fatalf("root was not rejected first: %v", err)
	}
}
