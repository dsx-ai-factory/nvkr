// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package pipeline

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"golang.org/x/sys/unix"
)

// These fixtures are deliberately serial: /proc observes the real transport
// capabilities owned by this process, rather than a fake Close implementation.
func TestRunPackerContainerTransportLifetime(t *testing.T) {
	for _, test := range []struct {
		name, failAt string
		cancel       bool
		wantCode     e.Code
	}{
		{name: "success"},
		{name: "input copy failure", failAt: "copy", wantCode: e.CodeBuildFailed},
		{name: "input copy cancellation", failAt: "copy", cancel: true, wantCode: e.CodeTimeout},
		{name: "stage authentication failure", failAt: "stage", wantCode: e.CodeUnverifiedRef},
		{name: "stage removal failure", failAt: "remove-stage", wantCode: e.CodeInternal},
		{name: "validation failure", failAt: "validate", wantCode: e.CodeBuildFailed},
		{name: "build failure", failAt: "build", wantCode: e.CodeBuildFailed},
		{name: "build cancellation", failAt: "build", cancel: true, wantCode: e.CodeTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			baseline := packerTransportDescriptors(t)
			assertTransports := func(want int) {
				t.Helper()
				current := packerTransportDescriptors(t)
				for identity := range baseline {
					delete(current, identity)
				}
				if len(current) != want {
					t.Fatalf("open transport capabilities = %v, want %d", current, want)
				}
			}
			payload := []byte("borrowed producer material\n")
			material, err := heldfile.FromBytesContext(t.Context(), payload, heldfile.Options{
				Label: "transport-lifetime-material", MaxBytes: int64(len(payload)),
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := material.Close(); err != nil {
					t.Error(err)
				}
			})
			assertMaterial := func() {
				t.Helper()
				raw, err := material.ReadAllContext(t.Context(), int64(len(payload)))
				if err != nil || string(raw) != string(payload) {
					t.Fatalf("borrowed material was closed or changed: %q, %v", raw, err)
				}
			}
			request, runtime, built := newPackerContainerTestRun(t, "Packer v1.14.2\n", "The configuration is valid.\n", "", nil)
			request.materials = []BuildMaterialInput{{Path: "materials/fixture", SHA256: material.Digest(), Content: material}}
			ctx, cancel := context.WithCancel(testContext(t))
			defer cancel()
			failure := errors.New("controlled transport lifetime failure")
			fallback := runtime.run
			copies, authenticated, removed, consumers := 0, false, false, 0
			failed := false
			runtime.runContext = func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args []string) error {
				operation := ""
				switch {
				case len(args) > 1 && args[0] == "cp" && args[1] == "-":
					copies++
					assertTransports(2)
					operation = "copy"
				case len(args) > 2 && args[0] == "start" && strings.HasSuffix(args[2], "-stage"):
					assertTransports(2)
					if copies != 2 {
						t.Fatalf("stage started after %d transfers, want 2", copies)
					}
					authenticated = true
					operation = "stage"
				case len(args) > 2 && args[0] == "rm" && strings.HasSuffix(args[2], "-stage"):
					assertTransports(2)
					if !authenticated {
						t.Fatal("stage removed before authentication")
					}
					removed = true
					operation = "remove-stage"
				case len(args) > 2 && args[0] == "start":
					assertTransports(0)
					if !removed {
						t.Fatal("Packer started before authenticated stage removal")
					}
					consumers++
					for _, phase := range []string{"version", "validate", "build"} {
						if strings.HasSuffix(args[2], "-"+phase) {
							operation = phase
						}
					}
				}
				assertMaterial()
				if test.failAt != "" && operation == test.failAt {
					failed = true
					if test.cancel {
						cancel()
						return ctx.Err()
					}
					return failure
				}
				return fallback(stdin, stdout, stderr, args)
			}
			artifact, err := runPackerContainer(ctx, request)
			if artifact != nil {
				defer artifact.Close()
			}
			if code(err) != test.wantCode || (artifact != nil) != (test.wantCode == "") {
				t.Fatalf("artifact=%v error=%v, want code %s", artifact, err, test.wantCode)
			}
			if failed != (test.failAt != "") {
				t.Fatalf("selected failure stage %q reached=%t", test.failAt, failed)
			}
			if failed && !test.cancel && !errors.Is(err, failure) {
				t.Fatalf("primary runtime failure was lost: %v", err)
			}
			if test.cancel && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation was lost: %v", err)
			}
			if test.failAt == "" && (consumers != 3 || !*built) {
				t.Fatalf("version/validation/build reached=%d, build created=%t", consumers, *built)
			}
			assertTransports(0)
			assertMaterial()
			volumes := 0
			for _, call := range runtime.cleanups {
				if len(call) > 3 && call[0] == "volume" && call[1] == "rm" {
					volumes++
				}
			}
			if volumes != 2 {
				t.Fatalf("reaped %d volumes, want both attempt volumes", volumes)
			}
			containers := make(map[string]bool)
			for _, call := range runtime.calls {
				if len(call) > 2 && call[0] == "create" && call[1] == "--name" {
					containers[call[2]] = true
				}
			}
			for _, calls := range [][][]string{runtime.calls, runtime.cleanups} {
				for _, call := range calls {
					if len(call) > 2 && call[0] == "rm" && call[1] == "-fv" {
						delete(containers, call[2])
					}
				}
			}
			if len(containers) != 0 {
				t.Fatalf("attempt containers were not reaped: %v", containers)
			}
		})
	}
}

type packerTransportDescriptor struct {
	device, inode uint64
}

func packerTransportDescriptors(t *testing.T) map[packerTransportDescriptor]string {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[packerTransportDescriptor]string)
	for _, entry := range entries {
		path := "/proc/self/fd/" + entry.Name()
		target, err := os.Readlink(path)
		if err != nil || (target != "/memfd:nvkr-packer-input-carrier (deleted)" && target != "/memfd:nvkr-packer-work-transport (deleted)") {
			continue
		}
		var stat unix.Stat_t
		if err := unix.Stat(path, &stat); err != nil {
			t.Fatal(err)
		}
		result[packerTransportDescriptor{device: stat.Dev, inode: stat.Ino}] = target
	}
	return result
}
