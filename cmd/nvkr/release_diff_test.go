// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/runpresenter"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/progress"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/releaseview"
	"github.com/urfave/cli/v2"
)

func TestReleaseDiffRejectsOptionsBeforeAcquisition(t *testing.T) {
	pinned := "127.0.0.1:1/releases@sha256:" + strings.Repeat("a", 64)
	for _, test := range []struct {
		extra []string
		code  e.Code
	}{
		{[]string{"--format", "mermaid"}, e.CodeUsage},
		{[]string{"--timeout", "0s"}, e.CodeUsage},
		{[]string{"unexpected"}, e.CodeUsage},
		{[]string{"--format", "json", "--ui=tui"}, e.CodeUsage},
		{[]string{"--after", "127.0.0.1:1/releases:latest"}, e.CodeUnverifiedRef},
	} {
		t.Run(strings.Join(test.extra, " "), func(t *testing.T) {
			var out bytes.Buffer
			workspace := filepath.Join(t.TempDir(), "must-not-exist")
			app := &cli.App{Commands: []*cli.Command{cmdRelease()}, Writer: &out}
			args := []string{"nvkr", "release", "diff", "--before", pinned, "--after", pinned, "--workspace", workspace}
			err := app.RunContext(t.Context(), append(args, test.extra...))
			if cliErrorCode(err) != test.code || out.Len() != 0 {
				t.Fatalf("invalid options reached acquisition or output: %v, %q", err, out.String())
			}
			if _, err := os.Stat(workspace); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rejected diff touched workspace: %v", err)
			}
		})
	}
}

func TestReleaseDiffComparesLocalLayoutsWithOptInExitCode(t *testing.T) {
	amd, arm := releaseBrowsePayloadFixture(t, "amd64"), releaseBrowsePayloadFixture(t, "arm64")
	reference := func(layout string) string {
		inspection, err := delivery.Inspect(t.Context(), layout)
		if err != nil {
			t.Fatal(err)
		}
		return "registry.example.test/releases@" + inspection.RootDigest
	}
	amdRef, armRef := reference(amd.layout), reference(arm.layout)
	run := func(t *testing.T, before, beforeLayout, after, afterLayout string, extra ...string) (string, error) {
		t.Helper()
		var out bytes.Buffer
		app := &cli.App{Commands: []*cli.Command{cmdRelease()}, Writer: &out}
		args := []string{"nvkr", "release", "diff", "--before", before, "--before-layout", beforeLayout, "--after", after, "--after-layout", afterLayout}
		err := app.RunContext(t.Context(), append(args, extra...))
		return out.String(), err
	}
	for _, exitCode := range []bool{false, true} {
		var extra []string
		if exitCode {
			extra = []string{"--exit-code"}
		}
		raw, err := run(t, amdRef, amd.layout, amdRef, amd.layout, extra...)
		if err != nil || !strings.Contains(raw, "Added: 0, removed: 0, changed: 0 (0 reference only), unchanged: 2\n") {
			t.Fatalf("identical inventories: %q, %v", raw, err)
		}
		raw, err = run(t, amdRef, amd.layout, armRef, arm.layout, append(extra, "--format", "json")...)
		if exitCode {
			assertLifecycleCommandCode(t, err, e.CodeDiff)
		} else if err != nil {
			t.Fatal(err)
		}
		var comparison releaseview.Comparison
		if err := json.Unmarshal([]byte(raw), &comparison); err != nil {
			t.Fatalf("differences did not print a comparison: %q, %v", raw, err)
		}
		if len(comparison.Changed) != 2 || comparison.Changed[0].Name != "node-image" || comparison.Changed[1].Name != "tools" ||
			comparison.Changed[1].Before.Arch != "amd64" || comparison.Changed[1].After.Arch != "arm64" || comparison.Changed[1].ReferenceOnly {
			t.Fatalf("platform change was not reported per named output: %s", raw)
		}
	}
}

func TestReleaseDiffTUIShowsDifferences(t *testing.T) {
	amd, arm := releaseBrowsePayloadFixture(t, "amd64"), releaseBrowsePayloadFixture(t, "arm64")
	reference := func(layout string) string {
		inspection, err := delivery.Inspect(t.Context(), layout)
		if err != nil {
			t.Fatal(err)
		}
		return "registry.example.test/releases@" + inspection.RootDigest
	}
	var snapshot progress.Snapshot
	services := runPresentationServices{eligible: func(io.Writer) bool { return true }, show: func(_ context.Context, _ runpresenter.Options, source runSnapshotSource, _ func(context.Context) error) runpresenter.Result {
		snapshot = source.Snapshot()
		return runpresenter.Result{}
	}}
	var out bytes.Buffer
	app := releasePresentationTestApp(executeReleaseVerification, &out, services)
	err := app.RunContext(t.Context(), []string{"nvkr", "release", "diff", "--before", reference(amd.layout), "--before-layout", amd.layout,
		"--after", reference(arm.layout), "--after-layout", arm.layout, "--exit-code", "--ui=tui"})
	assertLifecycleCommandCode(t, err, e.CodeDiff)
	if snapshot.Status != "different" || len(snapshot.Items) != 2 || snapshot.Items[0].State != "changed" || out.Len() != 0 ||
		!strings.Contains(snapshot.Note, "Not compared: deployment-compatibility, evidence-documents, policy-acceptance, signatures") {
		t.Fatalf("TUI comparison: %+v, stdout=%q", snapshot, out.String())
	}
	var tools *progress.Item
	for index := range snapshot.Items {
		if snapshot.Items[index].Name == "tools" {
			tools = &snapshot.Items[index]
		}
	}
	fields := map[string]string{}
	if tools != nil {
		for _, field := range tools.Fields {
			fields[field.Label] = field.Value
		}
	}
	// A changed field shows both values, and no transport reference reaches the viewer.
	if fields["arch before"] != "amd64" || fields["arch after"] != "arm64" || fields["Changed field"] != "reference" {
		t.Fatalf("TUI tools fields: %+v", fields)
	}
	for _, field := range tools.Fields {
		if strings.Contains(field.Value, "registry.example.test") {
			t.Fatalf("TUI shows a transport reference: %+v", field)
		}
	}
}

// A value longer than the viewer's field bound sits in its own field, so the
// other side of the change stays complete.
func TestReleaseComparisonDisplayKeepsAfterValueBesideLongBefore(t *testing.T) {
	long := make([]string, 400)
	for index := range long {
		long[index] = fmt.Sprintf("component-%03d", index)
	}
	change := releaseview.OutputChange{Name: "tools", Fields: []string{"components"},
		Before: releaseview.Output{Name: "tools", Kind: "executable", Components: long},
		After:  releaseview.Output{Name: "tools", Kind: "executable", Components: []string{"cli"}}}
	display := releaseComparisonDisplay(releaseview.Comparison{Changed: []releaseview.OutputChange{change}})
	fields := map[string]string{}
	for _, field := range display.Items[0].Fields {
		fields[field.Label] = field.Value
	}
	if len(fields["components before"]) <= 4096 || fields["components after"] != `["cli"]` {
		t.Fatalf("before %d bytes, after %q", len(fields["components before"]), fields["components after"])
	}
	if !strings.Contains(display.Note, "--format json") {
		t.Fatalf("note does not point to complete values: %q", display.Note)
	}
}
