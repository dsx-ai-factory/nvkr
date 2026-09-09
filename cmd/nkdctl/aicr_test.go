package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nhensley/nkdctl/pkg/nkd/catalog"
	"github.com/nhensley/nkdctl/pkg/nkd/ir"
	"github.com/urfave/cli/v2"
)

func TestAICRBundlingArgumentsComeFromDigestBoundActivation(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	generatorDigest := "sha256:" + strings.Repeat("b", 64)
	label := &catalog.AICRProfile{
		Identity: "gpu-runtime", Repo: "https://example.invalid/aicr",
		Service: "eks", Accelerator: "h100", OS: "ubuntu", Intent: "training",
		Platform: "dynamo", Digest: digest, GeneratorDigest: generatorDigest,
	}
	activation := &ir.AICRActivation{
		Service: "eks", Accelerator: "h100", OS: "ubuntu", Intent: "training",
		Platform: "dynamo", RecipeDigest: digest, GeneratorDigest: generatorDigest,
	}
	p, err := profileFromActivation(label, activation)
	if err != nil {
		t.Fatal(err)
	}
	if p.Service != activation.Service || p.Accelerator != activation.Accelerator ||
		p.OS != activation.OS || p.Intent != activation.Intent ||
		p.Platform != activation.Platform || p.Digest != activation.RecipeDigest ||
		p.GeneratorDigest != activation.GeneratorDigest {
		t.Fatalf("AICR output inputs did not come from the graph activation: %+v", p)
	}

	mutated := *label
	mutated.Accelerator = "h200"
	if _, err := profileFromActivation(&mutated, activation); err == nil {
		t.Fatal("catalog mutation after graph compilation must fail closed")
	}
	if _, err := profileFromActivation(label, nil); err == nil {
		t.Fatal("missing digest-bound activation must fail closed")
	}
	mutated = *label
	mutated.GeneratorDigest = "sha256:" + strings.Repeat("c", 64)
	if _, err := profileFromActivation(&mutated, activation); err == nil {
		t.Fatal("generator substitution after graph compilation must fail closed")
	}
}

func TestAICRCLIHasNoVerificationBypass(t *testing.T) {
	command := cmdAICR().Subcommands[0]
	for _, flag := range command.Flags {
		for _, name := range flag.Names() {
			if name == "no-verify" {
				t.Fatal("qualified AICR CLI exposes --no-verify")
			}
		}
	}
}

func TestAICRPreviewLabelsOutputUnqualified(t *testing.T) {
	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	t.Cleanup(func() { os.Stdout = oldStdout })

	app := &cli.App{Commands: []*cli.Command{cmdAICR()}}
	runErr := app.Run([]string{
		"nkdctl", "aicr", "bundle", "--print",
		"--data", filepath.Join("..", "..", "testdata", "examples", "operator-catalog"),
		"--category", "accelerated-runtime", "--arch", "amd64",
		"--accelerator", "h100", "--service", "eks", "--intent", "training",
		"--os", "ubuntu", "--k8s", "1.32.0",
	})
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = oldStdout
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if runErr != nil {
		t.Fatal(runErr)
	}
	message := string(output)
	if !strings.Contains(message, "UNQUALIFIED PREVIEW") ||
		!strings.Contains(message, "no authenticated publisher-vector binding") {
		t.Fatalf("AICR preview obscured its authority boundary: %q", message)
	}
}
