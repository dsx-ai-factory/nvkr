package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	client "github.com/NVIDIA/nke-recipes/client/v1"
	"github.com/NVIDIA/nke-recipes/internal/ownedcmd"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/aicr"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/assemble"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/attest"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/criteria"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/profile"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/provenance"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/recipe"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/trust"
	"github.com/urfave/cli/v2"
	"golang.org/x/sys/unix"
)

// ── shared flags ─────────────────────────────────────────────────────────────

func queryFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "data", Usage: "explicit catalog directory (required for recipe resolution)"},
		&cli.StringFlag{Name: "category", Usage: "recipe category: os | kubernetes | core-runtime | accelerated-runtime | operations | platform-services (ADR-12)"},
		&cli.StringFlag{Name: "role", Usage: "control-plane | worker | gpu-worker (composition/SKU filter, not a recipe axis)"},
		&cli.StringFlag{Name: "k8s", Usage: "Kubernetes version (semver, e.g. 1.32.0)"},
		&cli.StringFlag{Name: "os", Usage: "target OS (operator-choosable; default ubuntu)"},
		&cli.StringFlag{Name: "arch", Value: "amd64", Usage: "target arch: amd64 | arm64 (native per-arch, model B)"},
		&cli.StringFlag{Name: "cluster-type", Usage: "operator | tenant (management vs workload cluster; layered catalogs)"},
		// reserved axes — any non-empty value fails closed (NKD-FR-RC-012).
		&cli.StringFlag{Name: "accelerator", Hidden: true},
		&cli.StringFlag{Name: "service", Hidden: true},
		&cli.StringFlag{Name: "intent", Hidden: true},
		&cli.StringFlag{Name: "platform", Hidden: true},
		&cli.StringFlag{Name: "tier", Hidden: true},
		&cli.StringFlag{Name: "compliance", Hidden: true},
	}
}

func providerFor(c *cli.Context) catalog.DataProvider {
	if d := c.String("data"); d != "" {
		return catalog.Dir(d)
	}
	return nil
}

// extensionSurface classifies the resolution's extension surface (ADR-11 §3) for the
// deterministic selection record: fork/declared-overlay/published-tuple by whether a
// supplied catalog composes layers.
func extensionSurface(c *cli.Context) string {
	layered := false
	if cat, err := catalog.LoadContext(c.Context, providerFor(c)); err == nil {
		layered = cat.Layers != nil
	}
	return recipe.ClassifyExtension(true, layered)
}

func queryFrom(c *cli.Context) (criteria.Criteria, map[string]string) {
	q := criteria.Criteria{
		Category:    c.String("category"),
		Role:        c.String("role"),
		K8sVersion:  c.String("k8s"),
		OS:          c.String("os"),
		Arch:        c.String("arch"),
		ClusterType: c.String("cluster-type"),
	}
	reserved := map[string]string{}
	for _, a := range criteria.ReservedAxes {
		if v := c.String(a); v != "" {
			reserved[a] = v
		}
	}
	return q, reserved
}

// resolveRecipe loads + validates the catalog and resolves the query to a single
// recipe with its recipeDigest.
func resolveRecipe(c *cli.Context) (*recipe.Resolved, string, error) {
	if c.String("data") == "" {
		return nil, "", e.New(e.CodeUsage, "recipe resolution requires --data <catalog-directory>")
	}
	// Delegate resolution to the client/v1 SDK facade (ADR-17 §1): the CLI is a thin
	// adapter over the embeddable orchestration surface, never a second implementation.
	q, reserved := queryFrom(c)
	resolution, err := client.New(client.WithProvider(providerFor(c))).ResolveRecipe(c.Context, client.ResolveRequest{
		Criteria: q,
		Reserved: reserved,
	})
	if err != nil {
		return nil, "", err
	}
	return resolution.Resolved, resolution.RecipeDigest, nil
}

// resolveGraph is the shared front half of every command: load + validate the
// catalog, resolve the query, compile the IR, and compute recipeDigest.
func resolveGraph(c *cli.Context) (*ir.Graph, string, error) {
	res, _, err := resolveRecipe(c)
	if err != nil {
		return nil, "", err
	}
	g, err := recipe.Compile(res)
	if err != nil {
		return nil, "", err
	}
	digest, err := g.Digest()
	if err != nil {
		return nil, "", err
	}
	return g, digest, nil
}

// ── recipe ───────────────────────────────────────────────────────────────────

func cmdRecipe() *cli.Command {
	return &cli.Command{
		Name:    "resolve",
		Aliases: []string{"recipe"},
		Usage:   "resolve criteria against the catalog and print the pinned IR",
		Flags: append(queryFlags(),
			&cli.StringFlag{Name: "output", Aliases: []string{"o"}, Value: "ir",
				Usage: "ir | canonical | nodes | digest | components"},
		),
		Action: func(c *cli.Context) error {
			g, digest, err := resolveGraph(c)
			if err != nil {
				return err
			}
			switch c.String("output") {
			case "digest":
				fmt.Println(digest)
			case "canonical":
				// ADR-01 §4: the CANONICAL form is what is hashed and stored on the wire
				// (file bytes == hashed bytes) — the RFC-8785 JCS content-identity view.
				// The default `ir` (YAML) output is the display-only --pretty view.
				view, err := g.DigestView()
				if err != nil {
					return err
				}
				b, err := canonical.Marshal(view)
				if err != nil {
					return err
				}
				os.Stdout.Write(b)
				fmt.Println()
			case "nodes":
				// Full canonical execution graph consumed by `diff --against` and
				// provider runners. Unlike `canonical`, this intentionally retains
				// transport locators and composition criteria and is not recipeDigest
				// identity material.
				b, err := g.CanonicalJSON()
				if err != nil {
					return err
				}
				os.Stdout.Write(b)
				fmt.Println()
			case "components":
				var comps []string
				for _, n := range g.Nodes {
					if n.Kind == ir.KindBuild {
						comps = append(comps, fmt.Sprintf("%-26s %-13s %s", n.Component, n.OutputType, n.Toolchain))
					}
				}
				sort.Strings(comps)
				fmt.Printf("recipe: %s\nrecipeDigest: %s\n\n", g.RecipeID, digest)
				for _, l := range comps {
					fmt.Println(l)
				}
			default:
				b, err := g.MarshalDeterministic()
				if err != nil {
					return err
				}
				os.Stdout.Write(b)
				fmt.Printf("\n# recipeDigest: %s\n", digest)
				fmt.Printf("# extensionSurface: %s\n", extensionSurface(c))
			}
			return nil
		},
	}
}

// ── bundle ───────────────────────────────────────────────────────────────────

func cmdBundle() *cli.Command {
	return cmdBundleWithProfileVerifier(nil)
}

// cmdBundleWithProfileVerifier keeps the production verifier fixed while allowing
// adversarial tests to exercise the same profile-authentication admission boundary.
func cmdBundleWithProfileVerifier(profileVerifier trust.DocumentVerifier) *cli.Command {
	return &cli.Command{
		Name:  "bundle",
		Usage: "emit the build pipeline for a CI engine (no build)",
		Flags: append(queryFlags(),
			&cli.StringFlag{Name: "engine", Value: "local", Usage: "local | gitlab (github, tekton, and argo are currently unavailable)"},
			&cli.StringFlag{Name: "out", Usage: "write the emitted pipeline here (default: stdout)"},
			&cli.BoolFlag{Name: "check", Usage: "fail closed if the committed --out pipeline has drifted from the IR (ADR-21)"},
			&cli.BoolFlag{Name: "json", Usage: "with --check, classify the drift (stale-pins vs hand-edit) as JSON"},
			&cli.BoolFlag{Name: "airgap", Usage: "relocate the IR to a mirror (no upstream URLs); recipeDigest is unchanged (ADR-15)"},
			&cli.StringFlag{Name: "mirror-ref", Value: "mirror.local", Usage: "mirror reference base for --airgap"},
			&cli.BoolFlag{Name: "emit-callable", Usage: "emit the pipeline as a reusable unit an operator's outer workflow can invoke (ADR-21 §4)"},
			&cli.StringFlag{Name: "extensions", Usage: "operator hook-point overlay (YAML: preBuild/postVerify lines) merged into the pipeline (ADR-21 §4)"},
			&cli.BoolFlag{Name: "meta-lint", Usage: "emit the meta-CI lint workflow that runs `bundle --check` against the committed pipeline (ADR-21 §2)"},
			&cli.StringFlag{Name: "cosign", Usage: "committed workspace-relative cosign verifier executable used by terminal verify"},
			&cli.StringFlag{Name: "cosign-digest", Usage: "exact lowercase sha256 digest of the terminal cosign verifier executable"},
			&cli.StringFlag{Name: "expected-digests", Usage: "committed canonical signed validation vector used by terminal verify"},
			&cli.StringFlag{Name: "signature", Usage: "detached validation-vector signature (optional when the bundle contains it)"},
			&cli.StringFlag{Name: "certificate", Usage: "validation-vector signing certificate"},
			&cli.StringFlag{Name: "bundle", Usage: "offline validation-vector Sigstore bundle"},
			&cli.StringFlag{Name: "trusted-root", Usage: "explicit offline validation-vector trusted root"},
			&cli.StringFlag{Name: "trusted-root-digest", Usage: "exact lowercase sha256 digest of the validation-vector trusted root"},
			&cli.StringFlag{Name: "identity", Usage: "pinned validation-vector signer identity"},
			&cli.StringFlag{Name: "issuer", Usage: "pinned validation-vector OIDC issuer"},
			&cli.StringFlag{Name: "trust-store", Usage: "optional role-separated signer trust store"},
			&cli.StringFlag{Name: "profile", Usage: "signed canonical builder profile containing the runner pin"},
			&cli.StringFlag{Name: "profile-signature", Usage: "detached builder-profile signature"},
			&cli.StringFlag{Name: "profile-certificate", Usage: "builder-profile signing certificate"},
			&cli.StringFlag{Name: "profile-bundle", Usage: "offline builder-profile Sigstore bundle"},
			&cli.StringFlag{Name: "profile-trusted-root", Usage: "explicit offline builder-profile trusted root"},
			&cli.StringFlag{Name: "profile-trusted-root-digest", Usage: "exact lowercase sha256 digest of the builder-profile trusted root"},
			&cli.StringFlag{Name: "profile-identity", Usage: "pinned builder-profile signer identity"},
			&cli.StringFlag{Name: "profile-issuer", Usage: "pinned builder-profile OIDC issuer"},
		),
		Action: func(c *cli.Context) error {
			eng := c.String("engine")
			switch eng {
			case "local", "gitlab":
			case "github", "tekton", "argo":
				return e.New(e.CodeEngineCapability,
					"bundle --engine %s is unavailable until that provider can transport and authenticate every declared input, output, verifier input, and execution receipt", eng)
			default:
				return e.New(e.CodeUnsupported,
					"unknown engine %q (local, github, gitlab, tekton, argo)", eng)
			}
			if c.Bool("meta-lint") {
				return e.New(e.CodeEngineCapability,
					"bundle --meta-lint is unavailable until a provider adapter can transport the exact nkdctl, catalog, profile, signer, and committed bundle inputs")
			}
			if c.String("extensions") != "" {
				return e.New(e.CodeEngineCapability,
					"bundle --extensions is unavailable until the selected provider has a typed authenticated extension adapter")
			}
			if c.Bool("airgap") {
				return e.New(e.CodeUnsupported,
					"bundle --airgap requires an opaque verified active mirror generation; raw CLI paths cannot construct that authority")
			}
			if c.Bool("check") && strings.TrimSpace(c.String("out")) == "" {
				return e.New(e.CodeUsage, "bundle --check requires the committed --out pipeline path")
			}
			if eng == "local" && (c.Bool("check") || c.Bool("emit-callable")) {
				return e.New(e.CodeUnsupported,
					"local bundle rendering has no generated provider file to check or emit as callable")
			}
			var (
				verify       pipeline.VerificationConfig
				runnerDigest string
			)
			if eng != "local" {
				if err := pipeline.ValidateEmitterDataPath(c.String("data")); err != nil {
					return err
				}
				verify = emittedVerificationConfig(c)
				if err := verify.ValidateStatic(); err != nil {
					return err
				}
				// The generated runner interprets nodes.json, acquires artifacts, and
				// executes terminal verification even for a profile-free prebuilt graph.
				// Authenticate its pin before catalog/graph I/O; authored profile bytes
				// can never select this authority.
				var err error
				runnerDigest, err = authenticateBundleRunnerDigest(c, profileVerifier)
				if err != nil {
					return err
				}
			}
			g, digest, err := resolveGraph(c)
			if err != nil {
				return err
			}
			if eng == "local" {
				// Dry-run the local runner to show the concrete execution plan.
				report, err := pipeline.Local{}.Run(g, digest, pipeline.RunOptions{
					Ctx: c.Context, DryRun: true, Stdout: os.Stdout, Stderr: os.Stderr,
				})
				if report != nil {
					defer func() { _ = report.Close() }()
				}
				return err
			}
			// Thread --data so the emitted terminal verify job resolves the SAME
			// operator catalog the pipeline was emitted for.
			data := c.String("data")
			// Inject only the runner digest derived from the authenticated profile
			// receipt above. The graph's prebuilt artifact identity remains profile-free.
			emitters := map[string]pipeline.Emitter{
				"github": pipeline.GitHub{Data: data, RunnerDigest: runnerDigest, Verify: verify},
				"gitlab": pipeline.GitLab{Data: data, RunnerDigest: runnerDigest, Verify: verify},
				"tekton": pipeline.Tekton{Data: data, RunnerDigest: runnerDigest, Verify: verify},
				"argo":   pipeline.Argo{Data: data, RunnerDigest: runnerDigest, Verify: verify},
			}
			em := emitters[eng]
			emitted, err := em.Render(c.Context, g, digest)
			if err != nil {
				return err
			}
			if c.Bool("emit-callable") {
				// ADR-21 §4: emit the generated pipeline as a REUSABLE unit an operator's
				// own outer workflow invokes and wraps with org logic, leaving the pristine
				// generated file untouched. For GitHub that is a workflow_call trigger; the
				// other engines are natively composable (gitlab include:, tekton/argo refs).
				if eng == "github" {
					emitted = bytes.Replace(emitted, []byte("on: [workflow_dispatch]\n"),
						[]byte("on:\n  workflow_dispatch: {}\n  workflow_call: {}\n"), 1)
				}
			}
			// The emitted pipeline runs `nkd-exec exec-node` over a committed nodes.json
			// (the pinned IR data, ADR-18 §2). Write it alongside the pipeline so the two
			// travel together; on stdout, note where it must live.
			nodes, err := pipeline.NodesJSON(g)
			if err != nil {
				return err
			}
			transportPlan, err := pipeline.TransportPlanJSON(g)
			if err != nil {
				return err
			}
			if c.Bool("check") {
				return bundleCheck(c.Context, c.String("out"), eng, g.RecipeID, digest, emitted, nodes, transportPlan, c.Bool("json"))
			}
			if out := c.String("out"); out != "" {
				manifest, err := pipeline.ManifestJSON(eng, g.RecipeID, digest, out, emitted, nodes, transportPlan)
				if err != nil {
					return err
				}
				if err := writeExecutionBundle(c.Context, out, emitted, nodes, transportPlan, manifest); err != nil {
					return err
				}
				fmt.Fprintf(os.Stderr, "wrote content-bound, transactionally committed execution bundle %s, %s, %s, %s (UNQUALIFIED until its runtime inputs and producer evidence are authenticated)\n", out, filepath.Join(filepath.Dir(out), pipeline.NodesFile), filepath.Join(filepath.Dir(out), pipeline.TransportPlanFile), bundleManifestPath(out))
				return nil
			}
			_, err = os.Stdout.Write(emitted)
			if err == nil {
				fmt.Fprintf(os.Stderr, "\n# commit %s alongside this pipeline (nkd-exec reads it):\n", pipeline.NodesFile)
				os.Stderr.Write(nodes)
			}
			return err
		},
	}
}

