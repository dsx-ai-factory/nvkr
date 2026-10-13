// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/runpresenter"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/progress"
	"github.com/urfave/cli/v2"
)

func catalogPresentationApp(eligible bool, show func(progress.Snapshot) runpresenter.Result) *cli.App {
	return &cli.App{Commands: []*cli.Command{cmdCatalog()}, Metadata: map[string]interface{}{
		commandPresentationServicesKey: runPresentationServices{
			eligible: func(io.Writer) bool { return eligible },
			show: func(_ context.Context, options runpresenter.Options, source runSnapshotSource, work func(context.Context) error) runpresenter.Result {
				if work != nil || options.Live {
					panic("catalog presentation attempted execution")
				}
				return show(source.Snapshot())
			},
		},
	}}
}

func catalogLeafArguments() map[string][]string {
	return map[string][]string{
		"source-pin": {"--data", "must-not-be-read", "source"},
		"digest":     {"--data", "must-not-be-read"}, "validate": {"--data", "must-not-be-read"}, "list": {"--data", "must-not-be-read"},
		"pin": {"--dir", "must-not-be-read", "source"}, "bump": {"--dir", "must-not-be-read", "source"},
		"pin-artifacts": {"--dir", "must-not-be-read", "component"},
		"refresh":       {"--config", "must-not-be-read"}, "update-ci": {"--config", "must-not-be-read"},
	}
}

func TestCatalogPresentationAdmissionPrecedesOwnerIO(t *testing.T) {
	for leaf, flags := range catalogLeafArguments() {
		for _, mode := range []string{"tui", "unknown"} {
			t.Run(leaf+"/"+mode, func(t *testing.T) {
				shown := false
				app := catalogPresentationApp(false, func(progress.Snapshot) runpresenter.Result { shown = true; return runpresenter.Result{} })
				args := append([]string{"nvkr", "catalog", leaf, "--ui=" + mode}, flags...)
				output, err := runCLIWithCapturedStdout(t, app, args...)
				assertLifecycleCommandCode(t, err, e.CodeUsage)
				if shown || len(output) != 0 {
					t.Fatal("rejected presentation opened a viewer or emitted bytes")
				}
			})
		}
	}
	t.Run("explicit JSON conflict", func(t *testing.T) {
		shown := false
		app := catalogPresentationApp(true, func(progress.Snapshot) runpresenter.Result { shown = true; return runpresenter.Result{} })
		output, err := runCLIWithCapturedStdout(t, app, "nvkr", "catalog", "refresh", "--config", "must-not-be-read", "--ui=tui", "--json")
		assertLifecycleCommandCode(t, err, e.CodeUsage)
		if shown || len(output) != 0 {
			t.Fatal("JSON conflict performed presentation or output")
		}
	})
}

func TestCatalogPresentationSafeOwnerFailuresForEveryLeaf(t *testing.T) {
	for leaf, flags := range catalogLeafArguments() {
		t.Run(leaf, func(t *testing.T) {
			shown := false
			app := catalogPresentationApp(true, func(snapshot progress.Snapshot) runpresenter.Result {
				shown = true
				if snapshot.FailureCode != string(e.CodeInvalidCatalog) || snapshot.Mode != "command" {
					t.Errorf("owner code missing: %+v", snapshot)
				}
				raw, err := json.Marshal(snapshot)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(raw), "must-not-be-read") {
					t.Fatal("private failing path entered result browser")
				}
				return runpresenter.Result{}
			})
			output, err := runCLIWithCapturedStdout(t, app, append([]string{"nvkr", "catalog", leaf, "--ui=tui"}, flags...)...)
			assertLifecycleCommandCode(t, err, e.CodeInvalidCatalog)
			if !shown || len(output) != 0 {
				t.Fatal("owning failure bypassed safe viewer or printed ordinary output")
			}
		})
	}
}

