// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativePreTenantFailureEvidencePrecedesBootstrapDeletion(t *testing.T) {
	for _, mode := range []string{"capture", "capture fails", "missing kubeconfig", "unjoined operation"} {
		t.Run(mode, func(t *testing.T) {
			root := privateTestDir(t)
			events := filepath.Join(root, "events")
			writeTenantFixture(t, filepath.Join(root, "kubectl"), []byte(`#!/bin/sh
set -eu
test "$1" = --kubeconfig
test "$2" = "$FIXTURE/site.kubeconfig"
printf 'capture\n' >> "$EVENTS"
if test "$MODE" = 'capture fails'; then exit 7; fi
printf 'private-diagnostic\n'
`), 0o700)
			writeTenantFixture(t, filepath.Join(root, "kind"), []byte("#!/bin/sh\nprintf 'kind-delete\\n' >> \"$EVENTS\"\n"), 0o700)
			if mode != "missing kubeconfig" {
				writeTenantFixture(t, filepath.Join(root, "site.kubeconfig"), []byte("private-config"), 0o600)
			}
			runner := Runner{Work: root, Env: []string{"PATH=/usr/bin:/bin", "FIXTURE=" + root, "EVENTS=" + events, "MODE=" + mode}}
			attempt := nativeSiteAttempt{assets: NativeAssets{HostTools: root}, bootstrapRunner: &runner, bootstrapName: "owned"}
			primary := errors.New("original deploy OpenAPI failure")
			if mode == "unjoined operation" {
				primary = errors.Join(primary, ErrUnjoined)
			}
			observed := attempt.preserveFailure(t.Context(), primary)
			if !errors.Is(observed, primary) || errors.Is(observed, ErrUnjoined) != errors.Is(primary, ErrUnjoined) {
				t.Fatalf("early capture changed original operation failure: %v", observed)
			}
			if mode == "unjoined operation" {
				if _, err := os.Stat(events); !os.IsNotExist(err) {
					t.Fatal("unjoined operation started a diagnostic child")
				}
				return
			}
			if err := attempt.close(t.Context()); err != nil {
				t.Fatalf("ordinary early diagnostic loss blocked cleanup: %v", err)
			}
			raw, err := os.ReadFile(events)
			want := strings.Repeat("capture\n", 6) + "kind-delete\n"
			if mode == "missing kubeconfig" {
				want = "kind-delete\n"
			}
			if err != nil || string(raw) != want {
				t.Fatalf("pretenant capture did not precede Kind deletion: %s %v", raw, err)
			}
		})
	}
}

func TestNativePreTenantFailureRequiresWrittenPrivateKubeconfig(t *testing.T) {
	for _, mode := range []string{"absent", "empty", "symlink", "public", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			root := privateTestDir(t)
			config := filepath.Join(root, "site.kubeconfig")
			switch mode {
			case "empty":
				writeTenantFixture(t, config, nil, 0o600)
			case "symlink":
				other := filepath.Join(root, "other-config")
				writeTenantFixture(t, other, []byte("private-config"), 0o600)
				if err := os.Symlink(other, config); err != nil {
					t.Fatal(err)
				}
			case "public":
				writeTenantFixture(t, config, []byte("private-config"), 0o644)
			case "canceled":
				writeTenantFixture(t, config, []byte("private-config"), 0o600)
			}
			runner := Runner{Work: root, Env: []string{"PATH=/usr/bin:/bin"}}
			attempt := nativeSiteAttempt{bootstrapRunner: &runner, assets: NativeAssets{HostTools: root}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "canceled" {
				cancel()
			}
			primary := errors.New("source deployment failed")
			if result := attempt.preserveFailure(ctx, primary); result != primary {
				t.Fatalf("unavailable diagnostic changed operation failure: %v", result)
			}
			folders, err := filepath.Glob(filepath.Join(root, "native-site-failure-*"))
			if err != nil || len(folders) != 0 {
				t.Fatalf("unsafe or absent source kubeconfig started capture: %v %v", folders, err)
			}
		})
	}
}

