package main

import (
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
	"reflect"
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
	"github.com/NVIDIA/nke-recipes/pkg/nkd/buildgraph"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/profile"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/provenance"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/target"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/trust"
	"github.com/urfave/cli/v2"
)

// ── shared flags ─────────────────────────────────────────────────────────────

func selectionFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "data", Usage: "explicit catalog directory"},
		&cli.StringSliceFlag{Name: "component", Usage: "exact component ID to build (repeatable)"},
		&cli.StringFlag{Name: "platform", Usage: "target platform: linux/amd64 | linux/arm64"},
		&cli.StringFlag{Name: "aicr-profile", Usage: "exact catalog AICR profile, instead of components"},
	}
}

func providerFor(c *cli.Context) catalog.DataProvider {
	if d := c.String("data"); d != "" {
		return catalog.Dir(d)
	}
	return nil
}

// resolveBuild selects exact catalog producers and compiles their pinned graph.
func resolveBuild(c *cli.Context) (*buildgraph.Resolved, string, error) {
	if c.String("data") == "" {
		return nil, "", e.New(e.CodeUsage, "build resolution requires --data <catalog-directory>")
	}
	resolution, err := client.New(client.WithProvider(providerFor(c))).ResolveBuild(c.Context, client.ResolveRequest{
		Components: c.StringSlice("component"), Platform: c.String("platform"), AICRProfile: c.String("aicr-profile"),
	})
	if err != nil {
		return nil, "", err
	}
	return resolution.Resolved, resolution.BuildDigest, nil
}

// resolveGraph is the shared front half of every command: load + validate the
// catalog, select producers, compile the IR, and compute buildDigest.
func resolveGraph(c *cli.Context) (*ir.Graph, string, error) {
	res, _, err := resolveBuild(c)
	if err != nil {
		return nil, "", err
	}
	g, err := buildgraph.Compile(res)
	if err != nil {
		return nil, "", err
	}
	digest, err := g.Digest()
	if err != nil {
		return nil, "", err
	}
	return g, digest, nil
}

// ── resolve ───────────────────────────────────────────────────────────────────