func emittedVerificationConfig(c *cli.Context) pipeline.VerificationConfig {
	return pipeline.VerificationConfig{
		Cosign: c.String("cosign"), CosignDigest: c.String("cosign-digest"),
		ExpectedDigests: c.String("expected-digests"), Signature: c.String("signature"),
		Certificate: c.String("certificate"), Bundle: c.String("bundle"), TrustedRoot: c.String("trusted-root"),
		TrustedRootDigest: c.String("trusted-root-digest"),
		Identity:          c.String("identity"), Issuer: c.String("issuer"), TrustStore: c.String("trust-store"),
		Profile: c.String("profile"), ProfileSignature: c.String("profile-signature"),
		ProfileCertificate: c.String("profile-certificate"), ProfileBundle: c.String("profile-bundle"),
		ProfileTrustedRoot: c.String("profile-trusted-root"), ProfileTrustedRootDigest: c.String("profile-trusted-root-digest"),
		ProfileIdentity: c.String("profile-identity"),
		ProfileIssuer:   c.String("profile-issuer"),
	}
}

// bundleCheck is the ADR-21 §2 fail-closed drift gate: it compares the freshly
// re-emitted pipeline (fresh) to the committed file at path and, on any divergence,
// fails closed with CodePipelineDrift, classifying the cause — stale-pins (the
// committed recipeDigest no longer matches the resolved one; remedy: catalog bump then
// bundle) vs hand-edit (recipeDigest matches but bytes differ; remedy: bundle to
// regenerate). Emission is deterministic, so an unchanged input reproduces byte-for-byte.
func bundleCheck(ctx context.Context, path, engine, recipeID, recipeDigest string, fresh, nodes, transportPlan []byte, asJSON bool) error {
	if err := executionBundleContextError(ctx, "checking execution-bundle drift"); err != nil {
		return err
	}
	if path == "" {
		return e.New(e.CodeUsage, "bundle --check requires --out <committed pipeline file>")
	}
	freshManifest, freshManifestErr := pipeline.ManifestJSON(engine, recipeID, recipeDigest, path, fresh, nodes, transportPlan)
	if freshManifestErr != nil {
		return freshManifestErr
	}
	committed, err := readExecutionBundleMember(ctx, path, int64(len(fresh)))
	if err != nil {
		return e.Wrap(e.CodePipelineDrift, err, "reading committed pipeline %s", path)
	}
	committedNodes, nodesErr := readExecutionBundleMember(ctx,
		filepath.Join(filepath.Dir(path), pipeline.NodesFile), int64(len(nodes)))
	committedTransport, transportErr := readExecutionBundleMember(ctx,
		filepath.Join(filepath.Dir(path), pipeline.TransportPlanFile), int64(len(transportPlan)))
	committedManifest, manifestErr := readExecutionBundleMember(ctx,
		bundleManifestPath(path), int64(len(freshManifest)))
	if bytes.Equal(committed, fresh) && nodesErr == nil && bytes.Equal(committedNodes, nodes) &&
		transportErr == nil && bytes.Equal(committedTransport, transportPlan) &&
		manifestErr == nil && bytes.Equal(committedManifest, freshManifest) &&
		pipeline.VerifyManifest(committedManifest, engine, recipeID, recipeDigest, path, committed, committedNodes, committedTransport) == nil {
		fmt.Fprintf(os.Stderr, "%s is up to date (recipeDigest %s)\n", path, recipeDigest)
		return nil
	}
	class := "hand-edit"
	remedy := "run: nkdctl bundle --out " + path
	if committedDigest := headerRecipeDigest(committed); committedDigest != "" && committedDigest != recipeDigest {
		class = "stale-pins"
		remedy = "run: nkdctl catalog bump, then nkdctl bundle --out " + path
	} else if nodesErr != nil || transportErr != nil || manifestErr != nil || !bytes.Equal(committedNodes, nodes) || !bytes.Equal(committedTransport, transportPlan) || !bytes.Equal(committedManifest, freshManifest) {
		class = "incomplete-bundle"
	}
	if asJSON {
		fmt.Printf("{\"drift\":true,\"file\":%q,\"class\":%q,\"committedRecipeDigest\":%q,\"resolvedRecipeDigest\":%q}\n",
			path, class, headerRecipeDigest(committed), recipeDigest)
	}
	return e.New(e.CodePipelineDrift,
		"%s has drifted from the IR (%s); %s", path, class, remedy)
}

func bundleManifestPath(pipelinePath string) string { return pipelinePath + ".bundle.json" }

const maxExecutionBundleMemberBytes int64 = 64 << 20

func readExecutionBundleMember(ctx context.Context, path string, maximum int64) ([]byte, error) {
	if err := executionBundleContextError(ctx, "reading execution-bundle member"); err != nil {
		return nil, err
	}
	if maximum < 0 || maximum > maxExecutionBundleMemberBytes {
		return nil, e.New(e.CodePipelineDrift,
			"execution-bundle member %s exceeds the supported size limit", path)
	}
	file, held, err := openExecutionBundleRegular(path, e.CodePipelineDrift)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	if held.Size() > maximum {
		return nil, e.New(e.CodePipelineDrift,
			"execution-bundle member %s is not a bounded regular file", path)
	}
	raw := make([]byte, 0, held.Size())
	buffer := make([]byte, 128<<10)
	for {
		if err := executionBundleContextError(ctx, "reading execution-bundle member"); err != nil {
			return nil, err
		}
		n, readErr := file.Read(buffer)
		if n > 0 {
			if int64(len(raw)+n) > maximum {
				return nil, e.New(e.CodePipelineDrift,
					"execution-bundle member %s grew beyond its expected size", path)
			}
			raw = append(raw, buffer[:n]...)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	afterHeld, heldErr := file.Stat()
	afterPath, pathErr := os.Lstat(path)
	if heldErr != nil || pathErr != nil || !os.SameFile(held, afterHeld) ||
		!os.SameFile(held, afterPath) || afterHeld.Size() != int64(len(raw)) ||
		afterPath.Size() != int64(len(raw)) || afterHeld.Mode() != held.Mode() ||
		!afterHeld.ModTime().Equal(held.ModTime()) {
		return nil, e.New(e.CodePipelineDrift,
			"execution-bundle member %s changed while reading", path)
	}
	return raw, nil
}

func openExecutionBundleRegular(path string, code e.Code) (*os.File, os.FileInfo, error) {
	fd, err := unix.Open(path,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, e.Wrap(code, err,
			"opening execution-bundle member %s without following links", path)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, nil, e.New(code,
			"holding execution-bundle member %s", path)
	}
	held, err := file.Stat()
	pathInfo, pathErr := os.Lstat(path)
	if err != nil || pathErr != nil || !held.Mode().IsRegular() ||
		!pathInfo.Mode().IsRegular() || !os.SameFile(held, pathInfo) {
		_ = file.Close()
		return nil, nil, e.New(code,
			"execution-bundle member %s is not a stable regular file", path)
	}
	return file, held, nil
}

// headerRecipeDigest extracts the recipeDigest from a generated pipeline's provenance
// header ("# recipeDigest: <d>"), or "" if absent.
func headerRecipeDigest(b []byte) string {
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "# recipeDigest:"); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// writeAtomic writes and syncs data in the target directory, checks cancellation
// immediately before the rename linearization point, then renames and syncs the
// parent. A failure before rename leaves no public partial file; once rename begins,
// durability completion is intentionally non-cancelable.
func writeAtomic(ctx context.Context, path string, data []byte, perm os.FileMode) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "atomic output requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "writing %s", path)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".nkd-*.tmp")
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "creating temp for %s", path)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		_ = tmp.Close()
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(perm); err != nil {
		return e.Wrap(e.CodeInternal, err, "chmod temp for %s", path)
	}
	for offset := 0; offset < len(data); {
		if err := ctx.Err(); err != nil {
			return e.Wrap(e.CodeTimeout, err, "writing %s", path)
		}
		end := min(offset+(128<<10), len(data))
		written, err := tmp.Write(data[offset:end])
		if err != nil {
			return e.Wrap(e.CodeInternal, err, "writing temp for %s", path)
		}
		if written == 0 {
			return e.Wrap(e.CodeInternal, io.ErrShortWrite, "writing temp for %s", path)
		}
		offset += written
	}
	if err := tmp.Sync(); err != nil {
		return e.Wrap(e.CodeInternal, err, "syncing temp for %s", path)
	}
	if err := tmp.Close(); err != nil {
		return e.Wrap(e.CodeInternal, err, "closing temp for %s", path)
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "publishing %s", path)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return e.Wrap(e.CodeInternal, err, "renaming temp onto %s", path)
	}
	cleanup = false
	directory, err := os.Open(dir)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "opening parent directory for %s", path)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return e.Wrap(e.CodeInternal, err, "syncing parent directory for %s", path)
	}
	return nil
}

// writeExecutionBundle stages every member in one directory, syncs each, then
// publishes the manifest last as the commit record. A crash can leave new member
// bytes with an old/missing manifest, but that state is deliberately unverifiable:
// bundle --check and the nodesDigest runner guard fail closed instead of executing a
// mixed generation.
func writeExecutionBundle(ctx context.Context, pipelinePath string, emitted, nodes, transportPlan, manifest []byte) error {
	if err := executionBundleContextError(ctx, "writing execution bundle"); err != nil {
		return err
	}
	dir := filepath.Dir(pipelinePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return e.Wrap(e.CodeInternal, err, "creating execution bundle directory")
	}
	type stagedFile struct {
		path string
		data []byte
		temp string
	}
	files := []stagedFile{
		{path: filepath.Join(dir, pipeline.NodesFile), data: nodes},
		{path: filepath.Join(dir, pipeline.TransportPlanFile), data: transportPlan},
		{path: pipelinePath, data: emitted},
		{path: bundleManifestPath(pipelinePath), data: manifest}, // commit record: always last
	}
	for _, file := range files {
		if int64(len(file.data)) > maxExecutionBundleMemberBytes {
			return e.New(e.CodeEngineCapability,
				"execution-bundle member %s exceeds the supported size limit", file.path)
		}
	}
	expected := make(map[string]string, len(files))
	for _, file := range files {
		if err := executionBundleContextError(ctx, "snapshotting execution bundle"); err != nil {
			return err
		}
		state, err := bundleFileState(ctx, file.path)
		if err != nil {
			return err
		}
		expected[file.path] = state
	}
	cleanup := func() {
		for _, file := range files {
			if file.temp != "" {
				_ = os.Remove(file.temp)
			}
		}
	}
	defer cleanup()
	for i := range files {
		if err := executionBundleContextError(ctx, "staging execution bundle"); err != nil {
			return err
		}
		tmp, err := os.CreateTemp(dir, ".nkd-bundle-*.tmp")
		if err != nil {
			return e.Wrap(e.CodeInternal, err, "staging execution bundle member %s", files[i].path)
		}
		files[i].temp = tmp.Name()
		if err := tmp.Chmod(0o644); err != nil {
			tmp.Close()
			return e.Wrap(e.CodeInternal, err, "setting execution bundle member mode")
		}
		for offset := 0; offset < len(files[i].data); {
			if err := executionBundleContextError(ctx, "staging execution bundle member"); err != nil {
				_ = tmp.Close()
				return err
			}
			end := min(offset+(128<<10), len(files[i].data))
			written, err := tmp.Write(files[i].data[offset:end])
			if err != nil {
				_ = tmp.Close()
				return e.Wrap(e.CodeInternal, err, "writing execution bundle member %s", files[i].path)
			}
			if written == 0 {
				_ = tmp.Close()
				return e.Wrap(e.CodeInternal, io.ErrShortWrite, "writing execution bundle member %s", files[i].path)
			}
			offset += written
		}
		if err := executionBundleContextError(ctx, "syncing execution bundle member"); err != nil {
			_ = tmp.Close()
			return err
		}
		if err := tmp.Sync(); err != nil {
			tmp.Close()
			return e.Wrap(e.CodeInternal, err, "syncing execution bundle member %s", files[i].path)
		}
		if err := tmp.Close(); err != nil {
			return e.Wrap(e.CodeInternal, err, "closing execution bundle member %s", files[i].path)
		}
	}
	leasePath := pipelinePath + ".lease"
	releaseLease, err := acquireExecutionBundleLock(ctx, leasePath)
	if err != nil {
		return err
	}
	defer releaseLease()
	if err := executionBundleContextError(ctx, "committing execution bundle"); err != nil {
		return err
	}
	for _, file := range files {
		if err := executionBundleContextError(ctx, "checking execution bundle commit state"); err != nil {
			return err
		}
		current, err := bundleFileState(ctx, file.path)
		if err != nil {
			return err
		}
		if current != expected[file.path] {
			return e.New(e.CodeWriteConflict, "execution bundle member %s changed while regeneration was staged", file.path)
		}
	}
	// Cancellation is honored until this final linearization check. Once the first
	// payload rename starts, complete the manifest-last commit and directory fsync
	// so voluntary cancellation cannot strand a partially published generation.
	if err := executionBundleContextError(ctx, "committing execution bundle"); err != nil {
		return err
	}
	for i := range files {
		if err := os.Rename(files[i].temp, files[i].path); err != nil {
			return e.Wrap(e.CodeInternal, err, "publishing execution bundle member %s", files[i].path)
		}
		files[i].temp = ""
	}
	directory, err := os.Open(dir)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "opening execution bundle directory for sync")
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return e.Wrap(e.CodeInternal, syncErr, "syncing execution bundle transaction")
	}
	if closeErr != nil {
		return e.Wrap(e.CodeInternal, closeErr, "closing execution bundle directory")
	}
	return nil
}

