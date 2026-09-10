package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	client "github.com/nhensley/nkdctl/client/v1"
	"github.com/nhensley/nkdctl/pkg/nkd/attest"
	"github.com/nhensley/nkdctl/pkg/nkd/criteria"
	"github.com/nhensley/nkdctl/pkg/nkd/ir"
	"github.com/nhensley/nkdctl/pkg/nkd/profile"
	"github.com/nhensley/nkdctl/pkg/nkd/sbom"
	"github.com/urfave/cli/v2"
)

func TestDiagnosticSBOMConsumesSelectedBakeOffRecordOnlyWithExplicitFlag(t *testing.T) {
	ctx := context.Background()
	data := filepath.Join("..", "..", "testdata", "catalogs", "kubernetes-layered")
	resolution, err := client.New(client.WithData(data)).ResolveRecipe(ctx, client.ResolveRequest{Criteria: criteria.Criteria{
		Category: "kubernetes", Role: "worker", K8sVersion: "1.32.0",
		OS: "ubuntu", Arch: "amd64",
	}})
	if err != nil {
		t.Fatal(err)
	}
	resolved, recipeDigest := resolution.Resolved, resolution.RecipeDigest
	var outputType, toolchain string
	for _, component := range resolved.Components {
		if component.Name == "crictl" {
			outputType = component.OutputType
			toolchain = component.ToolchainImage
			if separator := strings.LastIndex(toolchain, "@"); separator >= 0 {
				toolchain = toolchain[separator+1:]
			}
			break
		}
	}
	if outputType == "" || toolchain == "" {
		t.Fatal("layered Kubernetes fixture no longer contains the crictl source component")
	}
	selectedProfile := profile.Embedded("amd64")
	profileDigest, err := selectedProfile.Digest()
	if err != nil {
		t.Fatal(err)
	}
	manifest := &attest.Manifest{
		SchemaVersion: attest.SchemaVersion, RecipeID: resolved.RecipeID, RecipeDigest: recipeDigest,
		ProfileDigest: profileDigest,
		BuilderProfile: &attest.BuilderProfileRef{
			ID: selectedProfile.Identity, Digest: profileDigest,
			HostControl: selectedProfile.HostControlClass,
		},
		Components: []attest.ComponentDigest{{
			Name: "crictl", OutputType: outputType, Toolchain: toolchain,
			InputDigest: "sha256:" + strings.Repeat("a", 64),
			BuilderType: ir.BuilderOCIHermetic, EngineClass: ir.EngineOCIHermetic,
			SHA256: "sha256:" + strings.Repeat("b", 64),
		}},
	}
	raw, err := manifest.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(t.TempDir(), "observed.json")
	if err := os.WriteFile(record, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	app := &cli.App{Commands: []*cli.Command{cmdSBOM()}}
	strictOut := filepath.Join(t.TempDir(), "strict.json")
	strictArgs := []string{"nkdctl", "sbom", "--category", "kubernetes", "--role", "worker",
		"--k8s", "1.32.0", "--arch", "amd64", "--data", data, "--expected-digests", record, "--out", strictOut}
	if err := app.RunContext(ctx, strictArgs); err == nil {
		t.Fatal("diagnostic observation was accepted without --diagnostic")
	}
	if _, err := os.Stat(strictOut); !os.IsNotExist(err) {
		t.Fatalf("strict rejection still published an SBOM: %v", err)
	}

	diagnosticOut := filepath.Join(t.TempDir(), "diagnostic.cdx.json")
	diagnosticArgs := append([]string(nil), strictArgs...)
	diagnosticArgs[len(diagnosticArgs)-1] = diagnosticOut
	diagnosticArgs = append(diagnosticArgs, "--diagnostic")
	if err := app.RunContext(ctx, diagnosticArgs); err != nil {
		t.Fatal(err)
	}
	bomBytes, err := os.ReadFile(diagnosticOut)
	if err != nil {
		t.Fatal(err)
	}
	var bom sbom.BOM
	if err := json.Unmarshal(bomBytes, &bom); err != nil {
		t.Fatal(err)
	}
	if len(bom.Components) != 1 || bom.Components[0].Name != "crictl" {
		t.Fatalf("diagnostic SBOM did not retain the exact selected scope: %+v", bom.Components)
	}

	perArtifactDir := filepath.Join(t.TempDir(), "sboms")
	perArtifactArgs := append([]string(nil), strictArgs...)
	perArtifactArgs[len(perArtifactArgs)-1] = perArtifactDir
	perArtifactArgs = append(perArtifactArgs, "--diagnostic", "--per-artifact")
	output, err := runCLIWithCapturedStdout(t, newApp(), perArtifactArgs...)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "UNQUALIFIED DIAGNOSTIC") {
		t.Fatalf("missing diagnostic label: %s", output)
	}
	entries, err := os.ReadDir(perArtifactDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name() != "crictl.cdx.json" || entries[1].Name() != "index.json" {
		t.Fatalf("unexpected per-artifact scope: %v", entries)
	}
	artifactBytes, err := os.ReadFile(filepath.Join(perArtifactDir, "crictl.cdx.json"))
	if err != nil {
		t.Fatal(err)
	}
	var artifactBOM sbom.BOM
	if err := json.Unmarshal(artifactBytes, &artifactBOM); err != nil {
		t.Fatal(err)
	}
	if len(artifactBOM.Components) != 0 || artifactBOM.Metadata.Component.Name != "crictl" {
		t.Fatalf("per-artifact BOM changed scope: %+v", artifactBOM)
	}
	indexBytes, err := os.ReadFile(filepath.Join(perArtifactDir, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var index sbom.Index
	if err := json.Unmarshal(indexBytes, &index); err != nil {
		t.Fatal(err)
	}
	if index.RecipeDigest != recipeDigest || len(index.Entries) != 1 {
		t.Fatalf("index lost recipe/scope: %+v", index)
	}
	artifactDigest, err := artifactBOM.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if entry := index.Entries[0]; entry.Artifact != "crictl" || entry.SBOMDigest != artifactDigest || entry.ArtifactSHA != manifest.Components[0].SHA256 {
		t.Fatalf("index does not bind emitted SBOM and artifact: %+v", entry)
	}
}