func cmdResolve() *cli.Command {
	return &cli.Command{
		Name:  "resolve",
		Usage: "resolve selected producers and print the pinned build graph",
		Flags: append(selectionFlags(),
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
				// transport locators and execution requirements and is not buildDigest
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
				fmt.Printf("buildDigest: %s\n\n", digest)
				for _, l := range comps {
					fmt.Println(l)
				}
			default:
				b, err := g.MarshalDeterministic()
				if err != nil {
					return err
				}
				os.Stdout.Write(b)
				fmt.Printf("\n# buildDigest: %s\n", digest)
			}
			return nil
		},
	}
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
	Resolved           *buildgraph.Resolved
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
	res, _, err := resolveBuild(c)
	if err != nil {
		return nil, err
	}
	graph, err := buildgraph.Compile(res)
	if err != nil {
		return nil, err
	}
	digest, err := graph.Digest()
	if err != nil {
		return nil, err
	}
	result := &qualifiedLocalBuild{Resolved: res}
	only := selectedComponents(c)
	options := pipeline.RunOptions{
		Ctx: c.Context, SourceRoot: sourceRoot, Only: only, Jobs: c.Int("jobs"),
		KeepGoing: c.Bool("keep-going"), Package: c.Bool("package"), Runtime: c.String("runtime"),
		Stdout: os.Stdout, Stderr: os.Stderr,
	}
	if buildHasSource(res) {
		if authenticatedProfile == nil {
			return result, e.New(e.CodeUntrustedProfile,
				"qualified from-source output requires a separately authenticated builder profile with runner, runtime, toolchain, and sandbox execution identities")
		}
		builderProfile := authenticatedProfile.Profile
		if err := builderProfile.RequireBuilders(buildgraph.RequiredBuilders(res)); err != nil {
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
	return append(append(append(selectionFlags(), buildFlags()...),
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
		fmt.Printf("\nUNQUALIFIED LOCAL DIAGNOSTIC: BIT-IDENTICAL across two same-host work paths (%d component(s)); buildDigest %s\n", len(ma.Components), a.BuildDigest)
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
	fmt.Printf("UNQUALIFIED DIAGNOSTIC: emitted one local observed-digests record → %s (buildDigest %s); it is not a validation vector or reproduction receipt\n", out, report.BuildDigest)
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
		selected, digest, err := resolveProfile(c, target.Arch(c.String("platform")))
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
		raw, err := readBoundedRegularInput(ctx, p, trust.MaxAuthenticatedDocumentBytes)
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
	fmt.Printf("UNQUALIFIED DIAGNOSTIC: record bytes are bit-identical across the two environments (%d component(s)); buildDigest %s\n", len(a.Components), a.BuildDigest)
	fmt.Println("(no execution receipt was authenticated from disk; this comparison is not validation evidence)")
	return nil
}

func cmdSBOM() *cli.Command {
	return &cli.Command{
		Name:   "sbom",
		Hidden: true, // retained as legacy compatibility for attest --sbom
		Usage:  "emit an UNQUALIFIED diagnostic CycloneDX SBOM from a caller-supplied manifest",
		Flags: append(selectionFlags(),
			&cli.StringFlag{Name: "out", Aliases: []string{"o"}, Value: "sbom.cdx.json", Usage: "SBOM output path (or output dir with --per-artifact)"},
			&cli.BoolFlag{Name: "per-artifact", Usage: "emit one SBOM per built artifact + an aggregate index (ADR-13 §1)"},
			&cli.BoolFlag{Name: "diagnostic", Usage: "accept an UNQUALIFIED bake-off observed-digests record instead of a publishable validation vector"},
			&cli.StringFlag{Name: "expected-digests", Aliases: []string{"e"}, Required: true,
				Usage: "canonical vector, or with --diagnostic an observed-digests record supplying the selected exact artifact digest(s)"},
		),
		Action: func(c *cli.Context) error {
			res, digest, err := resolveBuild(c)
			if err != nil {
				return err
			}
			raw, err := readBoundedRegularInput(c.Context, c.String("expected-digests"), trust.MaxAuthenticatedDocumentBytes)
			if err != nil {
				return e.Wrap(e.CodeVerifyFailed, err, "reading manifest")
			}
			m, err := parseSBOMManifest(raw, c.Bool("diagnostic"))
			if err != nil {
				return err
			}
			if m.BuildDigest != digest {
				return e.New(e.CodeVerifyFailed,
					"expected-digests buildDigest %s does not match the resolved build %s", m.BuildDigest, digest)
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
		Flags: append(append(append(selectionFlags(), buildFlags()...),
			&cli.StringFlag{Name: "out", Aliases: []string{"o"}, Value: "expected-digests.json",
				Usage: "manifest output path"},
			&cli.StringFlag{Name: "conformance", Usage: "reserved legacy field; qualified publisher vectors reject caller-authored conformance claims"},
			&cli.StringFlag{Name: "validation-vector", Usage: "reserved legacy field; qualified publisher vectors reject caller-authored validation references"},
			&cli.StringFlag{Name: "evidence-out", Usage: "also write a deterministic evidence bundle (verdict + preflight + per-component causes)"},
			&cli.BoolFlag{Name: "aicr-bundle", Usage: "reserved; publisher-attestation AICR bundling is unavailable in this release"},
			&cli.StringFlag{Name: "aicr-out", Value: ".nkd/work/aicr-bundle", Usage: "reserved output path for --aicr-bundle"},
			&cli.BoolFlag{Name: "vuln", Usage: "scan the deterministic SBOM with a shelled scanner and fold the report digest into the vector (ADR-13 §3)"},
			&cli.StringFlag{Name: "vuln-artifact", Usage: "with --vuln: exact component whose per-artifact SBOM is scanned (required for a multi-artifact build)"},
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
			g, err := buildgraph.Compile(res)
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
	res *buildgraph.Resolved, report *pipeline.Report,
) error {
	published, err := attest.VerifyPublishedVectorReceipt(ctx, vectorDocument)
	if err != nil {
		return err
	}
	g, err := buildgraph.Compile(res)
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

func printReport(rep *pipeline.Report) {
	if rep == nil {
		return
	}
	fmt.Printf("\n──────── nkdctl build report ────────\n")
	fmt.Printf("buildDigest:  %s\n\n", rep.BuildDigest)
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

// ── aicr ─────────────────────────────────────────────────────────────────────

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
					fmt.Printf("ok: %s — %d component(s), %d AICR profile(s)\n", cat.Metadata.Name, len(cat.Components), len(cat.AICRProfiles))
					return nil
				},
			},
			{
				Name:  "list",
				Usage: "list components and AICR profiles",
				Flags: []cli.Flag{&cli.StringFlag{Name: "data", Required: true, Usage: "path to the catalog directory"}},
				Action: func(c *cli.Context) error {
					cat, err := catalog.LoadContext(c.Context, providerFor(c))
					if err != nil {
						return err
					}
					fmt.Printf("catalog: %s\n\n", cat.Metadata.Name)
					profiles := make([]string, 0, len(cat.AICRProfiles))
					for name := range cat.AICRProfiles {
						profiles = append(profiles, name)
					}
					sort.Strings(profiles)
					fmt.Println("AICR profiles:")
					for _, name := range profiles {
						fmt.Printf("  %s\t%s\n", name, cat.AICRProfiles[name].Digest)
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
				Usage:     "resolve a source tag and any declared tool asset to exact pins, then write the lock",
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
				Usage:     "pin the newest constraint-matching source releases, including declared tool assets",
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
					&cli.StringSliceFlag{Name: "arch", Usage: "optional exact declared architecture set to pin"},
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
		if reflect.DeepEqual(prev, pin) {
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
		if want[a] {
			return e.New(e.CodeUsage, "pin-artifacts: component %q is selected more than once", a)
		}
		want[a] = true
	}
	if !c.Bool("all") && len(want) == 0 {
		return e.New(e.CodeUnsupported, "pin-artifacts: name a component or pass --all")
	}
	available := make(map[string]bool, len(targets))
	for _, selected := range targets {
		available[selected.Name] = true
	}
	for _, name := range c.Args().Slice() {
		if !available[name] {
			return e.New(e.CodeInvalidCatalog, "pin-artifacts: unknown prebuilt component %q", name)
		}
	}
	requestedArches := c.StringSlice("arch")
	for _, selected := range targets {
		if !c.Bool("all") && !want[selected.Name] {
			continue
		}
		if len(requestedArches) > 0 {
			declared := make([]string, 0, len(selected.Platforms))
			for _, platform := range selected.Platforms {
				declared = append(declared, target.Arch(platform))
			}
			requested := append([]string(nil), requestedArches...)
			sort.Strings(requested)
			if !reflect.DeepEqual(requested, declared) {
				return e.New(e.CodeUsage, "component %q requires exact declared architectures %v", selected.Name, declared)
			}
		}
	}
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
		arches := make([]string, 0, len(t.Platforms))
		for _, platform := range t.Platforms {
			arches = append(arches, target.Arch(platform))
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