func bundleFileState(ctx context.Context, path string) (string, error) {
	if err := executionBundleContextError(ctx, "hashing execution-bundle member"); err != nil {
		return "", err
	}
	file, held, err := openExecutionBundleRegular(path, e.CodeWriteConflict)
	if errors.Is(err, os.ErrNotExist) {
		return "absent", nil
	}
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	if held.Size() > maxExecutionBundleMemberBytes {
		return "", e.New(e.CodeWriteConflict,
			"execution bundle member %s exceeds the supported size limit", path)
	}
	hash := sha256.New()
	buffer := make([]byte, 128<<10)
	var total int64
	for {
		if err := executionBundleContextError(ctx, "hashing execution-bundle member"); err != nil {
			return "", err
		}
		n, readErr := file.Read(buffer)
		if n > 0 {
			total += int64(n)
			if total > maxExecutionBundleMemberBytes {
				return "", e.New(e.CodeWriteConflict,
					"execution bundle member %s grew beyond the supported size limit", path)
			}
			_, _ = hash.Write(buffer[:n])
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", e.Wrap(e.CodeInternal, readErr,
				"hashing execution bundle member %s", path)
		}
	}
	afterHeld, heldErr := file.Stat()
	afterPath, pathErr := os.Lstat(path)
	if heldErr != nil || pathErr != nil || !os.SameFile(held, afterHeld) ||
		!os.SameFile(held, afterPath) || afterHeld.Size() != total ||
		afterPath.Size() != total || afterHeld.Mode() != held.Mode() ||
		!afterHeld.ModTime().Equal(held.ModTime()) {
		return "", e.New(e.CodeWriteConflict,
			"execution bundle member %s changed while hashing", path)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

// ── build ────────────────────────────────────────────────────────────────────

// buildFlags are the execution flags shared by build / attest / verify.
func buildFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringSliceFlag{Name: "only", Usage: "build only these components (repeatable)"},
		&cli.IntFlag{Name: "jobs", Aliases: []string{"j"}, Value: 1,
			Usage: "max concurrent local fetch/source/build work slots; default 1 (increase cautiously on one CPU-bound machine)"},
		&cli.StringFlag{Name: "source-root", Value: ".nkd/work/src", Usage: "where sources are checked out"},
		&cli.StringFlag{Name: "runtime", Aliases: []string{"docker"}, Value: "docker", Usage: "container runtime binary"},
		&cli.BoolFlag{Name: "keep-going", Usage: "continue past a failed build instead of failing closed"},
		&cli.BoolFlag{Name: "package", Usage: "assemble each artifact (oci-image/.deb) and record that digest, not the raw binary"},
		&cli.StringFlag{Name: "profile", Usage: "builder-profile YAML pinning host and execution identities (required for qualified from-source output)"},
		&cli.StringFlag{Name: "profile-signature", Usage: "detached signature for the canonical JSON builder profile"},
		&cli.StringFlag{Name: "profile-certificate", Usage: "Fulcio certificate for the builder profile"},
		&cli.StringFlag{Name: "profile-bundle", Usage: "Sigstore bundle for the builder profile"},
		&cli.StringFlag{Name: "profile-trusted-root", Usage: "explicit trusted-root JSON for offline builder-profile authentication"},
		&cli.StringFlag{Name: "profile-trusted-root-digest", Usage: "exact lowercase sha256 digest of --profile-trusted-root"},
		&cli.StringFlag{Name: "profile-identity", Usage: "authorized builder-profile certificate identity"},
		&cli.StringFlag{Name: "profile-issuer", Usage: "exact builder-profile OIDC issuer"},
		&cli.StringFlag{Name: "trust-store", Usage: "operator role-separated trust store JSON (default .nkd/trust.json)"},
		&cli.BoolFlag{Name: "profile-offline", Usage: "authenticate the builder profile offline using its bundle and exact trusted root"},
		&cli.BoolFlag{Name: "skip-preflight", Usage: "skip the fail-closed host preflight against the builder profile (advisory divergences still recorded)"},
	}
}

// resolveProfile returns the selected builder profile and its digest: the --profile
// YAML if given, else the embedded operator-controlled default for the target arch
// (NKD v1 §8.2.1 — byte reproduction is bounded to this profile).
func resolveProfile(c *cli.Context, arch string) (profile.BuilderProfile, string, error) {
	var (
		p   profile.BuilderProfile
		err error
	)
	if path := c.String("profile"); path != "" {
		if p, err = profile.LoadContext(c.Context, path); err != nil {
			return profile.BuilderProfile{}, "", err
		}
	} else {
		p = profile.Embedded(arch)
	}
	dg, err := p.Digest()
	if err != nil {
		return profile.BuilderProfile{}, "", err
	}
	return p, dg, nil
}

// preflightGate runs the fail-closed host preflight before a build (NKD v1 §8.2.1:
// "run profile preflight before resolving build inputs and stop when a required host
// property is absent or differs"). It returns all divergences for the evidence
// bundle; a hard-fail divergence is a coded error unless --skip-preflight is set.
func preflightGate(c *cli.Context, arch string) (profile.BuilderProfile, string, []profile.Divergence, error) {
	p, dg, err := resolveProfile(c, arch)
	if err != nil {
		return p, dg, nil, err
	}
	_, divs, perr := p.VerifyHost(c.Context, profile.ObserveHost())
	if perr != nil && !c.Bool("skip-preflight") {
		return p, dg, divs, perr
	}
	return p, dg, divs, nil
}

type qualifiedLocalBuild struct {
	Report             *pipeline.Report
	Resolved           *recipe.Resolved
	Profile            *profile.BuilderProfile
	ProfileDigest      string
	ProfileDivergences []profile.Divergence
}

type authenticatedProducerProfile struct {
	Profile     profile.BuilderProfile
	Digest      string
	HostReceipt profile.HostPreflightReceipt
	Divergences []profile.Divergence
}

func authenticateProducerProfile(c *cli.Context) (*authenticatedProducerProfile, error) {
	verifier, err := resolveQualifiedDocumentVerifier(c, nil)
	if err != nil {
		return nil, err
	}
	offline := c.Bool("profile-offline")
	material := trust.SignatureMaterial{
		SignaturePath: c.String("profile-signature"), CertificatePath: c.String("profile-certificate"),
		BundlePath: c.String("profile-bundle"), TrustedRootPath: c.String("profile-trusted-root"),
	}
	if err := validateQualifiedMaterial("builder profile", material, offline); err != nil {
		return nil, err
	}
	storeFile, err := loadTrustStoreFile(c.Context, trustStorePath(c))
	if err != nil {
		return nil, err
	}
	policy, err := qualifiedRolePolicy(c, storeFile, trust.RoleBuilderProfile)
	if err != nil {
		return nil, err
	}
	policy, err = qualifiedPolicyWithTrustedRoot(policy,
		c.String("profile-trusted-root"), c.String("profile-trusted-root-digest"), offline)
	if err != nil {
		return nil, err
	}
	authenticator, err := trust.NewDocumentAuthenticator(verifier,
		trust.JSONSchema[profile.BuilderProfile](profile.SchemaVersion))
	if err != nil {
		return nil, err
	}
	receipt, err := authenticator.Authenticate(c.Context, trust.AuthenticateRequest{
		SourcePath: c.String("profile"), Signature: material, Offline: offline,
		Role: trust.RoleBuilderProfile, Policy: policy,
	})
	if err != nil {
		return nil, err
	}
	profileValue, ok := receipt.Decoded().(profile.BuilderProfile)
	if !ok {
		return nil, e.New(e.CodeInternal,
			"authenticated builder-profile decoder returned %T", receipt.Decoded())
	}
	if err := profileValue.Validate(); err != nil {
		return nil, err
	}
	digest, err := profileValue.Digest()
	if err != nil {
		return nil, err
	}
	hostReceipt, divergences, err := profileValue.VerifyHost(c.Context, profile.ObserveHost())
	if err != nil {
		return nil, err
	}
	return &authenticatedProducerProfile{
		Profile: profileValue, Digest: digest, HostReceipt: hostReceipt, Divergences: divergences,
	}, nil
}

// runQualifiedLocalBuild is the only local path allowed to feed attest/release or a
// Reproduced claim. Host preflight yields an opaque stage-one receipt; Local's
// production observer measures stage two and the report must retain that exact
// profile/node-bound qualification. --skip-preflight is never valid here.
func runQualifiedLocalBuild(c *cli.Context, sourceRoot string) (*qualifiedLocalBuild, error) {
	if err := rejectQualifiedPackaging(c); err != nil {
		return nil, err
	}
	if c.Bool("skip-preflight") {
		return nil, e.New(e.CodePreflightFailed,
			"--skip-preflight is limited to unqualified development builds")
	}
	var authenticatedProfile *authenticatedProducerProfile
	if strings.TrimSpace(c.String("profile")) != "" {
		var err error
		authenticatedProfile, err = authenticateProducerProfile(c)
		if err != nil {
			return nil, err
		}
	}
	res, _, err := resolveRecipe(c)
	if err != nil {
		return nil, err
	}
	graph, err := recipe.Compile(res)
	if err != nil {
		return nil, err
	}
	digest, err := graph.Digest()
	if err != nil {
		return nil, err
	}
	result := &qualifiedLocalBuild{Resolved: res}
	only, err := skuComponents(c, res)
	if err != nil {
		return nil, err
	}
	options := pipeline.RunOptions{
		Ctx: c.Context, SourceRoot: sourceRoot, Only: only, Jobs: c.Int("jobs"),
		KeepGoing: c.Bool("keep-going"), Package: c.Bool("package"), Runtime: c.String("runtime"),
		Stdout: os.Stdout, Stderr: os.Stderr,
	}
	if recipeHasSource(res) {
		if authenticatedProfile == nil {
			return result, e.New(e.CodeUntrustedProfile,
				"qualified from-source output requires a separately authenticated builder profile with runner, runtime, toolchain, and sandbox execution identities")
		}
		builderProfile := authenticatedProfile.Profile
		if err := builderProfile.RequireBuilders(recipe.RequiredBuilders(res)); err != nil {
			return nil, err
		}
		result.Profile = &builderProfile
		result.ProfileDigest = authenticatedProfile.Digest
		result.ProfileDivergences = authenticatedProfile.Divergences
		options.Qualification = &pipeline.QualifiedExecution{
			Profile: builderProfile, HostReceipt: authenticatedProfile.HostReceipt,
			Observer: pipeline.LocalExecutionObserver{},
		}
	} else if authenticatedProfile != nil {
		return result, e.New(e.CodeUntrustedProfile,
			"an all-prebuilt qualified build must remain profile-independent")
	}
	result.Report, err = pipeline.Local{}.Run(graph, digest, options)
	if err != nil {
		if result.Report != nil {
			_ = result.Report.Close()
		}
		return result, err
	}
	if err := result.Report.RequireQualified(c.Context); err != nil {
		_ = result.Report.Close()
		return result, err
	}
	if result.Profile != nil && result.Report.QualificationProfileDigest() != result.ProfileDigest {
		_ = result.Report.Close()
		return result, e.New(e.CodePreflightFailed,
			"qualified report profile %s does not match selected profile %s",
			result.Report.QualificationProfileDigest(), result.ProfileDigest)
	}
	return result, nil
}

func rejectQualifiedPackaging(c *cli.Context) error {
	if c != nil && c.Bool("package") {
		return e.New(e.CodeEngineCapability,
			"qualified --package is unavailable until OCI/deb assembly streams from a held artifact capability into a crash-safe transaction")
	}
	return nil
}

