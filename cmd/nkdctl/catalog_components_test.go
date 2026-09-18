package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
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
			output, err := runCLIWithCapturedStdout(t, newApp(), append([]string{"nkdctl"}, args...)...)
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
	if !strings.Contains(output, "7 component(s), 1 AICR profile(s)") {
		t.Fatalf("wrong producer summary: %s", output)
	}
}

func TestCatalogListShowsDirectProducersAndProfiles(t *testing.T) {
	output, err := runCatalogCommand(t, "catalog", "list", "--data", layeredCatalogPath(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"components:", "kubectl", "AICR profiles:", "h100-runtime"} {
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
	runErr := app.Run(append([]string{"nkdctl"}, args...))
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
	runErr := app.Run([]string{"nkdctl", "catalog", "update-ci", "--dir", "must-not-be-read"})
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
