// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/assemble"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/urfave/cli/v2"
)

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
