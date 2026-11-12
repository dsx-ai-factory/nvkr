// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/urfave/cli/v2"
)

func TestRemovedInterfacesRejectBeforeResolution(t *testing.T) {
	for _, args := range [][]string{
		{"recipe"},
		{"bundle", "--engine", "gitlab", "--data", "must-not-be-read"},
		{"catalog", "recipes"},
		{"resolve", "--recipe", "old"},
		{"resolve", "--category", "kubernetes"},
		{"resolve", "--k8s", "1.32.0"},
		{"resolve", "--os", "ubuntu"},
		{"resolve", "--arch", "amd64"},
		{"resolve", "--accelerator", "h100"},
		{"resolve", "--service", "eks"},
		{"resolve", "--intent", "training"},
		{"build", "--role", "worker"},
		{"assemble", "--format", "raw", "--role", "worker"},
		{"assemble", "--format", "raw", "--staging-out", "removed.json"},
		{"publish", "root", "--cluster-definition", "removed.json", "--out", "removed-layout"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			output, err := runCLIWithCapturedStdout(t, newApp(), append([]string{"nvkr"}, args...)...)
			assertLifecycleCommandCode(t, err, e.CodeUsage)
			if len(output) != 0 {
				t.Fatalf("removed interface emitted output: %s", output)
			}
		})
	}
}

func TestCatalogValidateReportsProducers(t *testing.T) {
	output, err := runCatalogCommand(t, "catalog", "validate", "--data", layeredCatalogPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "8 component(s)") {
		t.Fatalf("wrong producer summary: %s", output)
	}
}

func TestCatalogListShowsDirectProducers(t *testing.T) {
	output, err := runCatalogCommand(t, "catalog", "list", "--data", layeredCatalogPath(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"components:", "kubectl"} {
		if !strings.Contains(output, want) {
			t.Fatalf("catalog list omitted %q: %s", want, output)
		}
	}
	for _, old := range []string{"recipes:", "overlays (layered", "mixins:"} {
		if strings.Contains(output, old) {
			t.Fatalf("catalog list exposes removed %q", old)
		}
	}
}

func TestCatalogListFiltersAndEmitsJSON(t *testing.T) {
	arm := filepath.Join("..", "..", "inputs", "catalogs", "tools", "operator-arm64")
	output, err := runCatalogCommand(t, "catalog", "list", "--data", arm, "--platform", "linux/arm64", "--output-type", "host-package", "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	var got []struct {
		Name         string   `json:"name"`
		Platforms    []string `json:"platforms"`
		OutputType   string   `json:"outputType"`
		ProducerMode string   `json:"producerMode"`
	}
	if err := json.Unmarshal([]byte(output), &got); err != nil {
		t.Fatalf("not JSON: %v: %s", err, output)
	}
	if len(got) != 2 || got[0].Name != "helm" || got[1].Name != "kubectl" || got[0].Platforms[0] != "linux/arm64" || got[0].ProducerMode != "acquired" {
		t.Fatalf("unexpected projection: %+v", got)
	}

	plain, err := runCatalogCommand(t, "catalog", "list", "--data", layeredCatalogPath(t), "--output-type", "host-package")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plain, "kubectl") || !strings.Contains(plain, "kubelet") || strings.Contains(plain, "pause") {
		t.Fatalf("filtered plain list has wrong membership: %s", plain)
	}
}

func TestCatalogListRejectsBadSelectorsWithoutOutput(t *testing.T) {
	arm := filepath.Join("..", "..", "inputs", "catalogs", "tools", "operator-arm64")
	for _, args := range [][]string{
		{"--platform", "arm64"},
		{"--output-type", "package"},
		{"--platform", ""},
		{"--platform", "linux/amd64", "--platform", "linux/arm64"},
		{"--output-type", "host-package", "--output-type", "raw"},
		{"--format", "yaml"},
	} {
		output, err := runCatalogCommand(t, append([]string{"catalog", "list", "--data", arm}, args...)...)
		assertLifecycleCommandCode(t, err, e.CodeUsage)
		if output != "" {
			t.Fatalf("%v emitted %q", args, output)
		}
	}
}

func TestCatalogListFormatsAgreeOnFilteredMembership(t *testing.T) {
	data := layeredCatalogPath(t)
	filter := []string{"catalog", "list", "--data", data, "--output-type", "host-package"}
	plain, err := runCatalogCommand(t, filter...)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := runCatalogCommand(t, append(filter, "--format", "json")...)
	if err != nil {
		t.Fatal(err)
	}
	var found []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(raw), &found); err != nil || len(found) == 0 {
		t.Fatalf("JSON list unusable: %v: %s", err, raw)
	}
	for _, c := range found {
		if !strings.Contains(plain, "  "+c.Name+" ") {
			t.Fatalf("component %q is in JSON but not in plain output: %s", c.Name, plain)
		}
	}
	if got := strings.Count(plain, "\n  "); got != len(found) {
		t.Fatalf("plain lists %d components, JSON lists %d", got, len(found))
	}
}

