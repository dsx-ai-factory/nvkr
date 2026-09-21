package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/aicr"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/buildgraph"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/urfave/cli/v2"
)

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
		label.Platform != activation.Platform || label.Digest != activation.ProfileDigest ||
		label.GeneratorDigest != activation.GeneratorDigest {
		return aicr.Profile{}, e.New(e.CodePipelineDrift,
			"AICR catalog declaration changed after build identity was compiled")
	}
	p := aicr.Profile{
		Identity: label.Identity, Repo: label.Repo,
		Service: activation.Service, Accelerator: activation.Accelerator,
		OS: activation.OS, Intent: activation.Intent, Platform: activation.Platform,
		Digest: activation.ProfileDigest, GeneratorDigest: activation.GeneratorDigest,
	}
	if err := p.Validate(); err != nil {
		return aicr.Profile{}, err
	}
	return p, nil
}

// cmdAICR invokes AICR (github.com/NVIDIA/aicr) to render the accelerated-runtime
// bundle from an explicitly selected digest-pinned AICR profile (NKD-FR-RC-013a). It
// shells out to the pinned `aicr` binary through the same runner used by factory
// execution; nkdctl does not link the upstream AICR implementation.
func cmdAICR() *cli.Command {
	return &cli.Command{
		Name:   "aicr",
		Hidden: true, // retained for legacy compatibility
		Usage:  "render an accelerated-runtime bundle from an exact AICR profile",
		Subcommands: []*cli.Command{
			{
				Name:  "bundle",
				Usage: "render and locally verify an AICR deployment tree (external output remains unqualified)",
				Flags: append(selectionFlags(),
					&cli.StringFlag{Name: "out", Aliases: []string{"o"}, Value: ".nkd/work/aicr-bundle", Usage: "bundle output dir"},
					&cli.StringFlag{Name: "deployer", Usage: "helm | argocd | flux | helmfile (default: aicr's default, helm)"},
					&cli.BoolFlag{Name: "attest", Usage: "have aicr sign the bundle with build provenance"},
					&cli.BoolFlag{Name: "print", Usage: "print the aicr invocations without running them (plumbing preview)"},
					&cli.StringFlag{Name: "aicr", Value: aicr.DefaultBinary, Usage: "aicr binary name/path"},
				),
				Action: func(c *cli.Context) error {
					res, digest, err := resolveBuild(c)
					if err != nil {
						return err
					}
					if res.AICRProfile == nil {
						return e.New(e.CodeUnsupported,
							"selected build declares no AICR profile; select one with --aicr-profile")
					}
					g, err := buildgraph.Compile(res)
					if err != nil {
						return err
					}
					compiledDigest, err := g.Digest()
					if err != nil {
						return err
					}
					if compiledDigest != digest {
						return e.New(e.CodePipelineDrift, "resolved build identity changed before AICR bundling: got %s want %s", compiledDigest, digest)
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