// recipeHasSource reports whether any resolved component is built from source. It
// gates the builder-profile host preflight and manifest profile-binding: the no-compile
// (prebuilt) lane is host-independent — a content-addressed download reproduces the same
// bytes anywhere — so an all-prebuilt recipe is NOT bounded to a builder profile, and
// running the build-host preflight against it would be a spurious fail-closed.
func recipeHasSource(res *recipe.Resolved) bool {
	for _, nc := range res.Components {
		if nc.FromSource {
			return true
		}
	}
	return false
}

// runLocalBuild resolves the query, compiles the IR, runs the fail-closed host
// preflight against the selected builder profile (only when a from-source component is
// present), and runs the local engine.
// skuComponents is the build/delivery component set: an explicit --only wins; else
// --role selects the node-SKU subset via each component's `roles` affinity (ADR-12 §3,
// ADR-14 §4). It NEVER changes the recipe identity — the digest is over the full
// category recipe; role only bounds which components this build produces.
func skuComponents(c *cli.Context, res *recipe.Resolved) ([]string, error) {
	if only := c.StringSlice("only"); len(only) > 0 {
		return only, nil
	}
	role := c.String("role")
	if role == "" {
		return nil, nil
	}
	sku, err := recipe.FilterByRole(res, role)
	if err != nil {
		return nil, err
	}
	return sku.ComponentNames(), nil
}

func runLocalBuild(c *cli.Context) (*pipeline.Report, error) {
	return runLocalBuildAt(c, c.String("source-root"))
}

// runLocalBuildAt is the development runner behind build and diagnostic observations.
// Keeping the work root explicit also lets the separate bake-off command vary only
// that input while retaining the same query, profile preflight, and engine
// implementation. Its Report is deliberately unqualified and cannot feed
// attest/release authority.
func runLocalBuildAt(c *cli.Context, sourceRoot string) (*pipeline.Report, error) {
	res, _, err := resolveRecipe(c)
	if err != nil {
		return nil, err
	}
	g, err := recipe.Compile(res)
	if err != nil {
		return nil, err
	}
	digest, err := g.Digest()
	if err != nil {
		return nil, err
	}
	if recipeHasSource(res) {
		prof, _, _, err := preflightGate(c, res.Criteria.Arch)
		if err != nil {
			return nil, err
		}
		// ADR-07 §5 capability match: the profile must provide every builder the recipe
		// requires (e.g. a nix component needs a nix-providing profile), else fail closed.
		if err := prof.RequireBuilders(recipe.RequiredBuilders(res)); err != nil {
			return nil, err
		}
	}
	if !c.Bool("dry-run") {
		fmt.Fprintf(os.Stderr, "building with up to %d parallel job(s)\n", c.Int("jobs"))
	}
	only, err := skuComponents(c, res)
	if err != nil {
		return nil, err
	}
	return pipeline.Local{}.Run(g, digest, pipeline.RunOptions{
		Ctx:        c.Context,
		SourceRoot: sourceRoot,
		Only:       only,
		Jobs:       c.Int("jobs"),
		DryRun:     c.Bool("dry-run"),
		KeepGoing:  c.Bool("keep-going"),
		Package:    c.Bool("package"),
		Runtime:    c.String("runtime"),
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
	})
}

func cmdBuild() *cli.Command {
	return &cli.Command{
		Name:  "build",
		Usage: "run an unqualified local development build (cannot mint validation/release evidence)",
		Flags: append(append(append(append(queryFlags(), buildFlags()...),
			&cli.BoolFlag{Name: "dry-run", Usage: "print the docker invocations without executing"}),
			&cli.BoolFlag{Name: "bake-off", Usage: "run an UNQUALIFIED same-host byte comparison; use --emit/--compare for diagnostic records"},
			&cli.StringFlag{Name: "emit", Usage: "emit this single build's UNQUALIFIED observed-digests record here"},
			&cli.StringSliceFlag{Name: "compare", Usage: "diagnostically compare two observed-digests records"},
		), qualifiedDocumentVerifierFlags()...),
		Action: func(c *cli.Context) error {
			if c.Bool("bake-off") || len(c.StringSlice("compare")) > 0 {
				return runBakeOff(c) // legacy diagnostic compatibility entry point
			}
			if c.String("emit") != "" {
				return emitDiagnosticBuild(c, runLocalBuildAt)
			}
			rep, err := runLocalBuild(c)
			printReport(rep)
			if rep != nil {
				defer func() { _ = rep.Close() }()
			}
			return err
		},
	}
}

// cmdFetch is the no-compile lane's entry point (NKD-FR-BLD-050): it acquires the
// pinned, content-addressed prebuilt artifacts a recipe declares — downloading each and
// verifying it against the pinned digest (fail closed), never compiling. It runs only
// the prebuilt components (a from-source component is left to `nkdctl build`), so on a
// mixed recipe `fetch` + `build` cover the whole set. No builder-profile preflight
// applies: acquisition is host-independent.
func cmdFetch() *cli.Command {
	return &cli.Command{
		Name:   "fetch",
		Hidden: true, // retained for legacy compatibility
		Usage:  "acquire pinned prebuilt artifacts (no compile): download + verify against the pinned digest",
		Flags: append(queryFlags(),
			&cli.StringSliceFlag{Name: "only", Usage: "acquire only these components (repeatable)"},
			&cli.IntFlag{Name: "jobs", Aliases: []string{"j"}, Value: 4, Usage: "max concurrent acquisitions"},
			&cli.StringFlag{Name: "source-root", Value: ".nkd/work/src", Usage: "work root (artifacts land alongside, in ../fetch)"},
			&cli.StringFlag{Name: "runtime", Aliases: []string{"docker"}, Value: "docker", Usage: "container runtime for image pulls"},
			&cli.BoolFlag{Name: "package", Usage: "assemble acquired binaries into reproducible .deb packages and record that digest"},
			&cli.BoolFlag{Name: "keep-going", Usage: "continue past a failed acquisition instead of failing closed"},
			&cli.BoolFlag{Name: "dry-run", Usage: "print the acquisition plan without downloading"},
		),
		Action: func(c *cli.Context) error {
			res, _, err := resolveRecipe(c)
			if err != nil {
				return err
			}
			g, err := recipe.Compile(res)
			if err != nil {
				return err
			}
			digest, err := g.Digest()
			if err != nil {
				return err
			}
			// `fetch` is the no-compile lane: it acquires ONLY prebuilt components. An
			// explicit --only must name prebuilt components (else it would resolve a
			// from-source build node and compile it, bypassing the builder-profile
			// preflight); a bare invocation defaults to every prebuilt component, leaving
			// from-source ones to `build`.
			prebuilt := map[string]bool{}
			for _, nc := range res.Components {
				if nc.Prebuilt != nil {
					prebuilt[nc.Name] = true
				}
			}
			only := c.StringSlice("only")
			if len(only) == 0 {
				for name := range prebuilt {
					only = append(only, name)
				}
				sort.Strings(only)
			} else {
				for _, name := range only {
					if !prebuilt[name] {
						return e.New(e.CodeUnsupported,
							"fetch --only %q is not a prebuilt component — use `nkdctl build` for the from-source lane", name)
					}
				}
			}
			if len(only) == 0 {
				return e.New(e.CodeUnsupported,
					"recipe %q has no prebuilt components — use `nkdctl build` for the from-source lane", res.RecipeID)
			}
			rep, err := pipeline.Local{}.Run(g, digest, pipeline.RunOptions{
				Ctx:        c.Context,
				SourceRoot: c.String("source-root"),
				Only:       only,
				Jobs:       c.Int("jobs"),
				DryRun:     c.Bool("dry-run"),
				KeepGoing:  c.Bool("keep-going"),
				Package:    c.Bool("package"),
				Runtime:    c.String("runtime"),
				Stdout:     os.Stdout,
				Stderr:     os.Stderr,
			})
			printReport(rep)
			if rep != nil {
				defer func() { _ = rep.Close() }()
			}
			return err
		},
	}
}

// cmdDisk retains the hidden compatibility surface, but only its qualified held-qemu
// envelope verifier is operational. Script-backed production and boot observation
// fail before filesystem or subprocess I/O.
func cmdDisk() *cli.Command {
	return &cli.Command{
		Name:   "disk",
		Hidden: true, // retained for legacy compatibility
		Usage:  "verify a qcow2 envelope; script-backed disk production is disabled",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "format", Value: "qcow2", Usage: "disk format (qcow2 in v1)"},
			&cli.BoolFlag{Name: "boot", Usage: "reserved; fails closed until a qualified boot-observation adapter exists"},
			&cli.IntFlag{Name: "boot-timeout", Value: 900, Usage: "seconds to allow the emulated boot"},
			&cli.StringFlag{Name: "verify-envelope", Usage: "decode a derived envelope to its canonical RAW logical digest and assert the pre-encryption boundary (ADR-14 §1/§2/§3)"},
			&cli.StringFlag{Name: "qemu-img", Value: assemble.QemuImgDefault, Usage: "with --verify-envelope: qemu-img executable"},
			&cli.StringFlag{Name: "qemu-img-digest", Usage: "with --verify-envelope: exact sha256 digest of qemu-img executable"},
		},
		Action: runDisk,
	}
}

// runDisk preserves the qualified descriptor-held qemu envelope-verification path.
// Every producer/boot route fails before ambient scripts, filesystem writes, or
// subprocesses because those routes cannot currently return authority receipts.
func runDisk(c *cli.Context) error {
	if c.Bool("boot") {
		return e.New(e.CodeEngineCapability,
			"disk --boot is disabled until a qualified boot observer can return authenticated execution evidence")
	}
	format := c.String("format")
	if format != "qcow2" {
		if format == "raw" || format == "iso" {
			return e.New(e.CodeEngineCapability,
				"%s disk production is disabled until a qualified deterministic producer adapter is configured; --from-raw remains available for manifesting existing bytes", format)
		}
		return e.New(e.CodeUnsupported, "disk --format %q not supported (qcow2 verification only)", format)
	}
	// ADR-14 §1/§2/§3: verify a derived envelope by DECODING it to its RAW logical
	// stream through the pinned held-qemu seam, never by hashing container bytes.
	if env := c.String("verify-envelope"); env != "" {
		codec, err := assemble.OpenQemuImgCodec(c.Context, c.String("qemu-img"), c.String("qemu-img-digest"))
		if err != nil {
			return err
		}
		defer func() { _ = codec.Close() }()
		dg, n, err := codec.EnvelopeLogicalDigest(c.Context, format, env)
		if err != nil {
			return err
		}
		fmt.Printf("logical-disk digest (decoded %s RAW, %d bytes): %s\n", format, n, dg)
		return nil
	}
	return e.New(e.CodeEngineCapability,
		"qcow2 production is disabled until a qualified deterministic disk producer returns a receipt; use --verify-envelope to verify an existing envelope")
}

func cmdBakeOff() *cli.Command {
	return &cli.Command{
		Name:   "bake-off",
		Hidden: true, // retained for legacy compatibility
		Usage:  "run two same-host diagnostic builds under different work paths and compare bytes",
		Flags:  bakeOffFlags(),
		Action: runBakeOff,
	}
}

// bakeOffFlags are the bake-off-specific flags, shared by the hidden `bake-off` command
// and the legacy-compatible `build --bake-off` entry point.
func bakeOffFlags() []cli.Flag {
	return append(append(append(queryFlags(), buildFlags()...),
		&cli.StringFlag{Name: "emit", Usage: "emit THIS environment's UNQUALIFIED observed-digests record here"},
		&cli.StringSliceFlag{Name: "compare", Usage: "diagnostically compare two observed-digests records: --compare A.json --compare B.json"},
	), qualifiedDocumentVerifierFlags()...)
}

// runBakeOff is the bake-off body (ADR-08 §2), shared by `bake-off` and `build
// --bake-off`.
func runBakeOff(c *cli.Context) error {
	return runBakeOffWithBuild(c, runLocalBuildAt)
}

type diagnosticBuildFunc func(*cli.Context, string) (*pipeline.Report, error)

// runBakeOffWithBuild keeps the same-host comparison explicitly diagnostic. It
// exercises the real development runner but never asks that runner to mint an
// execution receipt, so a useful local byte comparison does not require production
// signer material. Qualified attest/verify paths remain separate and strict.
func runBakeOffWithBuild(c *cli.Context, build diagnosticBuildFunc) error {
	{
		// Compare half (ADR-08 §2): a pure, deterministic digest comparison of two
		// independently-produced records — the second environment may be a distinct
		// machine. Names the first divergent component on mismatch.
		if recs := c.StringSlice("compare"); len(recs) > 0 {
			if len(recs) != 2 {
				return e.New(e.CodeUsage, "bake-off --compare takes exactly two records")
			}
			return bakeoffCompare(c.Context, recs[0], recs[1])
		}
		if out := c.String("emit"); out != "" {
			return emitDiagnosticBuild(c, build)
		}
		sourceRoot := filepath.Clean(c.String("source-root"))
		base, leaf := filepath.Dir(sourceRoot), filepath.Base(sourceRoot)
		aRoot := filepath.Join(base, "a", leaf)
		bRoot := filepath.Join(base, "b", leaf)
		fmt.Fprintln(os.Stderr, "=== environment A ===")
		a, err := build(c, aRoot)
		if err != nil {
			if a != nil {
				printReport(a)
				_ = a.Close()
			}
			return err
		}
		if a == nil {
			return e.New(e.CodeInternal, "diagnostic environment A returned success without a report")
		}
		defer func() { _ = a.Close() }()
		ma, err := diagnosticManifestFromReport(c, a)
		if err != nil {
			return err
		}
		// Run B changes only the work path. Mutating process-global host environment
		// variables here would race other goroutines and still would not establish an
		// independent-machine reproduction observation.
		fmt.Fprintln(os.Stderr, "=== local run B (different build path) ===")
		b, err := build(c, bRoot)
		if err != nil {
			if b != nil {
				printReport(b)
				_ = b.Close()
			}
			return err
		}
		if b == nil {
			return e.New(e.CodeInternal, "diagnostic environment B returned success without a report")
		}
		defer func() { _ = b.Close() }()
		mb, err := diagnosticManifestFromReport(c, b)
		if err != nil {
			return err
		}
		if err := compareDiagnosticRecords(ma, mb); err != nil {
			fmt.Println("NOT bit-identical across environments:")
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "\nCOMPONENT\tDIGEST\tMATCH")
		for _, comp := range ma.Components {
			fmt.Fprintf(tw, "%s\t%s\tyes\n", comp.Name, comp.SHA256)
		}
		tw.Flush()
		fmt.Printf("\nUNQUALIFIED LOCAL DIAGNOSTIC: BIT-IDENTICAL across two same-host work paths (%d component(s)); recipeDigest %s\n", len(ma.Components), a.RecipeDigest)
		fmt.Println("(not a reproduction receipt; an authenticated two-independent-machine observation remains required)")
		return nil
	}
}

