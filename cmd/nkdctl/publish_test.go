package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/assemble"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/urfave/cli/v2"
)

func TestPublishBundleRootInspectExtractCLI(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "nkdctl")
	if err := os.WriteFile(binary, []byte("exact-tool"), 0o755); err != nil {
		t.Fatal(err)
	}
	app := &cli.App{Commands: []*cli.Command{cmdPublish()}}
	bundle := filepath.Join(root, "bundle")
	if err := app.RunContext(context.Background(), []string{"nkdctl", "publish", "bundle", "--file", "nkdctl=" + binary,
		"--arch", "amd64", "--out", bundle}); err != nil {
		t.Fatal(err)
	}
	publication := filepath.Join(root, "publication")
	if err := app.RunContext(context.Background(), []string{"nkdctl", "publish", "root", "--bundle", "tools=" + bundle,
		"--out", publication}); err != nil {
		t.Fatal(err)
	}
	if err := app.RunContext(context.Background(), []string{"nkdctl", "publish", "inspect", "--layout", publication}); err != nil {
		t.Fatal(err)
	}
	extracted := filepath.Join(root, "extracted")
	if err := app.RunContext(context.Background(), []string{"nkdctl", "publish", "extract", "--layout", publication,
		"--bundle", "tools", "--out", extracted}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(extracted, "nkdctl"))
	if err != nil || string(raw) != "exact-tool" {
		t.Fatalf("extracted bytes = %q, %v", raw, err)
	}

	// Diagnostic publication needs an explicit acknowledgment. The invalid target
	// must never be contacted when that admission is missing.
	err = app.RunContext(context.Background(), []string{"nkdctl", "publish", "push", "--layout", publication,
		"--to", "127.0.0.1:1/nkd/test:latest", "--plain-http", "--anonymous"})
	if cliErrorCode(err) != nkderr.CodeGateUnmet {
		t.Fatalf("push admission code = %q (%v)", cliErrorCode(err), err)
	}
}

func TestPublishAPTCLI(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "crictl")
	if err := os.WriteFile(binary, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	deb := filepath.Join(root, "crictl.deb")
	if _, err := assemble.Deb(binary, "crictl", "1.34.0", 1_748_736_000, "amd64", deb); err != nil { //nolint:staticcheck // creates an explicitly unqualified APT-projection fixture
		t.Fatal(err)
	}
	output := filepath.Join(root, "apt")
	app := &cli.App{Commands: []*cli.Command{cmdPublish()}}
	if err := app.RunContext(context.Background(), []string{"nkdctl", "publish", "apt", "--package", deb,
		"--distribution", "stable", "--component", "main", "--epoch", "1748736000", "--out", output}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(output, "dists", "stable", "Release")); err != nil {
		t.Fatal(err)
	}
}
