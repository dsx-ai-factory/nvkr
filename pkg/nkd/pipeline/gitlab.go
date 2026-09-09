package pipeline

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/nhensley/nkdctl/pkg/nkd/ir"
)

// GitLab renders the currently admitted prebuilt provider lane in the ADR-18 §8
// shape: every job runs on a GENERIC runner (no bespoke `nkd-network-none` tag), and
// build nodes are isolated by the runner's own `podman run --network=none` (emitted via
// exec-node --launch), not by an engine-provided network-none container. Graphs
// containing source builds still fail CodeEngineCapability before output until
// provider-stage runtime attestation is implemented (ADR-18 §8).
type GitLab struct {
	Data         string // Data = the operator's --data catalog path, threaded onto verify
	RunnerDigest string
	Verify       VerificationConfig
	// RuntimeImage is the pinned base image carrying the container runtime (rootless
	// podman) that fetch/build jobs run on so exec-node --launch can orchestrate the
	// toolchain container. Empty defaults to the pinned podman base. (Only fetch/build
	// need it; pull uses the daemonless OCI transport and verify is offline.)
	RuntimeImage string
	// HelperImage selects the pinned-helper-image delivery (ADR-18 §8) BESIDE the
	// from-source bootstrap: when set, the digest-pinned image ships nkd-exec (on PATH) and
	// the runtime, so every job runs on it, the from-source `build-runner` bootstrap job is
	// omitted, and the helper is pinned by the image digest instead of a per-job re-hash.
	// Empty keeps the from-source bootstrap.
	HelperImage string
}

func (e GitLab) helper() HelperRef {
	if e.HelperImage != "" {
		return imageHelper()
	}
	return bootstrapHelper()
}

// jobImage returns the image a job runs on under the active helper delivery. Image mode
// runs every job on the one pinned helper image; bootstrap mode uses the runtime base for
// fetch/build and the node's own image otherwise.
func (e GitLab) jobImage(j Job) string {
	if e.HelperImage != "" {
		return e.HelperImage
	}
	if j.Kind == "fetch" || j.Kind == "build" {
		return e.runtimeImage()
	}
	return j.Image
}

// filterNeeds drops the from-source bootstrap dependency in image mode (there is no
// bootstrap job to depend on).
func (e GitLab) filterNeeds(needs []string) []string {
	if e.HelperImage == "" {
		return needs
	}
	return withoutBootstrap(needs)
}

// runtimeImage is the pinned rootless-podman base fetch/build jobs run on. It is sourced
// from the operator's DeliveryProfile (ADR-18 §8) via the caller-set RuntimeImage field, not a
// hardcoded placeholder digest: an empty value here means the caller supplied no runtime
// image, which is only reachable on the from-source lane (gated) — the enabled prebuilt
// lane has no fetch/build jobs, and pinned-helper-image mode uses HelperImage instead.
func (e GitLab) runtimeImage() string { return e.RuntimeImage }

func (GitLab) Name() string { return "gitlab-ci" }

func (e GitLab) Render(ctx context.Context, g *ir.Graph, recipeDigest string) ([]byte, error) {
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
	fmt.Fprintf(bw, "# the IR is the committed %s; each job runs `nkd-exec exec-node`.\n", NodesFile)
	if e.HelperImage != "" {
		fmt.Fprintf(bw, "# ADR-18 §8, pinned-helper-image delivery: every job runs on %s (nkd-exec + runtime),\n", e.HelperImage)
		fmt.Fprintf(bw, "# so there is no from-source bootstrap job; the helper is pinned by the image digest.\n")
	}
	fmt.Fprintf(bw, "stages: [bootstrap, fetch, build, verify]\n\n")

	for _, j := range jobs {
		if err := requireEmitterContext(ctx); err != nil {
			return nil, err
		}
		if j.Kind == "bootstrap" && e.HelperImage != "" {
			continue // pinned-helper-image mode: no from-source bootstrap job
		}
		fmt.Fprintf(bw, "%s:\n", j.Name)
		switch j.Kind {
		case "bootstrap":
			fmt.Fprintf(bw, "  stage: bootstrap\n  image: %s\n  script:\n", j.Image)
			for _, l := range BootstrapScript(e.RunnerDigest) {
				fmt.Fprintf(bw, "    - %s\n", l)
			}
			fmt.Fprintf(bw, "  artifacts: {paths: [%s]}\n", RunnerBinary)
		case "verify":
			// Generic runner; the verifier is measure-only and runs --offline, so no
			// network-none runner tag is needed (ADR-18 §8).
			fmt.Fprintf(bw, "  stage: verify\n  when: always\n") // unconditional (ADR-18 §7)
			fmt.Fprintf(bw, "  image: %s\n", e.jobImage(j))
			if needs := e.filterNeeds(j.Needs); len(needs) > 0 {
				fmt.Fprintf(bw, "  needs: [%s]\n", strings.Join(needs, ", "))
			}
			fmt.Fprintf(bw, "  script:\n")
			for _, l := range append(PrepareTransportScriptWith(j, e.RunnerDigest, helper), verifyCmd) {
				fmt.Fprintf(bw, "    - %s\n", l)
			}
		default: // fetch | build | pull — all on GENERIC runners; build self-isolates.
			stage := "build"
			if j.Kind == "fetch" {
				stage = "fetch"
			}
			fmt.Fprintf(bw, "  stage: %s\n", stage)
			if image := e.jobImage(j); image != "" {
				fmt.Fprintf(bw, "  image: %s\n", image)
			}
			fmt.Fprintf(bw, "  needs: [%s]\n", strings.Join(e.filterNeeds(j.Needs), ", "))
			fmt.Fprintf(bw, "  script:\n")
			for _, l := range ExecTransportScriptWith(j, e.RunnerDigest, helper) {
				fmt.Fprintf(bw, "    - %s\n", l)
			}
			if j.Produces != nil {
				fmt.Fprintf(bw, "  artifacts:\n    name: %s\n    paths:\n", j.Produces.Name)
				fmt.Fprintf(bw, "      - %s\n", TransportArchivePath(j.Produces.Name))
			}
		}
	}
	return GeneratedPipelineBytes(ctx, e.Name(), recipeDigest, g.RecipeID, g, body.Bytes())
}