// emitDiagnosticBuild runs exactly one development build and serializes its local
// observation. It is intentionally independent of the bake-off protocol: callers
// may use the record for a diagnostic SBOM without implying a second environment or
// a reproduction comparison. Serialized diagnostics never carry execution authority.
func emitDiagnosticBuild(c *cli.Context, build diagnosticBuildFunc) error {
	out := c.String("emit")
	if out == "" {
		return e.New(e.CodeUsage, "diagnostic build emission requires --emit")
	}
	report, err := build(c, c.String("source-root"))
	if err != nil {
		if report != nil {
			printReport(report)
			_ = report.Close()
		}
		return err
	}
	if report == nil {
		return e.New(e.CodeInternal, "diagnostic build returned success without a report")
	}
	defer func() { _ = report.Close() }()
	printReport(report)
	manifest, err := diagnosticManifestFromReport(c, report)
	if err != nil {
		return err
	}
	b, err := manifest.Marshal()
	if err != nil {
		return err
	}
	if err := writeAtomic(c.Context, out, b, 0o644); err != nil {
		return err
	}
	fmt.Printf("UNQUALIFIED DIAGNOSTIC: emitted one local observed-digests record → %s (recipeDigest %s); it is not a validation vector or reproduction receipt\n", out, report.RecipeDigest)
	return nil
}

// compareDiagnosticRecords requires identical observation scope before comparing
// bytes. Manifest.VerifyAgainst intentionally permits a selected result subset for
// qualified verification, but that asymmetric rule would make a bake-off comparison
// order-dependent and could call an omitted component identical.
func compareDiagnosticRecords(a, b *attest.Manifest) error {
	if a == nil || b == nil {
		return e.New(e.CodeVerifyFailed, "diagnostic comparison requires two records")
	}
	if err := a.Validate(); err != nil {
		return err
	}
	if err := b.Validate(); err != nil {
		return err
	}
	shared := len(a.Components)
	if len(b.Components) < shared {
		shared = len(b.Components)
	}
	for index := 0; index < shared; index++ {
		if a.Components[index].Name != b.Components[index].Name {
			return e.New(e.CodeVerifyFailed,
				"diagnostic record scope differs at component %d: %q vs %q",
				index, a.Components[index].Name, b.Components[index].Name)
		}
	}
	if len(a.Components) > shared {
		return e.New(e.CodeVerifyFailed,
			"diagnostic record scope differs: second record is missing component %q",
			a.Components[shared].Name)
	}
	if len(b.Components) > shared {
		return e.New(e.CodeVerifyFailed,
			"diagnostic record scope differs: first record is missing component %q",
			b.Components[shared].Name)
	}
	return a.VerifyAgainst(b)
}

// diagnosticManifestFromReport projects development output into a deliberately
// non-publishable comparison record. From-source records retain the selected profile
// identity so comparisons cannot silently cross profile definitions, but no receipt
// or authority is attached and attest.Parse/qualified consumers reject these bytes.
func diagnosticManifestFromReport(c *cli.Context, report *pipeline.Report) (*attest.Manifest, error) {
	// FromReport is intentionally diagnostic here: the resulting bytes are parsed
	// only by ParseDiagnostic and cannot carry a qualified execution receipt.
	manifest := attest.FromReport(report)
	if manifest == nil {
		return nil, e.New(e.CodeVerifyFailed, "diagnostic build returned no report")
	}
	hasSource := false
	for _, component := range manifest.Components {
		if component.BuilderType != "" {
			hasSource = true
		}
	}
	if hasSource {
		selected, digest, err := resolveProfile(c, c.String("arch"))
		if err != nil {
			return nil, err
		}
		manifest.SetBuilderProfile(selected.Identity, digest, selected.HostControlClass)
	}
	if err := manifest.Validate(); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "development build produced an invalid diagnostic observation")
	}
	return manifest, nil
}

// bakeoffCompare is the deterministic compare half of the two-machine bake-off
// (ADR-08 §2): it loads two independently-emitted observed-digests records and asserts
// bit-identity, naming the first divergent component on mismatch. Parsed files no
// longer carry their same-process execution receipt, so this path is explicitly a
// diagnostic comparison and cannot itself authorize validation evidence.
func bakeoffCompare(ctx context.Context, pathA, pathB string) error {
	load := func(p string) (*attest.Manifest, error) {
		raw, err := readExecutionBundleMember(ctx, p, trust.MaxAuthenticatedDocumentBytes)
		if err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "reading record %s", p)
		}
		return attest.ParseDiagnostic(raw)
	}
	a, err := load(pathA)
	if err != nil {
		return err
	}
	b, err := load(pathB)
	if err != nil {
		return err
	}
	if err := compareDiagnosticRecords(a, b); err != nil {
		fmt.Printf("NOT bit-identical across environments (%s vs %s):\n", pathA, pathB)
		return err
	}
	fmt.Printf("UNQUALIFIED DIAGNOSTIC: record bytes are bit-identical across the two environments (%d component(s)); recipeDigest %s\n", len(a.Components), a.RecipeDigest)
	fmt.Println("(no execution receipt was authenticated from disk; this comparison is not validation evidence)")
	return nil
}

func cmdSBOM() *cli.Command {
	return &cli.Command{
		Name:   "sbom",
		Hidden: true, // retained as legacy compatibility for attest --sbom
		Usage:  "emit an UNQUALIFIED diagnostic CycloneDX SBOM from a caller-supplied manifest",
		Flags: append(queryFlags(),
			&cli.StringFlag{Name: "out", Aliases: []string{"o"}, Value: "sbom.cdx.json", Usage: "SBOM output path (or output dir with --per-artifact)"},
			&cli.BoolFlag{Name: "per-artifact", Usage: "emit one SBOM per built artifact + an aggregate index (ADR-13 §1)"},
			&cli.BoolFlag{Name: "diagnostic", Usage: "accept an UNQUALIFIED bake-off observed-digests record instead of a publishable validation vector"},
			&cli.StringFlag{Name: "expected-digests", Aliases: []string{"e"}, Required: true,
				Usage: "canonical vector, or with --diagnostic an observed-digests record supplying the selected exact artifact digest(s)"},
		),
		Action: func(c *cli.Context) error {
			res, digest, err := resolveRecipe(c)
			if err != nil {
				return err
			}
			raw, err := readExecutionBundleMember(c.Context, c.String("expected-digests"), trust.MaxAuthenticatedDocumentBytes)
			if err != nil {
				return e.Wrap(e.CodeVerifyFailed, err, "reading manifest")
			}
			m, err := parseSBOMManifest(raw, c.Bool("diagnostic"))
			if err != nil {
				return err
			}
			if m.RecipeDigest != digest {
				return e.New(e.CodeVerifyFailed,
					"expected-digests recipeDigest %s does not match the resolved recipe %s", m.RecipeDigest, digest)
			}
			hashes := make(map[string]string, len(m.Components))
			for _, comp := range m.Components {
				hashes[comp.Name] = comp.SHA256
			}
			var verified *sbom.VerifiedSet
			if c.Bool("diagnostic") {
				verified, err = buildDiagnosticSelectedSBOM(c.Context, res, digest, hashes)
			} else {
				verified, err = buildVerifiedSBOM(c.Context, res, digest, hashes)
			}
			if err != nil {
				return err
			}
			if c.Bool("per-artifact") {
				dir := c.String("out")
				if dir == "sbom.cdx.json" {
					dir = "sbom" // a directory default when emitting per-artifact
				}
				boms, idx, err := verified.PerArtifact()
				if err != nil {
					return err
				}
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return e.Wrap(e.CodeInternal, err, "creating sbom dir %s", dir)
				}
				for name, b := range boms {
					mb, err := b.Marshal()
					if err != nil {
						return err
					}
					if err := writeAtomic(c.Context, filepath.Join(dir, name+".cdx.json"), mb, 0o644); err != nil {
						return err
					}
				}
				ib, err := canonical.Marshal(idx)
				if err != nil {
					return err
				}
				if err := writeAtomic(c.Context, filepath.Join(dir, "index.json"), ib, 0o644); err != nil {
					return err
				}
				fmt.Printf("UNQUALIFIED DIAGNOSTIC: wrote %d per-artifact SBOMs + index to %s/; no producer receipt was authenticated\n", len(boms), dir)
				return nil
			}
			aggregate, err := verified.Aggregate()
			if err != nil {
				return err
			}
			b, err := aggregate.Marshal()
			if err != nil {
				return err
			}
			out := c.String("out")
			if err := writeAtomic(c.Context, out, b, 0o644); err != nil {
				return err
			}
			fmt.Printf("UNQUALIFIED DIAGNOSTIC: wrote %s — CycloneDX %s, %d selected component(s); no producer receipt was authenticated\n", out, "1.5", len(aggregate.Components))
			return nil
		},
	}
}

func parseSBOMManifest(raw []byte, diagnostic bool) (*attest.Manifest, error) {
	manifest, err := attest.Parse(raw)
	if err == nil || !diagnostic {
		return manifest, err
	}
	return attest.ParseDiagnostic(raw)
}

