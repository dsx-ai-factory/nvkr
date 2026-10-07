// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/attest"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/trust"
	"github.com/urfave/cli/v2"
)

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
