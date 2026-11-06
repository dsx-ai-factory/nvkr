// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalogmaintenance"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/urfave/cli/v2"
)

func TestCatalogRegenerateOverlaysFlags(t *testing.T) {
	help, err := runCatalogCommand(t, "catalog", "regenerate-overlays", "--help")
	if err != nil || !strings.Contains(help, "producer overlay maintenance inputs, relative to repository root") {
		t.Fatalf("overlay-specific configuration help missing: %v\n%s", err, help)
	}
	for _, tc := range []struct {
		name    string
		args    []string
		timeout time.Duration
	}{
		{"required config", nil, 0},
		{"default timeout", []string{"--config", "must-not-be-read"}, 15 * time.Minute},
		{"explicit timeout", []string{"--config", "must-not-be-read", "--timeout", "45s"}, 45 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := &cli.App{Commands: []*cli.Command{cmdCatalog()}}
			admitted := errors.New("flags admitted; stop before owner I/O")
			for _, command := range app.Commands[0].Subcommands {
				if command.Name == "regenerate-overlays" {
					command.Before = func(c *cli.Context) error {
						if c.Duration("timeout") != tc.timeout {
							t.Fatalf("timeout = %v, want %v", c.Duration("timeout"), tc.timeout)
						}
						return admitted
					}
				}
			}
			_, err := runCLIWithCapturedStdout(t, app, append([]string{"nvkr", "catalog", "regenerate-overlays"}, tc.args...)...)
			if tc.args == nil {
				if err == nil || !strings.Contains(err.Error(), `Required flag "config" not set`) {
					t.Fatalf("missing config was not rejected before owner I/O: %v", err)
				}
			} else if !errors.Is(err, admitted) {
				t.Fatalf("flag parsing did not reach the command: %v", err)
			}
		})
	}
	for _, timeout := range []string{"0s", "-1s"} {
		_, err := runCatalogCommand(t, "catalog", "regenerate-overlays", "--config", "must-not-be-read", "--timeout", timeout)
		assertLifecycleCommandCode(t, err, e.CodeUsage)
	}
}

func TestCatalogRegenerateOverlaysCheckDoesNotWrite(t *testing.T) {
	fixture, err := filepath.Abs("../../testdata/catalogs/kubernetes-layered")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	if err := os.CopyFS("catalog", os.DirFS(fixture)); err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.LoadContext(t.Context(), catalog.Dir("catalog"))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := cat.SnapshotDigest()
	if err != nil {
		t.Fatal(err)
	}
	const destination = "inputs/catalogs/copy.json"
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(path string, value any) []byte {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	component := cat.Components["cni-plugins"]
	// A compact valid overlay differs from the owner's generated formatting.
	before := write(destination, catalog.Overlay{APIVersion: catalog.OverlayAPIVersion, Kind: catalog.OverlayKind, Name: "copy", RequiresCatalogDigest: digest,
		Components: map[string]catalog.ComponentReplacement{"cni-plugins": {Component: &component}}})
	write("overlays.json", catalogmaintenance.ProducerOverlayInputs{APIVersion: "nvkr.dev/producer-overlay-maintenance/v1", Overlays: []catalogmaintenance.ProducerOverlayInput{
		{Name: "copy", Destination: destination, Base: "catalog", Producers: []catalogmaintenance.ProducerCopy{{Catalog: "catalog", Components: []string{"cni-plugins"}}}},
	}})
	args := []string{"catalog", "regenerate-overlays", "--config", "overlays.json"}
	_, err = runCatalogCommand(t, append(args, "--check")...)
	assertLifecycleCommandCode(t, err, e.CodePipelineDrift)
	after, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("check mode changed the overlay: %v", err)
	}
	entries, err := os.ReadDir(".")
	if err != nil || len(entries) != 3 {
		t.Fatalf("check mode created workspace files: %v %v", entries, err)
	}
	if _, err := runCatalogCommand(t, args...); err != nil {
		t.Fatal(err)
	}
	if _, err := runCatalogCommand(t, append(args, "--check")...); err != nil {
		t.Fatal(err)
	}
}