func cmdAttest() *cli.Command {
	return &cli.Command{
		Name:  "attest",
		Usage: "build, then emit the expected-digests manifest (the publisher's zero-binary artifact)",
		Flags: append(append(append(queryFlags(), buildFlags()...),
			&cli.StringFlag{Name: "out", Aliases: []string{"o"}, Value: "expected-digests.json",
				Usage: "manifest output path"},
			&cli.StringFlag{Name: "conformance", Usage: "reserved legacy field; qualified publisher vectors reject caller-authored conformance claims"},
			&cli.StringFlag{Name: "validation-vector", Usage: "reserved legacy field; qualified publisher vectors reject caller-authored validation references"},
			&cli.StringFlag{Name: "evidence-out", Usage: "also write a deterministic evidence bundle (verdict + preflight + per-component causes)"},
			&cli.BoolFlag{Name: "aicr-bundle", Usage: "reserved; publisher-attestation AICR bundling is unavailable in this release"},
			&cli.StringFlag{Name: "aicr-out", Value: ".nkd/work/aicr-bundle", Usage: "reserved output path for --aicr-bundle"},
			&cli.BoolFlag{Name: "vuln", Usage: "scan the deterministic SBOM with a shelled scanner and fold the report digest into the vector (ADR-13 §3)"},
			&cli.StringFlag{Name: "vuln-artifact", Usage: "with --vuln: exact component whose per-artifact SBOM is scanned (required for a multi-artifact recipe)"},
			&cli.StringFlag{Name: "vuln-scanner", Value: "grype", Usage: "with --vuln: qualified scanner kind (grype|trivy)"},
			&cli.StringFlag{Name: "vuln-scanner-digest", Usage: "with --vuln: exact observed scanner sha256 identity"},
			&cli.StringFlag{Name: "vuln-db", Usage: "with --vuln: exact observed scanner database sha256 identity"},
			&cli.StringFlag{Name: "vuln-out", Value: "vuln-report.json", Usage: "with --vuln: the deterministic vuln-report output path"},
			&cli.StringFlag{Name: "release-member", Usage: "also emit a release.Member for this build (aggregate with `release from-vectors` into a ReleaseSet; ADR-16)"},
			&cli.BoolFlag{Name: "sign", Usage: "sign the manifest detached+keyless via the pinned cosign capability"},
			&cli.BoolFlag{Name: "offline", Usage: "verification-only compatibility flag; qualified keyless signing rejects offline mode"},
			&cli.StringFlag{Name: "bundle", Usage: "with --sign: write a mirrorable bundle (sig+cert+Rekor inclusion proof) for airgap verify"},
		), qualifiedDocumentSignerFlags()...),
		Action: func(c *cli.Context) error {
			if c.String("conformance") != "" || c.String("validation-vector") != "" {
				return e.New(e.CodeUnsupported,
					"attest --conformance/--validation-vector cannot add caller-authored later-gate claims to a publisher vector; use a separately authenticated conformance receipt")
			}
			if c.Bool("aicr-bundle") {
				return e.New(e.CodeUnsupported,
					"attest --aicr-bundle cannot bind a rendered bundle with qualified publisher authority in this release; standalone AICR rendering is a locally verified diagnostic only")
			}
			if err := rejectQualifiedPackaging(c); err != nil {
				return err
			}
			if (c.Bool("sign") || c.String("bundle") != "") && c.Bool("offline") {
				return e.New(e.CodeUnsupported,
					"qualified keyless signing requires normal transparency-log access; --offline is verification-only")
			}
			if c.Bool("sign") || c.String("bundle") != "" {
				if err := admitQualifiedDocumentSigner(c); err != nil {
					return err
				}
			}
			if c.Bool("vuln") {
				return e.New(e.CodeUnsupported,
					"attest --vuln requires a network-denied observed-identity scanner adapter; the stock CLI has none")
			}
			if c.String("evidence-out") != "" {
				return e.New(e.CodeUnsupported,
					"attest --evidence-out cannot issue a reproduction verdict for the publisher's own build; operator observations belong to qualified verify")
			}
			if c.String("release-member") != "" {
				return e.New(e.CodeUnsupported,
					"attest --release-member requires typed reproduction and resolved-input receipts; a local vector cannot fabricate them")
			}
			qualified, err := runQualifiedLocalBuild(c, c.String("source-root"))
			if err != nil {
				if qualified != nil {
					printReport(qualified.Report)
				}
				return err
			}
			defer func() { _ = qualified.Report.Close() }()
			rep := qualified.Report
			res := qualified.Resolved
			m, err := attest.FromQualifiedReport(c.Context, rep)
			if err != nil {
				return err
			}
			g, err := recipe.Compile(res)
			if err != nil {
				return err
			}
			// Record the pinned AICR accelerated-runtime profile as provenance. Rendered
			// bundles remain standalone locally verified diagnostics in this release; the
			// publisher vector cannot acquire their authority through a caller-selected path.
			if res.AICRProfile != nil {
				p, err := profileFromActivation(res.AICRProfile, g.AICR)
				if err != nil {
					return err
				}
				ref := &attest.AICRRef{
					Identity: p.Identity, Digest: p.Digest, GeneratorDigest: p.GeneratorDigest, Repo: p.Repo,
				}
				m.AICR = ref
			}
			// Publisher evidence is assembled only from two opaque, locally verified
			// capabilities over this same qualified report: an exact-subject SBOM set and
			// report/graph-derived provenance. Caller-supplied evidence strings cannot
			// enter MarshalQualified.
			verifiedSBOM, err := buildVerifiedSBOMFromReport(c.Context, res, rep)
			if err != nil {
				return err
			}
			if gaps, err := verifiedSBOM.EvidenceGaps(); err != nil {
				return err
			} else if len(gaps) != 0 {
				fmt.Fprintf(os.Stderr, "SBOM contains %d explicit external-evidence gap(s); release authorization requires exact signed waivers\n", len(gaps))
			}
			qualifiedProvenance, err := provenance.BuildQualifiedFromReport(c.Context, rep, g)
			if err != nil {
				return err
			}
			publisherEvidence, err := attest.QualifyPublisherEvidence(c.Context, verifiedSBOM, qualifiedProvenance)
			if err != nil {
				return err
			}
			if err := m.AttachPublisherEvidence(publisherEvidence); err != nil {
				return err
			}
			b, err := m.MarshalQualified(c.Context)
			if err != nil {
				return err
			}
			out := c.String("out")
			bundle := c.String("bundle")
			if c.Bool("sign") || bundle != "" {
				if err := writeQualifiedSignedDocument(c, b, out, bundle); err != nil {
					return err
				}
			} else if err := writeAtomic(c.Context, out, b, 0o644); err != nil {
				return err
			}
			fmt.Printf("wrote %s — %s\n", out, m.Summary())
			if m.AICR != nil {
				fmt.Printf("AICR accelerated-runtime profile: %s @ %s (profile reference only; standalone `nkdctl aicr bundle` output remains unqualified across process boundaries)\n",
					m.AICR.Identity, m.AICR.Digest)
			}
			if c.Bool("sign") || bundle != "" {
				if bundle != "" {
					fmt.Printf("signed + bundled (mirrorable, airgap-verifiable): %s.sig + %s.pem + %s\n", out, out, bundle)
				} else {
					fmt.Printf("signed (detached, keyless): %s.sig + %s.pem\n", out, out)
				}
			} else {
				fmt.Println("(sign detached with --sign, add --bundle <path> for an airgap-verifiable bundle, or: cosign sign-blob --yes " + out + ")")
			}
			return nil
		},
	}
}

// verifyEvidenceDigests reconstructs the complete publisher-evidence aggregate
// from the terminal-qualified report and compares it to the authenticated vector
// receipt. Neither decoded manifests nor caller-provided digest strings can enter
// this authority boundary.
func verifyEvidenceDigests(ctx context.Context, vectorDocument trust.DocumentReceipt,
	res *recipe.Resolved, report *pipeline.Report,
) error {
	published, err := attest.VerifyPublishedVectorReceipt(ctx, vectorDocument)
	if err != nil {
		return err
	}
	g, err := recipe.Compile(res)
	if err != nil {
		return err
	}
	verified, err := buildVerifiedSBOMFromReport(ctx, res, report)
	if err != nil {
		return err
	}
	qualifiedProvenance, err := provenance.BuildQualifiedFromReport(ctx, report, g)
	if err != nil {
		return err
	}
	localEvidence, err := attest.QualifyPublisherEvidence(ctx, verified, qualifiedProvenance)
	if err != nil {
		return err
	}
	if err := localEvidence.VerifyAgainstPublishedVector(ctx, published); err != nil {
		return err
	}
	fmt.Println("qualified publisher evidence (SBOM, provenance, and exact artifact scope) matches the authenticated vector")
	return nil
}

// authenticateBundleRunnerDigest authenticates the exact canonical builder profile
// under RoleBuilderProfile and returns only its runner pin. This is generator-runner
// authority: it controls the executable that interprets the emitted graph and runs
// terminal verification, but it does not profile-bind a prebuilt artifact.
//
// Every caller-controlled capability is admitted before trusted-root, profile,
// catalog, or graph I/O. Offline bundle verification is mandatory because the same
// material must remain usable when the generated pipeline is checked in an air gap.
func authenticateBundleRunnerDigest(c *cli.Context, injected trust.DocumentVerifier) (string, error) {
	if c == nil || c.Context == nil {
		return "", e.New(e.CodeUsage,
			"bundle runner-profile authentication requires a command context")
	}
	ctx := c.Context
	if err := ctx.Err(); err != nil {
		return "", e.Wrap(e.CodeTimeout, err,
			"bundle runner-profile authentication canceled")
	}
	profilePath := strings.TrimSpace(c.String("profile"))
	if profilePath == "" {
		return "", e.New(e.CodeUntrustedProfile,
			"provider bundle emission requires an exact signed --profile containing the runner pin")
	}
	identity := strings.TrimSpace(c.String("profile-identity"))
	issuer := strings.TrimSpace(c.String("profile-issuer"))
	if identity == "" || issuer == "" {
		return "", e.New(e.CodeTrustPolicyMissing,
			"provider bundle emission requires explicit --profile-identity and --profile-issuer runner authority")
	}
	material := trust.SignatureMaterial{
		SignaturePath:   strings.TrimSpace(c.String("profile-signature")),
		CertificatePath: strings.TrimSpace(c.String("profile-certificate")),
		BundlePath:      strings.TrimSpace(c.String("profile-bundle")),
		TrustedRootPath: strings.TrimSpace(c.String("profile-trusted-root")),
	}
	if err := validateQualifiedMaterial("bundle runner profile", material, true); err != nil {
		return "", err
	}
	rootDigest := strings.TrimSpace(c.String("profile-trusted-root-digest"))
	if !exactSHA256Pin(rootDigest) {
		return "", e.New(e.CodeUnpinnedInput,
			"provider bundle emission requires an exact lowercase --profile-trusted-root-digest")
	}
	verifier, err := resolveQualifiedDocumentVerifier(c, injected)
	if err != nil {
		return "", err
	}
	policy, err := trust.NewRolePolicy(trust.RoleBuilderProfile, trust.Store{
		CLIIdentities: []string{identity},
	}, issuer)
	if err != nil {
		return "", err
	}
	policy, err = qualifiedPolicyWithTrustedRoot(policy, material.TrustedRootPath, rootDigest, true)
	if err != nil {
		return "", err
	}
	authenticator, err := trust.NewDocumentAuthenticator(verifier,
		trust.JSONSchema[profile.BuilderProfile](profile.SchemaVersion))
	if err != nil {
		return "", err
	}
	receipt, err := authenticator.Authenticate(ctx, trust.AuthenticateRequest{
		SourcePath: profilePath, Signature: material, Offline: true,
		Role: trust.RoleBuilderProfile, Policy: policy,
	})
	if err != nil {
		return "", err
	}
	authenticatedProfile, ok := receipt.Decoded().(profile.BuilderProfile)
	if !ok {
		return "", e.New(e.CodeInternal,
			"authenticated bundle runner-profile decoder returned %T", receipt.Decoded())
	}
	if err := authenticatedProfile.Validate(); err != nil {
		return "", err
	}
	for _, f := range authenticatedProfile.Fields {
		if f.Name == profile.FieldRunnerDigest {
			if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(f.Value) {
				return "", e.New(e.CodeUntrustedProfile, "builder profile runner digest %q is not canonical sha256", f.Value)
			}
			return f.Value, nil
		}
	}
	return "", e.New(e.CodeUntrustedProfile, "emitted pipeline requires builder profile field %s", profile.FieldRunnerDigest)
}

