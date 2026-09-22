package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/attest"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/target"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/trust"
	"github.com/urfave/cli/v2"
)

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
