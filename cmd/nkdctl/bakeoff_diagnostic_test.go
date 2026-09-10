package main

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/attest"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"github.com/urfave/cli/v2"
)

func diagnosticBakeOffContext(t *testing.T, args ...string) *cli.Context {
	t.Helper()
	set := flag.NewFlagSet("bake-off-test", flag.ContinueOnError)
	for _, cliFlag := range bakeOffFlags() {
		if err := cliFlag.Apply(set); err != nil {
			t.Fatal(err)
		}
	}
	if err := set.Parse(args); err != nil {
		t.Fatal(err)
	}
	ctx := cli.NewContext(nil, set, nil)
	ctx.Context = context.Background()
	return ctx
}

func diagnosticBakeOffReport(digest string) *pipeline.Report {
	return &pipeline.Report{
		RecipeID:     "kubernetes-test",
		RecipeDigest: "sha256:" + strings.Repeat("1", 64),
		Artifacts: []pipeline.ArtifactResult{{
			Component: "crictl", OutputType: "host-package",
			SHA256:      "sha256:" + strings.Repeat(digest, 64),
			Toolchain:   "registry.example.test/toolchain@sha256:" + strings.Repeat("e", 64),
			InputDigest: "sha256:" + strings.Repeat("f", 64),
			BuilderType: ir.BuilderOCIHermetic,
			EngineClass: ir.EngineOCIHermetic,
		}},
	}
}

func TestDiagnosticBuildEmitCannotCrossQualifiedParseBoundary(t *testing.T) {
	output := filepath.Join(t.TempDir(), "observed.json")
	ctx := diagnosticBakeOffContext(t,
		"--arch", "amd64", "--source-root", filepath.Join(t.TempDir(), "record", "src"),
		"--emit", output)
	calls := 0
	err := emitDiagnosticBuild(ctx, func(_ *cli.Context, sourceRoot string) (*pipeline.Report, error) {
		calls++
		if sourceRoot != ctx.String("source-root") {
			t.Fatalf("emit used source root %q, want %q", sourceRoot, ctx.String("source-root"))
		}
		return diagnosticBakeOffReport("a"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("emit ran %d builds, want 1", calls)
	}
	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseSBOMManifest(raw, true); err != nil {
		t.Fatalf("diagnostic SBOM parser rejected local observation: %v", err)
	}
	if _, err := parseSBOMManifest(raw, false); err == nil {
		t.Fatal("diagnostic observation crossed the publishable validation-vector parser")
	}
}

func TestDiagnosticBakeOffUsesTwoSiblingWorkRootsAndDetectsMismatch(t *testing.T) {
	base := t.TempDir()
	ctx := diagnosticBakeOffContext(t,
		"--arch", "amd64", "--source-root", filepath.Join(base, "bakeoff", "src"))
	var roots []string
	err := runBakeOffWithBuild(ctx, func(_ *cli.Context, sourceRoot string) (*pipeline.Report, error) {
		roots = append(roots, sourceRoot)
		return diagnosticBakeOffReport("b"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	wantRoots := []string{filepath.Join(base, "bakeoff", "a", "src"), filepath.Join(base, "bakeoff", "b", "src")}
	if strings.Join(roots, "\n") != strings.Join(wantRoots, "\n") {
		t.Fatalf("bake-off roots = %v, want %v", roots, wantRoots)
	}

	call := 0
	err = runBakeOffWithBuild(ctx, func(_ *cli.Context, _ string) (*pipeline.Report, error) {
		call++
		if call == 1 {
			return diagnosticBakeOffReport("c"), nil
		}
		return diagnosticBakeOffReport("d"), nil
	})
	if cliErrorCode(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("mismatched local builds did not fail verification: %v", err)
	}
}

func TestDiagnosticBakeOffComparisonRequiresExactComponentScope(t *testing.T) {
	ctx := diagnosticBakeOffContext(t, "--arch", "amd64")
	one, err := diagnosticManifestFromReport(ctx, diagnosticBakeOffReport("a"))
	if err != nil {
		t.Fatal(err)
	}
	twoReport := diagnosticBakeOffReport("a")
	second := twoReport.Artifacts[0]
	second.Component = "kubectl"
	second.SHA256 = "sha256:" + strings.Repeat("b", 64)
	twoReport.Artifacts = append(twoReport.Artifacts, second)
	two, err := diagnosticManifestFromReport(ctx, twoReport)
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]*attest.Manifest{{one, two}, {two, one}} {
		err := compareDiagnosticRecords(pair[0], pair[1])
		if cliErrorCode(err) != nkderr.CodeVerifyFailed || !strings.Contains(err.Error(), "kubectl") {
			t.Fatalf("different diagnostic scopes compared as identical: %v", err)
		}
	}
}
