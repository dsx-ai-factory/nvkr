package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/profile"
	"github.com/urfave/cli/v2"
)

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