func printReport(rep *pipeline.Report) {
	if rep == nil {
		return
	}
	fmt.Printf("\n──────── nkdctl build report ────────\n")
	fmt.Printf("recipe:        %s\nrecipeDigest:  %s\n\n", rep.RecipeID, rep.RecipeDigest)
	if rep.Qualified() {
		fmt.Printf("authority:     qualified execution (%s)\n\n", orNone(rep.QualificationProfileDigest()))
	} else {
		fmt.Printf("authority:     UNQUALIFIED DEVELOPMENT OUTPUT — cannot attest, release, or claim reproduction\n\n")
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NODE\tKIND\tSTATUS\tDURATION")
	for _, n := range rep.Nodes {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", n.ID, n.Kind, n.Status, n.Duration.Round(1e6))
	}
	tw.Flush()
	if len(rep.Artifacts) > 0 {
		// Content digests of each artifact this run produced: for the from-source lane a
		// compiled binary (or, with --package, its assembled .deb/oci-image digest); for
		// the no-compile lane the acquired binary or the pulled image's pinned manifest
		// digest. These seed the expected-digests manifest.
		fmt.Printf("\nartifact digests (sha256 of each built or acquired artifact):\n")
		aw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(aw, "COMPONENT\tOUTPUT\tSHA256\tBYTES")
		for _, a := range rep.Artifacts {
			fmt.Fprintf(aw, "%s\t%s\t%s\t%d\n", a.Component, a.OutputType, a.SHA256, a.Size)
		}
		aw.Flush()
	}
}

// ── profile ──────────────────────────────────────────────────────────────────

// cmdProfile inspects the builder profile and runs the host preflight. The profile
// bounds nkdctl's byte-reproducibility claim to a declared, digest-identified set of
// host properties (NKD v1 §8.2.1/§10.1); its DIGEST is the authoritative identity.
func cmdProfile() *cli.Command {
	profFlags := []cli.Flag{
		&cli.StringFlag{Name: "arch", Value: "amd64", Usage: "target arch for the embedded default profile"},
		&cli.StringFlag{Name: "profile", Usage: "builder-profile YAML (default: embedded operator-controlled profile)"},
	}
	return &cli.Command{
		Name:  "profile",
		Usage: "inspect the builder profile and run the fail-closed host preflight",
		Subcommands: []*cli.Command{
			{
				Name:  "show",
				Usage: "print the builder profile and its authoritative digest",
				Flags: profFlags,
				Action: func(c *cli.Context) error {
					p, dg, err := resolveProfile(c, c.String("arch"))
					if err != nil {
						return err
					}
					b, err := p.Marshal()
					if err != nil {
						return err
					}
					os.Stdout.Write(b)
					fmt.Printf("\n# profileDigest: %s\n", dg)
					return nil
				},
			},
			{
				Name:  "digest",
				Usage: "print just the profile digest (the authoritative identity)",
				Flags: profFlags,
				Action: func(c *cli.Context) error {
					_, dg, err := resolveProfile(c, c.String("arch"))
					if err != nil {
						return err
					}
					fmt.Println(dg)
					return nil
				},
			},
			{
				Name:  "repin",
				Usage: "write a new runner/toolchain digest into the profile and re-sign (ADR-21 §5)",
				Flags: append(append(profFlags,
					&cli.StringFlag{Name: "runner", Usage: "new from-source nkd-exec digest (sha256:…)"},
					&cli.StringFlag{Name: "toolchain", Usage: "new pinned toolchain/runtime digest"},
					&cli.StringFlag{Name: "out", Usage: "write the updated profile here; signed repins require a distinct absent generation path"},
					&cli.BoolFlag{Name: "sign", Usage: "re-sign the profile after repinning (detached, keyless)"},
				), qualifiedDocumentSignerFlags()...),
				Action: func(c *cli.Context) error {
					if c.Bool("sign") {
						out := strings.TrimSpace(c.String("out"))
						if out == "" {
							return e.New(e.CodeUsage,
								"signed profile repin requires an explicit new --out path; immutable signed generations are never overwritten")
						}
						if source := strings.TrimSpace(c.String("profile")); source != "" {
							sourceAbs, sourceErr := filepath.Abs(filepath.Clean(source))
							outAbs, outErr := filepath.Abs(filepath.Clean(out))
							if sourceErr != nil || outErr != nil {
								return e.New(e.CodeUsage, "profile repin paths could not be resolved")
							}
							if sourceAbs == outAbs {
								return e.New(e.CodeImmutableViolation,
									"signed profile repin requires a new --out generation distinct from --profile")
							}
						}
						if err := admitQualifiedDocumentSigner(c); err != nil {
							return err
						}
					}
					p, _, err := resolveProfile(c, c.String("arch"))
					if err != nil {
						return err
					}
					if c.String("runner") == "" && c.String("toolchain") == "" {
						return e.New(e.CodeUsage, "profile repin: pass --runner and/or --toolchain")
					}
					if r := c.String("runner"); r != "" {
						p.Repin(profile.FieldRunnerDigest, r, true) // builder-identity: fail-closed
					}
					if tc := c.String("toolchain"); tc != "" {
						p.Repin(profile.FieldBootstrapToolchainDigest, tc, true)
					}
					dg, err := p.Digest()
					if err != nil {
						return err
					}
					b, err := p.Marshal()
					if err != nil {
						return err
					}
					out := c.String("out")
					if out == "" {
						out = c.String("profile")
					}
					if out == "" {
						return e.New(e.CodeUsage, "profile repin: pass --out or --profile to write the updated profile")
					}
					if c.Bool("sign") {
						if err := writeQualifiedSignedDocument(c, b, out, ""); err != nil {
							return err
						}
						fmt.Printf("re-signed profile: %s.sig + %s.pem\n", out, out)
					} else if err := writeAtomic(c.Context, out, b, 0o644); err != nil {
						return err
					}
					fmt.Printf("repinned profile → %s (new profileDigest %s)\n", out, dg)
					return nil
				},
			},
			{
				Name:  "preflight",
				Usage: "check this host against the profile; fail closed on a hard-fail divergence",
				Flags: append(profFlags, &cli.BoolFlag{Name: "skip-preflight", Usage: "report divergences but do not fail"}),
				Action: func(c *cli.Context) error {
					p, dg, err := resolveProfile(c, c.String("arch"))
					if err != nil {
						return err
					}
					_, divs, perr := p.VerifyHost(c.Context, profile.ObserveHost())
					fmt.Printf("profile %s (%s)\n", p.Identity, dg)
					if len(divs) == 0 {
						fmt.Println("host matches the profile on all fields.")
					}
					tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
					if len(divs) > 0 {
						fmt.Fprintln(tw, "FIELD\tWANT\tGOT\tHARD-FAIL")
						for _, d := range divs {
							fmt.Fprintf(tw, "%s\t%s\t%s\t%t\n", d.Field, d.Want, d.Got, d.HardFail)
						}
						tw.Flush()
					}
					if perr != nil && !c.Bool("skip-preflight") {
						return perr
					}
					return nil
				},
			},
		},
	}
}

// profileFromActivation uses the digest-bound IR activation as the sole authority
// for every output-affecting AICR generator argument. Catalog identity/repository
// remain display/transport metadata and are accepted only after the catalog tuple is
// shown to agree with the already-digested graph.
func profileFromActivation(label *catalog.AICRProfile, activation *ir.AICRActivation) (aicr.Profile, error) {
	if label == nil || activation == nil {
		return aicr.Profile{}, e.New(e.CodePipelineDrift, "AICR catalog declaration and digest-bound graph activation must both be present")
	}
	if label.Service != activation.Service || label.Accelerator != activation.Accelerator ||
		label.OS != activation.OS || label.Intent != activation.Intent ||
		label.Platform != activation.Platform || label.Digest != activation.RecipeDigest ||
		label.GeneratorDigest != activation.GeneratorDigest {
		return aicr.Profile{}, e.New(e.CodePipelineDrift,
			"AICR catalog declaration changed after recipe identity was compiled")
	}
	p := aicr.Profile{
		Identity: label.Identity, Repo: label.Repo,
		Service: activation.Service, Accelerator: activation.Accelerator,
		OS: activation.OS, Intent: activation.Intent, Platform: activation.Platform,
		Digest: activation.RecipeDigest, GeneratorDigest: activation.GeneratorDigest,
	}
	if err := p.Validate(); err != nil {
		return aicr.Profile{}, err
	}
	return p, nil
}

// ── aicr ─────────────────────────────────────────────────────────────────────

// cmdAICR leverages AICR (github.com/NVIDIA/aicr) to render the accelerated-runtime
// bundle for a recipe that declares a digest-pinned AICR profile (NKD-FR-RC-013a). It
// shells out to the `aicr` binary — nkdctl does not import AICR (keeping its linked
// deps minimal); an operator embedding nkdctl's SDK can instead inject an SDK-backed
// aicr.UnqualifiedBundler.
func cmdAICR() *cli.Command {
	return &cli.Command{
		Name:   "aicr",
		Hidden: true, // retained for legacy compatibility
		Usage:  "leverage AICR to render the accelerated-runtime bundle for an AICR-enabled recipe",
		Subcommands: []*cli.Command{
			{
				Name:  "bundle",
				Usage: "render and locally verify an AICR deployment tree (external output remains unqualified)",
				Flags: append(queryFlags(),
					&cli.StringFlag{Name: "out", Aliases: []string{"o"}, Value: ".nkd/work/aicr-bundle", Usage: "bundle output dir"},
					&cli.StringFlag{Name: "deployer", Usage: "helm | argocd | flux | helmfile (default: aicr's default, helm)"},
					&cli.BoolFlag{Name: "attest", Usage: "have aicr sign the bundle with build provenance"},
					&cli.BoolFlag{Name: "print", Usage: "print the aicr invocations without running them (plumbing preview)"},
					&cli.StringFlag{Name: "aicr", Value: aicr.DefaultBinary, Usage: "aicr binary name/path"},
				),
				Action: func(c *cli.Context) error {
					res, digest, err := resolveRecipe(c)
					if err != nil {
						return err
					}
					if res.AICRProfile == nil {
						return e.New(e.CodeUnsupported,
							"resolved recipe %q declares no aicrProfile — the AICR-governed axes are inactive for it (see testdata/examples/operator-catalog)", res.RecipeID)
					}
					g, err := recipe.Compile(res)
					if err != nil {
						return err
					}
					compiledDigest, err := g.Digest()
					if err != nil {
						return err
					}
					if compiledDigest != digest {
						return e.New(e.CodePipelineDrift, "resolved recipe identity changed before AICR bundling: got %s want %s", compiledDigest, digest)
					}
					p, err := profileFromActivation(res.AICRProfile, g.AICR)
					if err != nil {
						return err
					}
					opts := aicr.Options{OutputDir: c.String("out"), Deployer: c.String("deployer"), Attest: c.Bool("attest")}
					b := aicr.CLIBundler{Binary: c.String("aicr")}
					fmt.Printf("AICR profile %s (%s @ %s; generator %s)\n", p.Identity,
						fmt.Sprintf("service=%s accelerator=%s os=%s intent=%s", p.Service, p.Accelerator, p.OS, p.Intent), p.Digest, p.GeneratorDigest)
					if c.Bool("print") {
						for _, step := range aicr.Preview(c.String("aicr"), p, opts) {
							fmt.Printf("  %s\n", strings.Join(step, " "))
						}
						fmt.Println("UNQUALIFIED PREVIEW: no generator ran and no authenticated publisher-vector binding was created")
						return nil
					}
					qualifiedBundle, err := b.BundleQualified(c.Context, p, opts, os.Stdout, os.Stderr)
					if err != nil {
						return err
					}
					r, err := qualifiedBundle.ValidateFor(c.Context, p)
					if err != nil {
						return err
					}
					fmt.Printf("AICR recipe digest %s verified against the pin; rendered bundle → %s\n", r.RecipeDigest, r.OutputDir)
					fmt.Printf("  generator: %s\n  bundle tree: %s\n", r.GeneratorDigest, r.BundleDigest)
					if r.ChecksumsFile != "" {
						fmt.Printf("  checksums: %s\n", r.ChecksumsFile)
					}
					fmt.Println("UNQUALIFIED EXTERNAL OUTPUT: the same-process receipt cannot cross this boundary; this release has no authenticated publisher-vector path for the rendered bundle digest")
					return nil
				},
			},
		},
	}
}

// ── catalog ──────────────────────────────────────────────────────────────────

func cmdCatalog() *cli.Command {
	return &cli.Command{
		Name:  "catalog",
		Usage: "inspect and validate a catalog",
		Subcommands: []*cli.Command{
			{
				Name:  "digest",
				Usage: "print the exact resolved catalog snapshot digest used to pin an ordered component overlay",
				Flags: []cli.Flag{&cli.StringFlag{Name: "data", Required: true, Usage: "path to the catalog directory"}},
				Action: func(c *cli.Context) error {
					cat, err := catalog.LoadContext(c.Context, providerFor(c))
					if err != nil {
						return err
					}
					digest, err := cat.SnapshotDigest()
					if err != nil {
						return err
					}
					fmt.Println(digest)
					return nil
				},
			},
			{
				Name:  "validate",
				Usage: "load and fail-closed-validate a catalog",
				Flags: []cli.Flag{&cli.StringFlag{Name: "data", Required: true, Usage: "path to the catalog directory"}},
				Action: func(c *cli.Context) error {
					cat, err := catalog.LoadContext(c.Context, providerFor(c))
					if err != nil {
						return err
					}
					if cat.Layers != nil {
						fmt.Printf("ok: %s — layered catalog: %d category recipe pattern(s), 1 base, %d overlay(s), %d mixin(s), %d component(s)\n",
							cat.Metadata.Name, len(cat.Layers.Categories()), len(cat.Layers.Overlays),
							len(cat.Layers.Mixins), len(cat.Components))
					} else {
						fmt.Printf("ok: %s — %d flat recipe(s), %d component(s)\n",
							cat.Metadata.Name, len(cat.Recipes), len(cat.Components))
					}
					return nil
				},
			},
			{
				Name:  "recipes",
				Usage: "enumerate every concrete resolved recipe in an explicit resolution frame",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "data", Required: true, Usage: "path to the catalog directory"},
					&cli.StringFlag{Name: "k8s", Required: true, Usage: "concrete Kubernetes version bounding the enumeration frame"},
					&cli.StringFlag{Name: "os", Value: criteria.DefaultOS, Usage: "concrete target OS bounding the enumeration frame"},
					&cli.StringFlag{Name: "arch", Value: criteria.DefaultArch, Usage: "concrete target architecture bounding the enumeration frame"},
					&cli.StringFlag{Name: "category", Usage: "optionally enumerate only this category"},
					&cli.StringFlag{Name: "service", Usage: "AICR service for accelerated-runtime enumeration (for example eks)"},
					&cli.StringFlag{Name: "accelerator", Usage: "AICR accelerator for accelerated-runtime enumeration (for example h100)"},
					&cli.StringFlag{Name: "intent", Usage: "AICR intent for accelerated-runtime enumeration (training | inference)"},
					&cli.StringFlag{Name: "platform", Usage: "optional AICR workload platform"},
					&cli.BoolFlag{Name: "json", Usage: "emit the deterministic enumeration as canonical JSON"},
				},
				Action: func(c *cli.Context) error {
					cat, err := catalog.LoadContext(c.Context, providerFor(c))
					if err != nil {
						return err
					}
					resolved, err := recipe.Enumerate(cat, criteria.Criteria{
						Category: c.String("category"), K8sVersion: c.String("k8s"),
						OS: c.String("os"), Arch: c.String("arch"),
					}, map[string]string{
						"service": c.String("service"), "accelerator": c.String("accelerator"),
						"intent": c.String("intent"), "platform": c.String("platform"),
					})
					if err != nil {
						return err
					}
					if c.Bool("json") {
						raw, err := canonical.Marshal(resolved)
						if err != nil {
							return err
						}
						_, err = os.Stdout.Write(append(raw, '\n'))
						return err
					}
					fmt.Printf("resolvable recipes for k8s=%s os=%s arch=%s (role-independent):\n\n",
						c.String("k8s"), c.String("os"), c.String("arch"))
					tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
					fmt.Fprintln(tw, "RECIPE\tCATEGORY\tCONTENTS\tAICR PROFILE\tRECIPE DIGEST")
					for _, item := range resolved {
						contents := fmt.Sprintf("%d component(s)", len(item.Components))
						if item.AICR != nil && len(item.Components) == 0 {
							contents = "AICR activation closure"
						}
						fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", item.RecipeID,
							item.Criteria.Category, contents, item.AICRProfileIdentity, item.RecipeDigest)
					}
					return tw.Flush()
				},
			},
			{
				Name:  "list",
				Usage: "list recipes and components",
				Flags: []cli.Flag{&cli.StringFlag{Name: "data", Required: true, Usage: "path to the catalog directory"}},
				Action: func(c *cli.Context) error {
					cat, err := catalog.LoadContext(c.Context, providerFor(c))
					if err != nil {
						return err
					}
					fmt.Printf("catalog: %s\n\n", cat.Metadata.Name)
					if cat.Layers != nil {
						// Layered (base/overlays/mixins) catalog: show the composition graph.
						fmt.Printf("overlays (layered — recipes are composed):\n")
						ov := make([]string, 0, len(cat.Layers.Overlays))
						for n := range cat.Layers.Overlays {
							ov = append(ov, n)
						}
						sort.Strings(ov)
						ltw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
						fmt.Fprintf(ltw, "  base\t(root)\t%d componentRefs\n", len(cat.Layers.Base.Spec.ComponentRefs))
						for _, n := range ov {
							o := cat.Layers.Overlays[n]
							crit := ""
							if o.Spec.Criteria != nil {
								crit = fmt.Sprintf("category=%s k8s=%s os=%s arch=%s",
									orAny(o.Spec.Criteria.Category), orAny(o.Spec.Criteria.K8sVersion),
									orAny(o.Spec.Criteria.OS), orAny(o.Spec.Criteria.Arch))
							}
							if o.Spec.AICRProfile != nil {
								crit += " aicr=" + o.Spec.AICRProfile.Identity
							}
							fmt.Fprintf(ltw, "  %s\tbase=%s\t%d componentRefs\t%s\n", n, orNone(o.Spec.Base), len(o.Spec.ComponentRefs), crit)
						}
						ltw.Flush()
						if len(cat.Layers.Mixins) > 0 {
							mx := make([]string, 0, len(cat.Layers.Mixins))
							for n := range cat.Layers.Mixins {
								mx = append(mx, n)
							}
							sort.Strings(mx)
							fmt.Printf("\nmixins: %s\n", strings.Join(mx, ", "))
						}
					} else {
						fmt.Printf("recipes:\n")
						for _, r := range cat.Recipes {
							fmt.Printf("  %-26s role=%s k8s=%s os=%s arch=%s  (%d components)\n",
								r.ID, orAny(r.Criteria.Role), orAny(r.Criteria.K8sVersion),
								orAny(r.Criteria.OS), orAny(r.Criteria.Arch), len(r.Components))
						}
					}
					fmt.Printf("\ncomponents:\n")
					names := make([]string, 0, len(cat.Components))
					for n := range cat.Components {
						names = append(names, n)
					}
					sort.Strings(names)
					tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
					for _, n := range names {
						fmt.Fprintf(tw, "  %s\t%s\n", n, cat.Components[n].String())
					}
					tw.Flush()
					return nil
				},
			},
			{
				Name:      "pin",
				Usage:     "resolve a source's tag to a full pin (commit/tree/epoch) and write the lock",
				ArgsUsage: "<source>",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "tag", Usage: "tag to pin (default: newest matching the source constraint)"},
					&cli.StringFlag{Name: "dir", Required: true, Usage: "on-disk catalog directory to update"},
					&cli.DurationFlag{Name: "timeout", Value: 5 * time.Minute, Usage: "maximum duration for upstream git/container resolution"},
				},
				Action: func(c *cli.Context) error { return pinSources(c, false) },
			},
			{
				Name:      "bump",
				Usage:     "pin the newest tag matching each source's constraint (for scheduled CI)",
				ArgsUsage: "[source...]",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "all", Usage: "bump every source"},
					&cli.BoolFlag{Name: "check", Usage: "dry-run: print the lockfile diff, write nothing (ADR-22 §4)"},
					&cli.StringFlag{Name: "dir", Required: true, Usage: "on-disk catalog directory to update"},
					&cli.DurationFlag{Name: "timeout", Value: 5 * time.Minute, Usage: "maximum duration for upstream git/container resolution"},
				},
				Action: func(c *cli.Context) error { return pinSources(c, true) },
			},
			{
				Name:      "pin-artifacts",
				Usage:     "resolve prebuilt component digests (per arch) and write them to the lock's artifacts section",
				ArgsUsage: "[component...]",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "all", Usage: "pin every prebuilt component"},
					&cli.StringSliceFlag{Name: "arch", Value: cli.NewStringSlice("amd64", "arm64"), Usage: "arches to pin"},
					&cli.StringFlag{Name: "dir", Required: true, Usage: "on-disk catalog directory to update"},
					&cli.StringFlag{Name: "runtime", Aliases: []string{"docker"}, Value: "docker", Usage: "container runtime for image manifest inspection"},
					&cli.DurationFlag{Name: "timeout", Value: 5 * time.Minute, Usage: "maximum duration for artifact digest resolution"},
				},
				Action: pinArtifacts,
			},
			{
				Name:  "update-ci",
				Usage: "emit a scheduled GitHub Actions workflow that bumps pins, verifies, and opens a PR",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "dir", Required: true, Usage: "catalog directory the workflow updates"},
					&cli.StringFlag{Name: "cron", Value: "0 6 * * 1", Usage: "schedule (default: weekly Mon 06:00 UTC)"},
				},
				Action: func(c *cli.Context) error {
					return e.New(e.CodeEngineCapability,
						"catalog update-ci is unavailable until a provider adapter can regenerate, authenticate, reproduce, verify, and open a PR with every exact input transported")
				},
			},
		},
	}
}

