// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
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
			want := strings.Repeat("capture\n", 5) + "kind-delete\n"
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
