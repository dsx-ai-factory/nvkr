package pipeline

import (
	"bytes"
	"context"
	"fmt"
	"io"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// Tekton is the Tekton renderer for the shared ADR-18 job model. It is not an admitted
// provider adapter: Render returns CodeEngineCapability before output until authenticated
// inputs, outputs, workspaces, dependency edges, and runtime observation are mapped.
type Tekton struct {
	Data         string // Data = the operator's --data catalog path, threaded onto verify
	RunnerDigest string
	Verify       VerificationConfig
}

func (Tekton) Name() string { return "tekton" }

// bootstrapImage is immutable and new enough to compile go.mod. The runner's own
// profile pin remains authoritative; this image digest prevents tag drift before
// that verification can execute.
const bootstrapImage = "golang@sha256:fe5d57d3b718e7a4986bae156c2d73f44973bfd313073aed08a4de6692bb6161"

func (e Tekton) Render(ctx context.Context, g *ir.Graph, recipeDigest string) ([]byte, error) {
	if err := requireEmitterContext(ctx); err != nil {
		return nil, err
	}
	if err := ValidateEmitterDataPath(e.Data); err != nil {
		return nil, err
	}
	if err := validateEmitterContract(g, e.RunnerDigest, e.Verify); err != nil {
		return nil, err
	}
	if err := requireEmitterContext(ctx); err != nil {
		return nil, err
	}
	if !tektonTransportContractImplemented() {
		return nil, nkderr.New(nkderr.CodeEngineCapability,
			"tekton adapter does not yet map authenticated job inputs, outputs, workspaces, and exact dependency edges")
	}
	verifyCmd, err := verifyCommand(g, e.Data, e.Verify)
	if err != nil {
		return nil, err
	}
	jobs, err := LowerJobs(g)
	if err != nil {
		return nil, err
	}
	if err := requireEmitterContext(ctx); err != nil {
		return nil, err
	}
	var body bytes.Buffer
	bw := io.Writer(&body)
	fmt.Fprintf(bw, "apiVersion: tekton.dev/v1\nkind: Pipeline\n")
	fmt.Fprintf(bw, "metadata: {name: nkd-build-%s}\n", g.RecipeID)
	fmt.Fprintf(bw, "spec:\n  workspaces: [{name: shared}]\n  tasks:\n")

	step := func(indent, name, image string, script []string) {
		fmt.Fprintf(bw, "%s- name: %s\n%s  image: %s\n%s  script: |\n", indent, name, indent, image, indent)
		for _, l := range script {
			fmt.Fprintf(bw, "%s      %s\n", indent, l)
		}
	}

	for _, j := range jobs {
		if err := requireEmitterContext(ctx); err != nil {
			return nil, err
		}
		if j.Kind == "verify" {
			continue // emitted in finally: below
		}
		fmt.Fprintf(bw, "    - name: %s\n", j.Name)
		if len(j.Needs) > 0 && j.Kind != "bootstrap" {
			fmt.Fprintf(bw, "      runAfter: [%s]\n", BootstrapJob)
		}
		fmt.Fprintf(bw, "      taskSpec:\n        workspaces: [{name: shared}]\n        steps:\n")
		if j.Kind == "bootstrap" {
			step("          ", "build-runner", bootstrapImage, BootstrapScript(e.RunnerDigest))
		} else {
			img := j.Image
			if img == "" {
				img = bootstrapImage
			}
			step("          ", "exec", img, ExecScript(j, e.RunnerDigest))
		}
	}

	// finally: the unconditional terminal verify (ADR-18 §7).
	fmt.Fprintf(bw, "  finally:\n    - name: verify\n      taskSpec:\n        steps:\n")
	step("          ", "verify", bootstrapImage, append(RunnerGuardScript(e.RunnerDigest), verifyCmd))
	return GeneratedPipelineBytes(ctx, e.Name(), recipeDigest, g.RecipeID, g, body.Bytes())
}

func tektonTransportContractImplemented() bool { return false }