func TestCatalogPresentationReadOnlyTypedResultsAndImplicitJSONOverride(t *testing.T) {
	data := filepath.Join("..", "..", "testdata", "catalogs", "kubernetes-layered")
	for _, leaf := range []string{"source-pin", "digest", "validate", "list"} {
		t.Run(leaf, func(t *testing.T) {
			var snapshot progress.Snapshot
			app := catalogPresentationApp(true, func(value progress.Snapshot) runpresenter.Result { snapshot = value; return runpresenter.Result{} })
			args := []string{"nvkr", "catalog", leaf, "--data", data, "--ui=tui"}
			if leaf == "source-pin" {
				args = append(args, "kubernetes")
			}
			output, err := runCLIWithCapturedStdout(t, app, args...)
			if err != nil {
				t.Fatal(err)
			}
			if len(output) != 0 || snapshot.Status != "complete" || snapshot.Title == "" {
				t.Fatalf("result was not an exclusively typed browser: %s %+v", output, snapshot)
			}
			if leaf == "digest" && len(snapshot.Fields) != 2 {
				t.Fatal("snapshot digest fields absent")
			}
			if leaf != "digest" && len(snapshot.Items) == 0 {
				t.Fatal("typed catalog items absent")
			}
			if leaf == "source-pin" && snapshot.Items[0].Fields[0].Value != "70d3cc986aa8221cd1dfb1121852688902d3bf53" {
				t.Fatal("source pin bypassed exact owning snapshot")
			}
		})
	}
	for _, mode := range []string{"", "plain"} {
		t.Run("implicit JSON/"+mode, func(t *testing.T) {
			app := catalogPresentationApp(true, func(progress.Snapshot) runpresenter.Result {
				t.Fatal("default/plain opened browser")
				return runpresenter.Result{}
			})
			args := []string{"nvkr", "catalog", "source-pin", "--data", data}
			if mode != "" {
				args = append(args, "--ui="+mode)
			}
			args = append(args, "kubernetes")
			output, err := runCLIWithCapturedStdout(t, app, args...)
			if err != nil {
				t.Fatal(err)
			}
			want := "{\"commit\":\"70d3cc986aa8221cd1dfb1121852688902d3bf53\",\"treeHash\":\"195abcc8a74b362b88fc76b0ee676a3354e4937e\"}\n"
			if string(output) != want {
				t.Fatalf("implicit JSON bytes changed: %s", output)
			}
		})
	}
}

func TestCatalogPinPresentationPrivacyAndCheckOutcome(t *testing.T) {
	secret := "private-sentinel"
	pin := catalog.Pin{Commit: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40), Tag: secret, DerivedFrom: secret, ToolchainImage: secret, Tool: &catalog.ToolPin{URL: secret}}
	artifact := catalog.ArtifactPin{Tag: secret, URL: secret, DerivedFrom: secret, ImageParentDigest: "sha256:" + strings.Repeat("c", 64), Digests: map[string]string{"arm64": "sha256:" + strings.Repeat("d", 64), "amd64": "sha256:" + strings.Repeat("e", 64)}}
	update := catalog.SourcePinUpdate{Changed: 2, Pins: []catalog.SourcePinChange{{Name: "source", After: pin, Changed: true}}, Artifacts: []catalog.ArtifactPinChange{{Name: "image", After: artifact, Changed: true}}}
	for _, test := range []struct {
		name         string
		check        bool
		err          error
		state, write string
	}{
		{name: "check", check: true, err: e.New(e.CodeDiff, "%s", secret), state: "changed", write: "proposed; no write requested"},
		{name: "written", state: "changed", write: "written"},
		{name: "partial error", err: e.New(e.CodeWriteConflict, "%s", secret), state: "resolved", write: "resolution description; write outcome unconfirmed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			display := catalogSourceUpdateDisplay(update, test.check, test.err)
			raw, err := json.Marshal(display)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), secret) {
				t.Fatal("transport/tool/error details leaked into typed projection")
			}
			if display.Fields[1].Value != test.write || display.Items[0].State != test.state || display.Items[1].State != test.state {
				t.Fatalf("proposal/write/error conflated: %+v", display)
			}
			if display.Items[1].Fields[3].Label != "amd64 previous digest" {
				t.Fatal("architecture fields not deterministic")
			}
			var resultCode string
			app := catalogPresentationApp(true, func(snapshot progress.Snapshot) runpresenter.Result {
				resultCode = snapshot.FailureCode
				return runpresenter.Result{}
			})
			app.Commands = []*cli.Command{{Name: "present", Flags: commandPresentationFlags(), Action: func(c *cli.Context) error {
				view, err := selectCommandPresentation(c)
				if err != nil {
					return err
				}
				return showCommandResult(c, view, display, test.err)
			}}}
			_, err = runCLIWithCapturedStdout(t, app, "nvkr", "present", "--ui=tui")
			if test.err != nil && (!errors.Is(err, test.err) || resultCode != string(runErrorCode(test.err)) || e.ExitCode(err) != e.ExitCode(test.err)) {
				t.Fatalf("owning check/failure changed: %v", err)
			}
		})
	}
}

func TestCatalogComponentPresentationOmitsPrivateProducerConfiguration(t *testing.T) {
	const secret = "private-producer-sentinel"
	loaded := &catalog.Catalog{Components: map[string]catalog.Component{"component": {
		FromSource: true, Platforms: []string{"linux/amd64"}, BuilderType: "docker", OutputType: "executable",
		Repo: secret, Tag: secret, ArtifactPath: secret, WriteDir: secret, ToolchainImage: secret,
		BuildCommand: []string{secret}, VersionStamp: map[string]string{secret: secret},
		Prebuilt: &catalog.Prebuilt{URL: secret, Image: secret},
	}}}
	display := catalogComponentsDisplay(loaded, "Catalog components")
	raw, err := json.Marshal(display)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatal("private producer configuration entered typed display")
	}
	if len(display.Items) != 1 || display.Items[0].Name != "component" || display.Items[0].Fields[2].Value != "linux/amd64" {
		t.Fatal("public component/platform fields omitted")
	}
}