func TestNativeOptionalInstanceEvidenceIsBoundedPrivateAndBestEffort(t *testing.T) {
	for _, layout := range []string{"instances", "var/instances"} {
		for _, mode := range []string{"present", "absent", "runtime symlink", "vmnet symlink", "runtime oversized", "vmnet oversized", "storage failure", "already retained", "canceled", "unsafe ID"} {
			t.Run(layout+"/"+mode, func(t *testing.T) {
				simdir, out := privateTestDir(t), privateTestDir(t)
				id := "owned-instance"
				directory := filepath.Join(simdir, layout, id)
				if err := os.MkdirAll(directory, 0o700); err != nil {
					t.Fatal(err)
				}
				runtimeFile, vmnetFile := filepath.Join(directory, "runtime.json"), filepath.Join(directory, "vmnet-client.log")
				if mode != "absent" {
					writeTenantFixture(t, runtimeFile, []byte("private-runtime"), 0o600)
					writeTenantFixture(t, vmnetFile, []byte("private-vmnet"), 0o600)
				}
				// Distinct optional-file failures must not stop the other file.
				if strings.HasSuffix(mode, "symlink") {
					path := runtimeFile
					if mode == "vmnet symlink" {
						path = vmnetFile
					}
					if err := os.Rename(path, path+".original"); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(path+".original", path); err != nil {
						t.Fatal(err)
					}
				}
				if strings.HasSuffix(mode, "oversized") {
					path, size := runtimeFile, int64(1<<20)+1
					if mode == "vmnet oversized" {
						path, size = vmnetFile, int64(8<<20)+1
					}
					if err := os.Truncate(path, size); err != nil {
						t.Fatal(err)
					}
				}
				runtimeOut := filepath.Join(out, "native-runtime-"+id+".json")
				vmnetOut := filepath.Join(out, "native-vmnet-client-"+id+".log")
				if mode == "storage failure" {
					for _, path := range []string{runtimeOut, vmnetOut} {
						if err := os.Mkdir(path, 0o700); err != nil {
							t.Fatal(err)
						}
					}
				}
				if mode == "already retained" {
					for _, path := range []string{runtimeOut, vmnetOut} {
						writeTenantFixture(t, path, []byte("original evidence"), 0o600)
					}
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if mode == "canceled" {
					cancel()
				}
				if mode == "unsafe ID" {
					id = "../owned-instance"
				}
				retainNativeInstanceFiles(ctx, simdir, layout, out, id)
				for _, item := range []struct {
					path, prefix, want string
				}{{runtimeOut, "runtime", "private-runtime"}, {vmnetOut, "vmnet", "private-vmnet"}} {
					if mode == "storage failure" {
						if info, err := os.Stat(item.path); err != nil || !info.IsDir() {
							t.Fatal("optional storage failure replaced existing destination")
						}
						continue
					}
					raw, err := os.ReadFile(item.path)
					absent := mode == "absent" || mode == "canceled" || mode == "unsafe ID" || mode == item.prefix+" symlink" || mode == item.prefix+" oversized"
					if absent {
						if !os.IsNotExist(err) {
							t.Fatalf("unsafe/absent optional evidence retained: %s %v", raw, err)
						}
						continue
					}
					want := item.want
					if mode == "already retained" {
						want = "original evidence"
					}
					info, statErr := os.Stat(item.path)
					if err != nil || statErr != nil || string(raw) != want || info.Mode().Perm() != 0o600 {
						t.Fatalf("optional evidence not private/exact: %s %v %v", raw, err, statErr)
					}
				}
			})
		}
	}
}

func TestNativeConsoleRetentionMatchesPinnedBackend(t *testing.T) {
	for _, row := range []struct {
		layout string
		want   map[string]string
	}{
		{"instances", map[string]string{"native-console-owned-instance.log": "console.log"}},
		{"var/instances", map[string]string{
			"native-console-owned-instance-ttyS0.log": "disk.qcow2.ttyS0.log",
			"native-console-owned-instance-ttyS1.log": "disk.qcow2.ttyS1.log",
		}},
	} {
		for _, mode := range []string{"small", "at limit", "oversized"} {
			t.Run(row.layout+"/"+mode, func(t *testing.T) {
				simdir, out := privateTestDir(t), privateTestDir(t)
				if err := os.MkdirAll(filepath.Join(simdir, row.layout, "owned-instance"), 0o700); err != nil {
					t.Fatal(err)
				}
				contents := make(map[string][]byte)
				for _, name := range []string{"console.log", "disk.qcow2.ttyS0.log", "disk.qcow2.ttyS1.log"} {
					want := []byte("private-" + name)
					if mode != "small" {
						want = bytes.Repeat([]byte("."), 8<<20)
						copy(want, "retained-beginning-"+name)
						last := "latest-" + name
						copy(want[len(want)-len(last):], last)
					}
					contents[name] = want
					raw := want
					if mode == "oversized" {
						raw = append([]byte("discarded-prefix-"+name), want...)
					}
					writeTenantFixture(t, filepath.Join(simdir, row.layout, "owned-instance", name), raw, 0o600)
				}
				if err := retainNativeInstanceFiles(t.Context(), simdir, row.layout, out, "owned-instance"); err != nil {
					t.Fatal(err)
				}
				entries, err := os.ReadDir(out)
				if err != nil || len(entries) != len(row.want) {
					t.Fatalf("unexpected backend console membership: %v %v", entries, err)
				}
				for name, source := range row.want {
					path := filepath.Join(out, name)
					raw, err := os.ReadFile(path)
					info, statErr := os.Stat(path)
					if err != nil || statErr != nil || !bytes.Equal(raw, contents[source]) || info.Mode().Perm() != 0o600 {
						t.Fatalf("backend serial evidence not private and exact: %q %v %v", name, err, statErr)
					}
				}
			})
		}
	}
}

func TestNativeSerialRetentionFailurePreservesOtherConsole(t *testing.T) {
	for _, mode := range []string{"missing", "symlink", "already retained", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			simdir, out := privateTestDir(t), privateTestDir(t)
			first := filepath.Join(simdir, "var/instances/owned-instance/disk.qcow2.ttyS0.log")
			second := filepath.Join(simdir, "var/instances/owned-instance/disk.qcow2.ttyS1.log")
			firstOut := filepath.Join(out, "native-console-owned-instance-ttyS0.log")
			if err := os.MkdirAll(filepath.Dir(first), 0o700); err != nil {
				t.Fatal(err)
			}
			writeTenantFixture(t, second, []byte("private-second-console"), 0o600)
			if mode != "missing" {
				writeTenantFixture(t, first, []byte("private-first-console"), 0o600)
			}
			switch mode {
			case "symlink":
				if err := os.Rename(first, first+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(first+".original", first); err != nil {
					t.Fatal(err)
				}
			case "already retained":
				writeTenantFixture(t, firstOut, []byte("original evidence"), 0o600)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "canceled" {
				cancel()
			}
			err := retainNativeInstanceFiles(ctx, simdir, "var/instances", out, "owned-instance")
			if (err != nil) != (mode != "missing") {
				t.Fatalf("serial capture failure lost: %v", err)
			}
			raw, err := os.ReadFile(firstOut)
			if mode == "already retained" {
				if err != nil || string(raw) != "original evidence" {
					t.Fatal("original retained console replaced", err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatal("unsafe or unavailable first console was retained", err)
			}
			raw, err = os.ReadFile(filepath.Join(out, "native-console-owned-instance-ttyS1.log"))
			if mode == "canceled" {
				if !os.IsNotExist(err) {
					t.Fatal("canceled console capture created output", err)
				}
			} else if err != nil || string(raw) != "private-second-console" {
				t.Fatal("first console failure prevented second capture", err)
			}
		})
	}
}
