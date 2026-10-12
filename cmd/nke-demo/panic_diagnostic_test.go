// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/gdamore/tcell/v2"
)

func TestPanicDiagnosticsArePrivateAndPresentationIndependent(t *testing.T) {
	for _, mode := range []string{"plain", "worker", "renderer", "sudo", "canceled worker"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("TMPDIR", root)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "canceled worker" {
				cancel()
			}
			value := "sensitive panic value\nwith raw control \x1b"
			if mode == "renderer" {
				value = "private renderer panic"
			}
			cleaned := false
			work := func(context.Context, io.Writer) error { defer func() { cleaned = true }(); panic(value) }
			var public bytes.Buffer
			var err error
			switch mode {
			case "renderer":
				screen := &testDashboardScreen{SimulationScreen: tcell.NewSimulationScreen("UTF-8"), panicDraw: true}
				view := newDashboard()
				writer := &checkedWriter{out: view, observer: view, cancel: cancel, phases: make(map[string]time.Time)}
				err = dashboardLoop(ctx, cancel, screen, func() error { return nil }, writer, view, func(ctx context.Context, _ io.Writer) error {
					defer func() { cleaned = true }()
					<-ctx.Done()
					return nil
				})
			case "plain":
				err = progressOutput(ctx, &public, nil, false, "", work)
			case "sudo":
				err = withSudoSession(ctx, func(context.Context) error { return nil }, func(context.Context) error { return nil }, 30*time.Second, func(ctx context.Context, _ func(context.Context) error) error { return work(ctx, &public) })
			default:
				err = runProgressWork(ctx, &public, work)
			}
			assertCode(t, err, nkderr.CodeInternal)
			if !cleaned || strings.Contains(err.Error(), value) || strings.Contains(err.Error(), "sensitive") || strings.Contains(err.Error(), "goroutine") || public.Len() != 0 {
				t.Fatalf("panic skipped defers or leaked: %v %q", err, public.String())
			}
			files, readErr := os.ReadDir(root)
			if readErr != nil || len(files) != 1 {
				t.Fatalf("recovery file count=%d: %v", len(files), readErr)
			}
			name := filepath.Join(root, files[0].Name())
			info, statErr := os.Stat(name)
			if statErr != nil || info.Mode().Perm() != 0600 || info.Size() > 66<<10 {
				t.Fatalf("nonprivate or unbounded panic diagnostic: %v %v", info, statErr)
			}
			raw, readErr := os.ReadFile(name)
			if readErr != nil || !strings.Contains(string(raw), value) || !strings.Contains(string(raw), "runtime/debug.Stack") || !strings.Contains(string(raw), "TestPanicDiagnosticsArePrivateAndPresentationIndependent") {
				t.Fatalf("missing actual panic value/stack: %v", readErr)
			}
		})
	}
}

func TestPanicDiagnosticStorageFailureStillReportsInternal(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "absent"))
	err := recoveredPanic(t.Context(), "demo worker", "sensitive-value")
	assertCode(t, err, nkderr.CodeInternal)
	if !strings.Contains(err.Error(), "could not create") || strings.Contains(err.Error(), "sensitive-value") {
		t.Fatalf("storage failure leaked or was hidden: %v", err)
	}
}

type panicDiagnosticFormatter struct{ called *bool }

func (value panicDiagnosticFormatter) Format(fmt.State, rune) {
	*value.called = true
	panic("custom formatter must not execute in recovery")
}

type customRuntimePanicError struct{ called *bool }

func (value customRuntimePanicError) RuntimeError() {}
func (value customRuntimePanicError) Error() string {
	*value.called = true
	panic("custom runtime.Error must not execute in recovery")
}

func TestPanicDiagnosticTypeAndRuntimeDetails(t *testing.T) {
	panicValue := func(work func()) (value any) {
		defer func() { value = recover() }()
		work()
		return nil
	}
	called := false
	for _, test := range []struct {
		name  string
		value any
		want  []string
	}{
		{"standard error pointer", errors.New("private custom error text"), []string{"*errors.errorString", "value omitted"}},
		{"type assertion", panicValue(func() { var value any = 1; _ = value.(string) }), []string{"*runtime.TypeAssertionError", "interface conversion", "int", "string"}},
		{"bounds", panicValue(func() { _ = []int{}[1] }), []string{"runtime.boundsError", "index out of range", "length 0"}},
		{"nil runtime pointer", (*runtime.TypeAssertionError)(nil), []string{"*runtime.TypeAssertionError", "value omitted"}},
		{"incomplete runtime value", &runtime.TypeAssertionError{}, []string{"*runtime.TypeAssertionError", "runtime error detail unavailable"}},
		{"caller runtime error", customRuntimePanicError{&called}, []string{"main.customRuntimePanicError", "value omitted"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			text := boundedPanicValue(test.value)
			for _, want := range test.want {
				if !strings.Contains(text, want) {
					t.Fatalf("panic diagnostic omitted %q: %s", want, text)
				}
			}
			if called || len(text) > 35<<10 || strings.Contains(text, "private custom error text") {
				t.Fatalf("custom callback ran, private error formatted or bound exceeded: %s", text)
			}
		})
	}
}

func TestPanicValueDoesNotInvokeCallbacksOrTraverseComposites(t *testing.T) {
	called := false
	for _, test := range []struct {
		name  string
		value any
		want  string
	}{
		{"string", "actual primitive", "actual primitive"},
		{"large string", strings.Repeat("x", 1<<20), "[truncated]"},
		{"formatter", panicDiagnosticFormatter{&called}, "custom formatting are disabled"},
		{"composite", make([]string, 1<<20), "composite traversal"},
		{"integer", int64(-100), "-100"},
		{"unsigned", uint64(100), "100"},
		{"boolean", true, "true"},
		{"float", 1.5, "1.5"},
		{"complex", complex(1, 2), "(1+2i)"},
		{"nil", nil, "nil"},
	} {
		t.Run(test.name, func(t *testing.T) {
			text := boundedPanicValue(test.value)
			if len(text) > 35<<10 || !strings.Contains(text, test.want) || called {
				t.Fatalf("unsafe panic representation: len=%d callback=%v", len(text), called)
			}
		})
	}
}
