// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/runpresenter"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

type readOnlyDiagnosticWriter struct {
	t        *testing.T
	returned *bool
	bytes.Buffer
}

func (w *readOnlyDiagnosticWriter) Write(raw []byte) (int, error) {
	w.t.Helper()
	if !*w.returned {
		w.t.Fatal("private diagnostics replayed before read-only session returned")
	}
	return w.Buffer.Write(raw)
}

func TestDirectReadOnlyViewersReplayPrivateDiagnosticsAfterFailure(t *testing.T) {
	lifecycleCommandRepo(t)
	root, record, initial := runInspectionFixture(t, "reader-diagnostics")
	for _, command := range []struct {
		name string
		args []string
	}{
		{"plan", []string{"nvkr", "run", "plan", "--plan", ".github/nvkr-run/plan.json", "--ui=tui"}},
		{"inspect", []string{"nvkr", "run", "inspect", "--record", record, "--ui=tui"}},
	} {
		for _, fail := range []bool{false, true} {
			t.Run(command.name+map[bool]string{false: "/success", true: "/failure"}[fail], func(t *testing.T) {
				const sentinel = "private read-only panic stack sentinel"
				var out bytes.Buffer
				returned, shown := false, false
				diagnostics := &readOnlyDiagnosticWriter{t: t, returned: &returned}
				app := newApp()
				app.Writer, app.ErrWriter = &out, diagnostics
				app.Metadata = map[string]interface{}{commandPresentationServicesKey: runPresentationServices{
					eligible: func(io.Writer) bool { return true },
					show: func(_ context.Context, options runpresenter.Options, source runSnapshotSource, work func(context.Context) error) runpresenter.Result {
						shown = true
						defer func() { returned = true }()
						if options.Live || work != nil || options.Diagnostics == nil {
							t.Fatal("direct viewer starts work or lacks its bounded private capture")
						}
						if _, err := options.Diagnostics.Write([]byte(sentinel)); err != nil {
							t.Fatal(err)
						}
						public, err := json.Marshal(source.Snapshot())
						if err != nil || strings.Contains(string(public), sentinel) || out.Len() != 0 || diagnostics.Len() != 0 {
							t.Fatal("private capture reached an active viewer or public output", err)
						}
						if fail {
							return runpresenter.Result{PresentationErr: e.New(e.CodeUnavailable, "terminal input failed")}
						}
						return runpresenter.Result{}
					},
				}}
				err := app.RunContext(t.Context(), command.args)
				if !shown || !returned {
					t.Fatal("direct viewer was not exercised")
				}
				if fail {
					assertLifecycleCommandCode(t, err, e.CodeUnavailable)
					if !strings.Contains(diagnostics.String(), sentinel) {
						t.Fatal("failed direct viewer lost private diagnostic replay")
					}
				} else if err != nil || diagnostics.Len() != 0 {
					t.Fatal("successful direct viewer replayed private capture or changed its outcome", err)
				}
				if out.Len() != 0 {
					t.Fatal("direct viewer emitted private diagnostics or summary onto stdout")
				}
				after, err := os.ReadFile(record)
				if err != nil || !bytes.Equal(initial, after) {
					t.Fatal("read-only diagnostic capture changed the stored Run", err)
				}
				entries, err := os.ReadDir(root)
				if err != nil || len(entries) != 1 {
					t.Fatal("read-only diagnostic capture persisted extra files", err)
				}
			})
		}
	}
}
