package main

import (
	"fmt"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/buildgraph"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/target"
	"github.com/urfave/cli/v2"
)

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

// buildHasSource reports whether any resolved component is built from source. It
// gates the builder-profile host preflight and manifest profile-binding: the no-compile
// (prebuilt) lane is host-independent — a content-addressed download reproduces the same
// bytes anywhere — so an all-prebuilt graph is NOT bounded to a builder profile, and
// running the build-host preflight against it would be a spurious fail-closed.
func buildHasSource(res *buildgraph.Resolved) bool {
	for _, nc := range res.Components {
		if nc.FromSource {
			return true
		}
	}
	return false
}

// selectedComponents optionally narrows an unqualified diagnostic execution.
func selectedComponents(c *cli.Context) []string {
	return c.StringSlice("only")
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
	res, _, err := resolveBuild(c)
	if err != nil {
		return nil, err
	}
	g, err := buildgraph.Compile(res)
	if err != nil {
		return nil, err
	}
	digest, err := g.Digest()
	if err != nil {
		return nil, err
	}
	if buildHasSource(res) {
		prof, _, _, err := preflightGate(c, target.Arch(res.Platform))
		if err != nil {
			return nil, err
		}
		// ADR-07 §5 capability match: the profile must provide every builder the selected graph
		// requires (e.g. a nix component needs a nix-providing profile), else fail closed.
		if err := prof.RequireBuilders(buildgraph.RequiredBuilders(res)); err != nil {
			return nil, err
		}
	}
	if !c.Bool("dry-run") {
		fmt.Fprintf(os.Stderr, "building with up to %d parallel job(s)\n", c.Int("jobs"))
	}
	only := selectedComponents(c)
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
		Flags: append(append(append(append(selectionFlags(), buildFlags()...),
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
// pinned, content-addressed prebuilt artifacts the selected graph declares — downloading each and
// verifying it against the pinned digest (fail closed), never compiling. It runs only
// the prebuilt components (a from-source component is left to `nkdctl build`), so on a
// mixed graph `fetch` + `build` cover the whole set. No builder-profile preflight
// applies: acquisition is host-independent.
func cmdFetch() *cli.Command {
	return &cli.Command{
		Name:   "fetch",
		Hidden: true, // retained for legacy compatibility
		Usage:  "acquire pinned prebuilt artifacts (no compile): download + verify against the pinned digest",
		Flags: append(selectionFlags(),
			&cli.StringSliceFlag{Name: "only", Usage: "acquire only these components (repeatable)"},
			&cli.IntFlag{Name: "jobs", Aliases: []string{"j"}, Value: 4, Usage: "max concurrent acquisitions"},
			&cli.StringFlag{Name: "source-root", Value: ".nkd/work/src", Usage: "work root (artifacts land alongside, in ../fetch)"},
			&cli.StringFlag{Name: "runtime", Aliases: []string{"docker"}, Value: "docker", Usage: "container runtime for image pulls"},
			&cli.BoolFlag{Name: "package", Usage: "assemble acquired binaries into reproducible .deb packages and record that digest"},
			&cli.BoolFlag{Name: "keep-going", Usage: "continue past a failed acquisition instead of failing closed"},
			&cli.BoolFlag{Name: "dry-run", Usage: "print the acquisition plan without downloading"},
		),
		Action: func(c *cli.Context) error {
			res, _, err := resolveBuild(c)
			if err != nil {
				return err
			}
			g, err := buildgraph.Compile(res)
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
					"selected build has no prebuilt components — use `nkdctl build` for the from-source lane")
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

func printReport(rep *pipeline.Report) {
	if rep == nil {
		return
	}
	fmt.Printf("\n──────── nkdctl build report ────────\n")
	fmt.Printf("buildDigest:  %s\n\n", rep.BuildDigest)
	fmt.Printf("authority:     %s\n\n", reportAuthorityLabel(rep.Qualified(), rep.QualificationProfileDigest()))
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

// reportAuthorityLabel describes an existing marker; it grants no authority.
// Profile-free acquisition has no measured execution or reproduction claim.
func reportAuthorityLabel(qualified bool, profileDigest string) string {
	switch {
	case !qualified:
		return "UNQUALIFIED DEVELOPMENT OUTPUT, cannot attest, release, or claim reproduction"
	case profileDigest == "":
		return "verified acquired bytes, no execution qualification or reproduction claim"
	default:
		return fmt.Sprintf("qualified execution (%s)", profileDigest)
	}
}
