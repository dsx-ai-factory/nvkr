package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/urfave/cli/v2"
)

func TestReleaseBrowseRejectsPresentationBeforeAcquisition(t *testing.T) {
	for _, extra := range [][]string{{"--format", "html"}, {"--timeout", "0s"}, {"--layout", "missing", "--anonymous"}, {"unexpected"}} {
		var out bytes.Buffer
		app := &cli.App{Commands: []*cli.Command{cmdRelease()}, Writer: &out}
		args := []string{"nkdctl", "release", "browse", "--reference", "127.0.0.1:1/releases@sha256:" + strings.Repeat("a", 64), "--workspace", filepath.Join(t.TempDir(), "must-not-exist")}
		err := app.RunContext(t.Context(), append(args, extra...))
		if cliErrorCode(err) != e.CodeUsage || out.Len() != 0 {
			t.Fatalf("invalid options reached acquisition or output: %v, %q", err, out.String())
		}
	}
}

func TestReleaseBrowseRejectsInvalidReferencesWithoutEffects(t *testing.T) {
	for _, reference := range []string{"", " ", "registry.example.test/releases", "registry.example.test/releases:latest", "registry.example.test/releases@sha256:bad"} {
		t.Run(reference, func(t *testing.T) {
			var out bytes.Buffer
			app := &cli.App{Commands: []*cli.Command{cmdRelease()}, Writer: &out}
			workspace := filepath.Join(t.TempDir(), "must-not-exist")
			err := app.RunContext(t.Context(), []string{"nkdctl", "release", "browse", "--reference", reference, "--workspace", workspace})
			if cliErrorCode(err) != e.CodeUnverifiedRef || out.Len() != 0 {
				t.Fatalf("invalid reference reached acquisition or output: %v, %q", err, out.String())
			}
			if _, err := os.Stat(workspace); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid reference touched workspace: %v", err)
			}
		})
	}
}

func TestReleaseVerifyRequiresCompleteExactInputSetBeforeAcquisition(t *testing.T) {
	for _, flags := range [][]string{
		{"--definition", "definition.yaml"},
		{"--environment", "environment.yaml"},
		{"--plan", "plan.json"},
		{"--definition", "definition.yaml", "--environment", "environment.yaml"},
		{"--definition", "definition.yaml", "--plan", "plan.json"},
		{"--environment", "environment.yaml", "--plan", "plan.json"},
	} {
		workspace := filepath.Join(t.TempDir(), "must-not-exist")
		args := []string{"nkdctl", "release", "verify",
			"--reference", "127.0.0.1:1/releases@sha256:" + strings.Repeat("a", 64),
			"--lane", "pipeline-attested", "--scope", "artifact-publication",
			"--identity", "^builder$", "--issuer", "issuer", "--workspace", workspace,
		}
		app := &cli.App{Commands: []*cli.Command{cmdRelease()}}
		err := app.RunContext(t.Context(), append(args, flags...))
		if cliErrorCode(err) != e.CodeUsage {
			t.Fatalf("partial exact-input flags reached acquisition: flags=%v error=%v", flags, err)
		}
		if _, err := os.Stat(workspace); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("partial exact-input flags touched workspace: %v", err)
		}
	}
}
