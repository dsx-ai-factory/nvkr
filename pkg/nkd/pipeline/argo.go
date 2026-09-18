package pipeline

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// Argo is the Argo Workflows renderer for the shared ADR-18 job model. It is not an
// admitted provider adapter: Render returns CodeEngineCapability before output until
// authenticated producer archives, verifier inputs, and runtime observation are
// represented by the provider contract.
type Argo struct {
	Data         string // Data = the operator's --data catalog path, threaded onto verify
	RunnerDigest string
	Verify       VerificationConfig
}

func (Argo) Name() string { return "argo" }

func (e Argo) Render(ctx context.Context, g *ir.Graph, buildDigest string) ([]byte, error) {
	if err := requireEmitterContext(ctx); err != nil {
		return nil, err
	}
	if err := validateGraphIdentity(g, buildDigest); err != nil {
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
	if !argoTransportContractImplemented() {
		return nil, nkderr.New(nkderr.CodeEngineCapability,
			"argo adapter does not yet map authenticated job inputs, outputs, workspaces, and producer result archives")
	}
	verifyArgv, err := verifyArgs(g, e.Data, e.Verify)
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
	fmt.Fprintf(bw, "apiVersion: argoproj.io/v1alpha1\nkind: Workflow\n")
	fmt.Fprintf(bw, "metadata: {generateName: nkd-build-%s-}\nspec:\n  entrypoint: main\n  templates:\n", strings.TrimPrefix(buildDigest, "sha256:")[:16])

	// DAG: each job is a task; verify depends on every producer's terminal state.
	fmt.Fprintf(bw, "    - name: main\n      dag:\n        tasks:\n")
	for _, j := range jobs {
		if err := requireEmitterContext(ctx); err != nil {
			return nil, err
		}
		fmt.Fprintf(bw, "          - name: %s\n            template: %s\n", j.Name, j.Name)
		if j.Kind == "verify" {
			var deps []string
			for _, p := range j.Needs {
				deps = append(deps, fmt.Sprintf("(%s.Succeeded || %s.Failed || %s.Errored)", p, p, p))
			}
			if len(deps) > 0 {
				fmt.Fprintf(bw, "            depends: %q\n", strings.Join(deps, " && "))
			}
		} else if len(j.Needs) > 0 {
			fmt.Fprintf(bw, "            depends: %q\n", strings.Join(j.Needs, " && "))
		}
	}

	// Container templates.
	for _, j := range jobs {
		if err := requireEmitterContext(ctx); err != nil {
			return nil, err
		}
		fmt.Fprintf(bw, "    - name: %s\n      container:\n", j.Name)
		switch j.Kind {
		case "bootstrap":
			fmt.Fprintf(bw, "        image: %s\n        command: [sh, -c]\n        args: [%q]\n", bootstrapImage, strings.Join(BootstrapScript(e.RunnerDigest), " && "))
		case "verify":
			verifyScript := append(RunnerGuardScript(e.RunnerDigest), shellJoin(append([]string{"./" + RunnerBinary}, verifyArgv...)))
			fmt.Fprintf(bw, "        image: %s\n        command: [sh, -c]\n        args: [%q]\n", j.Image, strings.Join(verifyScript, " && "))
		default: // build | pull
			img := j.Image
			if img == "" {
				img = bootstrapImage
			}
			fmt.Fprintf(bw, "        image: %s\n        command: [sh, -c]\n        args: [%q]\n", img, strings.Join(ExecScript(j, e.RunnerDigest), " && "))
		}
	}
	return GeneratedPipelineBytes(ctx, e.Name(), buildDigest, g, body.Bytes())
}

func argoTransportContractImplemented() bool { return false }
