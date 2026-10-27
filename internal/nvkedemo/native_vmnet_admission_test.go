// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestNativeVMNetPairMatchesNICoSelectionAndExactBundle(t *testing.T) {
	for _, tc := range []struct {
		name string
		ok   bool
	}{
		{"primary", true}, {"secondary", true}, {"directory skipped", true}, {"installed symlink", true},
		{"client mismatch", false}, {"helper mismatch", false}, {"mismatch does not fall back", false},
		{"missing client", false}, {"missing helper", false}, {"not executable", false},
		{"writable executable", false}, {"writable ancestor", false}, {"selected symlink", false},
		{"oversized installed", false}, {"canceled", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := privateTestDir(t)
			tools, installed, alternate := filepath.Join(root, "tools"), filepath.Join(root, "installed"), filepath.Join(root, "alternate")
			for _, dir := range []string{tools, installed, alternate} {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			client, second, helper := filepath.Join(installed, "vmnet-client"), filepath.Join(alternate, "vmnet-client"), filepath.Join(installed, "vmnet-helper")
			for _, member := range []struct{ path, content string }{
				{filepath.Join(tools, "vmnet-client"), "selected-client"}, {filepath.Join(tools, "vmnet-helper"), "selected-helper"},
				{client, "selected-client"}, {second, "selected-client"}, {helper, "selected-helper"},
			} {
				if err := os.WriteFile(member.path, []byte(member.content), 0700); err != nil {
					t.Fatal(err)
				}
			}
			ctx := t.Context()
			var err error
			switch tc.name {
			case "secondary":
				client += "-absent"
			case "directory skipped":
				client = installed
			case "installed symlink":
				link := client + "-link"
				err = os.Symlink(client, link)
				client = link
			case "client mismatch", "mismatch does not fall back":
				err = os.WriteFile(client, []byte("different-bytes"), 0700)
			case "helper mismatch":
				err = os.WriteFile(helper, []byte("different-bytes"), 0700)
			case "missing client":
				client, second = client+"-absent", second+"-absent"
			case "missing helper":
				helper += "-absent"
			case "not executable":
				err = os.Chmod(client, 0600)
			case "writable executable":
				err = os.Chmod(client, 0720)
			case "writable ancestor":
				err = os.Chmod(installed, 0770)
			case "selected symlink":
				selected := filepath.Join(tools, "vmnet-client")
				err = os.Rename(selected, selected+"-original")
				if err == nil {
					err = os.Symlink(selected+"-original", selected)
				}
			case "oversized installed":
				err = os.Truncate(client, 257<<20)
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if err != nil {
				t.Fatal(err)
			}
			err = admitNativeVMNetPair(ctx, tools, []string{client, second}, helper)
			if (err == nil) != tc.ok {
				t.Fatalf("installed pair admission=%v, want accepted=%v", err, tc.ok)
			}
			if tc.name == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation identity lost", err)
			}
		})
	}
}

func TestNativeVMNetAdmissionBoundary(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name, platform string
		ctx            context.Context
		code           nvkrerr.Code
	}{
		{"Linux requires no installed vmnet", "linux", t.Context(), ""},
		{"missing selected bundle", "darwin", t.Context(), nvkrerr.CodeEngineCapability},
		{"canceled", "darwin", canceled, nvkrerr.CodeTimeout},
		{"nil context", "darwin", nil, nvkrerr.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := admitNativeVMNet(tc.ctx, tc.platform, filepath.Join(privateTestDir(t), "absent"))
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var coded *nvkrerr.Error
			if !errors.As(err, &coded) || coded.Code != tc.code {
				t.Fatalf("unexpected admission boundary: %v", err)
			}
		})
	}
}