// pinSources resolves and writes lock pins for one source (pin) or the newest
// constraint-matching tags across sources (bump).
func pinSources(c *cli.Context, bump bool) error {
	ctx, cancel := context.WithTimeout(c.Context, c.Duration("timeout"))
	defer cancel()
	dir := c.String("dir")
	sources, err := catalog.ReadSourcesContext(ctx, catalog.Dir(dir))
	if err != nil {
		return err
	}

	var targets []string
	switch {
	case !bump:
		if c.Args().Len() != 1 {
			return e.New(e.CodeUnsupported, "pin: expects exactly one <source>")
		}
		targets = []string{c.Args().First()}
	case c.Args().Len() > 0:
		targets = c.Args().Slice()
	case c.Bool("all"):
		targets = catalog.SortedSourceNames(sources)
	default:
		return e.New(e.CodeUnsupported, "bump: name a source or pass --all")
	}

	lock, err := catalog.LoadLockContext(ctx, dir)
	if err != nil {
		return err
	}
	changed := 0
	for _, name := range targets {
		s, ok := sources[name]
		if !ok {
			return e.New(e.CodeInvalidCatalog, "unknown source %q", name)
		}
		tag := c.String("tag")
		if bump || tag == "" {
			tags, err := catalog.ListTags(ctx, s.Repo)
			if err != nil {
				return err
			}
			if tag, err = catalog.SelectTagForSource(tags, s); err != nil {
				return err
			}
		}
		prev := lock.Pins[name]
		pin, err := catalog.ResolvePin(ctx, s, tag)
		if err != nil {
			return err
		}
		if prev == pin {
			fmt.Printf("  %s: already at %s (%s)\n", name, pin.Tag, pin.Commit[:12])
			continue
		}
		lock.Pins[name] = pin
		changed++
		fmt.Printf("  %s: %s -> %s (%s, epoch %d)\n", name, orNone(prev.Tag), pin.Tag, pin.Commit[:12], pin.SourceDateEpoch)
	}
	if changed == 0 {
		fmt.Println("no changes")
		return nil
	}
	if c.Bool("check") {
		// ADR-22 §4 dry-run: the diff was printed above; write nothing and signal
		// "differences present" with the non-error diff status so a gate can branch.
		fmt.Printf("(dry-run) %d pin(s) would change in %s/%s\n", changed, dir, catalog.LockFile)
		return e.New(e.CodeDiff, "%d pin(s) would change (dry-run: --check)", changed)
	}
	if err := catalog.WriteLockContext(ctx, dir, lock); err != nil {
		return err
	}
	fmt.Printf("wrote %d pin(s) to %s/%s\n", changed, dir, catalog.LockFile)
	return nil
}

// hex64 matches a bare 64-hex sha256 body (e.g. a dl.k8s.io .sha256 sidecar).
var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// pinArtifacts resolves the per-arch content digests of the catalog's prebuilt
// components and writes them to the lock's artifacts section — the machine-managed half
// of the no-compile lane, mirroring `catalog pin` for from-source. A binary URL is
// pinned from its published .sha256 sidecar when present (cheap, authoritative) else by
// downloading and hashing; an image ref is pinned from `docker manifest inspect`'s
// per-arch manifest digests.
func pinArtifacts(c *cli.Context) error {
	ctx, cancel := context.WithTimeout(c.Context, c.Duration("timeout"))
	defer cancel()
	dir := c.String("dir")
	targets, err := catalog.ReadPrebuiltTargetsContext(ctx, catalog.Dir(dir))
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return e.New(e.CodeUnsupported, "no prebuilt components in %s/%s", dir, catalog.RegistryFile)
	}
	want := map[string]bool{}
	for _, a := range c.Args().Slice() {
		want[a] = true
	}
	if !c.Bool("all") && len(want) == 0 {
		return e.New(e.CodeUnsupported, "pin-artifacts: name a component or pass --all")
	}
	arches := c.StringSlice("arch")
	lock, err := catalog.LoadLockContext(ctx, dir)
	if err != nil {
		return err
	}
	if lock.Artifacts == nil {
		lock.Artifacts = map[string]catalog.ArtifactPin{}
	}
	changed := 0
	for _, t := range targets {
		if !c.Bool("all") && !want[t.Name] {
			continue
		}
		digests := map[string]string{}
		if t.Image != "" {
			m, err := resolveImageDigests(ctx, c.String("runtime"), t.Image, arches)
			if err != nil {
				return err
			}
			digests = m
		} else {
			for _, a := range arches {
				d, err := resolveBinaryDigest(ctx, strings.ReplaceAll(t.URL, "{{.Arch}}", a))
				if err != nil {
					return err
				}
				digests[a] = d
			}
		}
		ap := catalog.ArtifactPin{Tag: t.Tag, Digests: digests}
		if artifactPinEqual(lock.Artifacts[t.Name], ap) {
			fmt.Printf("  %s: already pinned (%s, %d arch)\n", t.Name, t.Tag, len(digests))
			continue
		}
		lock.Artifacts[t.Name] = ap
		changed++
		fmt.Printf("  %s: pinned %d arch digest(s) @ %s\n", t.Name, len(digests), t.Tag)
	}
	if changed == 0 {
		fmt.Println("no changes")
		return nil
	}
	if err := catalog.WriteLockContext(ctx, dir, lock); err != nil {
		return err
	}
	fmt.Printf("wrote artifact pin(s) to %s/%s\n", dir, catalog.LockFile)
	return nil
}

func artifactPinEqual(a, b catalog.ArtifactPin) bool {
	if a.Tag != b.Tag || len(a.Digests) != len(b.Digests) {
		return false
	}
	for k, v := range b.Digests {
		if a.Digests[k] != v {
			return false
		}
	}
	return true
}

// resolveBinaryDigest resolves an artifact URL to its pinned sha256, preferring the
// published <url>.sha256 sidecar (authoritative, tiny) and falling back to downloading
// and hashing the artifact itself.
func resolveBinaryDigest(ctx context.Context, url string) (string, error) {
	if body, err := httpGetBody(ctx, url+".sha256", 64<<10); err == nil {
		if fields := strings.Fields(strings.TrimSpace(string(body))); len(fields) > 0 && hex64.MatchString(fields[0]) {
			return "sha256:" + fields[0], nil
		}
	} else if ctx.Err() != nil {
		return "", e.Wrap(e.CodeTimeout, ctx.Err(), "resolving binary digest canceled")
	}
	return httpSHA256(ctx, url, 2<<30)
}

// resolveImageDigests reads a multi-arch image's per-arch manifest digests via
// `docker manifest inspect`, returning arch -> sha256:<digest> for each requested arch
// (fail closed if an arch is absent from the manifest list).
func resolveImageDigests(ctx context.Context, docker, ref string, arches []string) (map[string]string, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "image digest resolution requires a context")
	}
	out, err := ownedcmd.Output(ctx, docker, "manifest", "inspect", ref)
	if err != nil {
		if ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "docker manifest inspection canceled")
		}
		return nil, e.Wrap(e.CodeFetchFailed, err, "docker manifest inspect %s", ref)
	}
	var idx struct {
		Manifests []struct {
			Digest   string `json:"digest"`
			Platform struct {
				Architecture string `json:"architecture"`
				OS           string `json:"os"`
			} `json:"platform"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(out, &idx); err != nil {
		return nil, e.Wrap(e.CodeFetchFailed, err, "parsing manifest list of %s", ref)
	}
	byArch := map[string]string{}
	for _, m := range idx.Manifests {
		if m.Platform.OS == "linux" {
			byArch[m.Platform.Architecture] = m.Digest
		}
	}
	res := map[string]string{}
	for _, a := range arches {
		d, ok := byArch[a]
		if !ok {
			return nil, e.New(e.CodeFetchFailed, "image %s has no linux/%s manifest", ref, a)
		}
		res[a] = d
	}
	return res, nil
}

// httpGetBody fetches a URL and returns its body, erroring on a non-200 status.
func httpGetBody(ctx context.Context, url string, maxBytes int64) ([]byte, error) {
	resp, err := catalogHTTPResponse(ctx, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.ContentLength > maxBytes {
		return nil, e.New(e.CodeFetchFailed, "GET %s exceeds %d bytes", url, maxBytes)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "GET %s canceled", url)
		}
		return nil, e.Wrap(e.CodeFetchFailed, err, "reading GET %s", url)
	}
	if int64(len(body)) > maxBytes {
		return nil, e.New(e.CodeFetchFailed, "GET %s exceeds %d bytes", url, maxBytes)
	}
	return body, nil
}

func httpSHA256(ctx context.Context, url string, maxBytes int64) (string, error) {
	resp, err := catalogHTTPResponse(ctx, url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.ContentLength > maxBytes {
		return "", e.New(e.CodeFetchFailed, "GET %s exceeds %d bytes", url, maxBytes)
	}
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return "", e.Wrap(e.CodeTimeout, ctx.Err(), "GET %s canceled", url)
		}
		return "", e.Wrap(e.CodeFetchFailed, err, "hashing GET %s", url)
	}
	if written > maxBytes {
		return "", e.New(e.CodeFetchFailed, "GET %s exceeds %d bytes", url, maxBytes)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func catalogHTTPResponse(ctx context.Context, url string) (*http.Response, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "catalog HTTP resolution requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "catalog HTTP resolution canceled")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, e.Wrap(e.CodeFetchFailed, err, "constructing GET %s", url)
	}
	response, err := newEvidenceHTTPClient().Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "GET %s canceled", url)
		}
		return nil, e.Wrap(e.CodeUnavailable, err, "GET %s", url)
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		return nil, e.New(e.CodeUnavailable, "GET %s returned HTTP %s", url, response.Status)
	}
	return response, nil
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func orAny(s string) string {
	if criteria.IsWildcard(s) {
		return "any"
	}
	return s
}
