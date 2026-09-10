package pipeline

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// GitHub is the GitHub Actions renderer for the shared ADR-18 job model, in the ADR-18 §8
// Model-B shape: jobs run on generic runners and build nodes self-isolate via the helper's
// own `podman run --network=none` (GitHub-hosted runners ship podman), so no engine
// container block is used. The prebuilt lane is admitted; from-source graphs still fail
// closed in validateEmitterContract until provider runtime attestation exists (ADR-18 §8).
type GitHub struct {
	Actions      map[string]catalog.ActionPin
	Data         string      // the operator's --data catalog path, threaded onto verify
	Ext          *Extensions // operator hook points injected at pre-build / post-verify (ADR-21 §4)
	RunnerDigest string      // builder-profile pinned helper digest (ADR-07/18 §4)
	Verify       VerificationConfig
	// HelperImage selects the pinned-helper-image delivery (ADR-18 §8) beside the
	// from-source bootstrap: when set, jobs run inside this digest-pinned image (which ships
	// nkd-exec on PATH and the runtime), the from-source bootstrap job is omitted, and the
	// helper is pinned by the image digest. Empty keeps the from-source bootstrap on the
	// generic hosted runner (which ships podman).
	HelperImage string
}

func (e GitHub) helper() HelperRef {
	if e.HelperImage != "" {
		return imageHelper()
	}
	return bootstrapHelper()
}

// withoutBootstrap drops the from-source bootstrap dependency when there is no bootstrap
// job (pinned-helper-image mode).
func withoutBootstrap(needs []string) []string {
	out := make([]string, 0, len(needs))
	for _, n := range needs {
		if n != BootstrapJob {
			out = append(out, n)
		}
	}
	return out
}

func withoutBootstrapWhen(needs []string, active bool) []string {
	if !active {
		return needs
	}
	return withoutBootstrap(needs)
}

func (GitHub) Name() string { return "github-actions" }

func (e GitHub) Render(ctx context.Context, g *ir.Graph, recipeDigest string) ([]byte, error) {
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
	actions, err := ResolveGitHubActions(e.Actions)
	if err != nil {
		return nil, err
	}
	helper := e.helper()
	verifyCmd, err := verifyCommandWith(g, e.Data, e.Verify, helper)
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
	fmt.Fprintf(bw, "# the IR is the committed %s; each job runs `nkd-exec exec-node` — no build logic here.\n", NodesFile)
	fmt.Fprintf(bw, "# ADR-18 §8: jobs run on GENERIC runners (no engine container block); build nodes are\n")
	fmt.Fprintf(bw, "# isolated by the runner's own `%s run --network=none` (GitHub-hosted runners ship podman).\n", EmittedRuntime)
	if e.HelperImage != "" {
		fmt.Fprintf(bw, "# pinned-helper-image delivery: jobs run in container %s (nkd-exec on PATH); no bootstrap job.\n", e.HelperImage)
	}
	fmt.Fprintf(bw, "name: nkd-build-%s\n", g.RecipeID)
	fmt.Fprintf(bw, "on: [workflow_dispatch]\n")
	fmt.Fprintf(bw, "jobs:\n")

	for _, j := range jobs {
		if err := requireEmitterContext(ctx); err != nil {
			return nil, err
		}
		if j.Kind == "bootstrap" && e.HelperImage != "" {
			continue // pinned-helper-image mode: no from-source bootstrap job
		}
		fmt.Fprintf(bw, "  %s:\n", j.Name)
		fmt.Fprintf(bw, "    runs-on: ubuntu-24.04\n")
		if e.HelperImage != "" {
			fmt.Fprintf(bw, "    container: %s\n", e.HelperImage) // helper + runtime ship pinned in the image
		}
		if needs := withoutBootstrapWhen(j.Needs, e.HelperImage != ""); len(needs) > 0 {
			fmt.Fprintf(bw, "    needs: [%s]\n", strings.Join(needs, ", "))
		}
		if j.Kind == "verify" {
			fmt.Fprintf(bw, "    if: ${{ always() }}\n") // unconditional terminal verify (ADR-18 §7)
		}
		fmt.Fprintf(bw, "    steps:\n")
		fmt.Fprintf(bw, "      - uses: %s\n", actions.Checkout)
		switch j.Kind {
		case "bootstrap":
			fmt.Fprintf(bw, "      - run: |\n")
			for _, l := range BootstrapScript(e.RunnerDigest) {
				fmt.Fprintf(bw, "          %s\n", l)
			}
			actions.emitGitHubUpload(bw, *j.Produces)
		case "verify":
			for _, name := range j.Consumes {
				if e.HelperImage != "" && name == RunnerBinary {
					continue // the helper is in the image, not a transported artifact
				}
				actions.emitGitHubDownload(bw, name)
			}
			fmt.Fprintf(bw, "      - run: |\n")
			for _, l := range append(PrepareTransportScriptWith(j, e.RunnerDigest, helper), verifyCmd) {
				fmt.Fprintf(bw, "          %s\n", l)
			}
			if e.Ext != nil && len(e.Ext.PostVerify) > 0 {
				fmt.Fprintf(bw, "      - name: post-verify (operator extension)\n        run: |\n")
				for _, l := range e.Ext.PostVerify {
					fmt.Fprintf(bw, "          %s\n", l)
				}
			}
		default: // build | pull
			for _, name := range j.Consumes {
				if e.HelperImage != "" && name == RunnerBinary {
					continue // the helper is in the image, not a transported artifact
				}
				actions.emitGitHubDownload(bw, name)
			}
			if e.Ext != nil && len(e.Ext.PreBuild) > 0 {
				fmt.Fprintf(bw, "      - name: pre-build (operator extension)\n        run: |\n")
				for _, l := range e.Ext.PreBuild {
					fmt.Fprintf(bw, "          %s\n", l)
				}
			}
			fmt.Fprintf(bw, "      - run: |\n")
			for _, l := range ExecTransportScriptWith(j, e.RunnerDigest, helper) {
				fmt.Fprintf(bw, "          %s\n", l)
			}
			if j.Produces != nil {
				actions.emitGitHubUpload(bw, *j.Produces)
			}
		}
	}
	return GeneratedPipelineBytes(ctx, e.Name(), recipeDigest, g.RecipeID, g, body.Bytes())
}

func (actions GitHubActionRefs) emitGitHubDownload(w io.Writer, name string) {
	destination := "."
	if name != RunnerBinary {
		destination = filepath.ToSlash(filepath.Dir(TransportArchivePath(name)))
	}
	fmt.Fprintf(w, "      - uses: %s\n        with:\n          name: %s\n          path: %s\n", actions.Download, name, destination)
}

func (actions GitHubActionRefs) emitGitHubUpload(w io.Writer, artifact JobArtifact) {
	path := artifact.Paths[0]
	if artifact.Name != RunnerBinary {
		path = TransportArchivePath(artifact.Name)
	}
	fmt.Fprintf(w, "      - uses: %s\n        with:\n          name: %s\n          path: %s\n          if-no-files-found: error\n", actions.Upload, artifact.Name, path)
}