func TestCatalogListNoMatchIsNoMatchErrorInEveryFormat(t *testing.T) {
	arm := filepath.Join("..", "..", "inputs", "catalogs", "tools", "operator-arm64")
	for _, format := range []string{"text", "json"} {
		output, err := runCatalogCommand(t, "catalog", "list", "--data", arm, "--output-type", "tarball", "--format", format)
		assertLifecycleCommandCode(t, err, e.CodeNoMatch)
		if output != "" {
			t.Fatalf("%s no-match emitted %q", format, output)
		}
	}
}

func TestCatalogListChecksSelectorsBeforeLoadingTheCatalog(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")
	for _, args := range [][]string{{"--platform", "bogus"}, {"--output-type", "bogus"}, {"--platform", ""}} {
		output, err := runCatalogCommand(t, append([]string{"catalog", "list", "--data", missing}, args...)...)
		assertLifecycleCommandCode(t, err, e.CodeUsage)
		if output != "" {
			t.Fatalf("%v emitted %q", args, output)
		}
	}
}

func TestCatalogListAdmitsDarwinSelectorAndNoMatchesAbsentPlatform(t *testing.T) {
	native := filepath.Join("..", "..", "inputs", "catalogs", "tools", "native")
	output, err := runCatalogCommand(t, "catalog", "list", "--data", native, "--platform", "darwin/arm64", "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	var got []struct {
		Platforms []string `json:"platforms"`
	}
	if err := json.Unmarshal([]byte(output), &got); err != nil || len(got) == 0 {
		t.Fatalf("no darwin/arm64 components listed: %v: %s", err, output)
	}
	arm := filepath.Join("..", "..", "inputs", "catalogs", "tools", "operator-arm64")
	_, err = runCatalogCommand(t, "catalog", "list", "--data", arm, "--platform", "darwin/arm64")
	assertLifecycleCommandCode(t, err, e.CodeNoMatch)
}

func TestCatalogListValidatesWholeCatalogBeforeFiltering(t *testing.T) {
	dir := t.TempDir()
	registry, err := os.ReadFile(filepath.Join(layeredCatalogPath(t), "registry.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	broken := strings.Replace(string(registry), "components:\n", "components:\n  broken:\n    outputType: not-a-type\n    platforms:\n    - linux/amd64\n", 1)
	if broken == string(registry) {
		t.Fatal("fixture registry has no components key")
	}
	for name, content := range map[string]string{"registry.yaml": broken} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	lock, err := os.ReadFile(filepath.Join(layeredCatalogPath(t), "pins.lock.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pins.lock.yaml"), lock, 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := runCatalogCommand(t, "catalog", "list", "--data", dir, "--output-type", "host-package")
	assertLifecycleCommandCode(t, err, e.CodeInvalidCatalog)
	if output != "" {
		t.Fatalf("invalid catalog emitted %q", output)
	}
}

func runCatalogCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	oldStdout := os.Stdout
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writeEnd
	t.Cleanup(func() { os.Stdout = oldStdout })
	app := &cli.App{Commands: []*cli.Command{cmdCatalog()}}
	runErr := app.Run(append([]string{"nvkr"}, args...))
	if err := writeEnd.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = oldStdout
	raw, readErr := io.ReadAll(readEnd)
	if closeErr := readEnd.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if readErr != nil {
		t.Fatal(readErr)
	}
	return string(raw), runErr
}

func layeredCatalogPath(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "testdata", "examples", "layered-catalog"))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCatalogUpdateCIFailsBeforeOutputOrCatalogIO(t *testing.T) {
	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	t.Cleanup(func() { os.Stdout = oldStdout })
	app := &cli.App{Commands: []*cli.Command{cmdCatalog()}}
	runErr := app.Run([]string{"nvkr", "catalog", "update-ci", "--engine", "gitlab"})
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = oldStdout
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if cliErrorCode(runErr) != e.CodeEngineCapability {
		t.Fatalf("update-ci code = %q, want %q: %v",
			cliErrorCode(runErr), e.CodeEngineCapability, runErr)
	}
	if len(output) != 0 {
		t.Fatalf("unsupported update-ci emitted bytes: %q", output)
	}
}
