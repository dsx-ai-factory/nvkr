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

func TestReleaseVerifyExposesAndValidatesDiskEvidencePolicy(t *testing.T) {
	var out bytes.Buffer
	app := &cli.App{Commands: []*cli.Command{cmdRelease()}, Writer: &out}
	err := app.RunContext(t.Context(), []string{"nkdctl", "release", "verify", "--help"})
	if err != nil || !strings.Contains(out.String(), "minimum-disk-evidence") || !strings.Contains(out.String(), "build-provenance-v1") {
		t.Fatalf("release verify help omitted disk evidence policy: err=%v output=%q", err, out.String())
	}
	if !strings.Contains(out.String(), "build-provenance-v1 for pipeline-attested; strict for qualified") {
		t.Fatalf("release verify help omitted lane-specific defaults: %q", out.String())
	}

	out.Reset()
	app = &cli.App{Commands: []*cli.Command{cmdRelease()}, Writer: &out}
	err = app.RunContext(t.Context(), []string{"nkdctl", "release", "verify", "--reference", "registry.example.test/product@sha256:" + strings.Repeat("a", 64),
		"--lane", "qualified", "--scope", "release", "--minimum-disk-evidence", "build-provenance-v1"})
	if cliErrorCode(err) != e.CodeTrustPolicyMissing || out.Len() != 0 {
		t.Fatalf("qualified producer-only policy was not rejected before adapters: err=%v output=%q", err, out.String())
	}

	out.Reset()
	app = &cli.App{Commands: []*cli.Command{cmdRelease()}, Writer: &out}
	err = app.RunContext(t.Context(), []string{"nkdctl", "release", "verify", "--reference", "registry.example.test/product@sha256:" + strings.Repeat("a", 64),
		"--lane", "qualified", "--scope", "release"})
	if cliErrorCode(err) != e.CodeEngineCapability || out.Len() != 0 {
		t.Fatalf("qualified default was weakened or reached an adapter: err=%v output=%q", err, out.String())
	}
}
