// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/assemble"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/attest"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/release"
	"github.com/urfave/cli/v2"
)

// emitDiskManifest builds the unified DiskManifest for a canonical RAW/ISO stream (ADR-14
// §1): the logical digest + per-block manifest, optionally verifying a derived envelope
// decodes to the same logical digest and recording it as provenance. Fail-closed on
// encrypted input (ADR-14 §3).
func emitDiskManifest(c *cli.Context) error {
	raw := c.String("from-raw")
	format := c.String("format")
	envelope := c.String("envelope")
	diskKind := assemble.DiskKindRAW
	switch format {
	case "raw":
		if envelope != "" {
			return e.New(e.CodeUsage, "assemble --format raw cannot carry --envelope; select its actual derived format")
		}
	case "iso":
		if envelope != "" {
			return e.New(e.CodeUsage, "assemble --format iso cannot carry a decoded RAW envelope")
		}
		diskKind = assemble.DiskKindISO
	case "qcow2":
		if envelope == "" {
			return e.New(e.CodeUsage, "assemble --format qcow2 --from-raw requires --envelope <qcow2>")
		}
	default:
		return e.New(e.CodeUnsupported,
			"assemble --from-raw supports canonical raw/iso manifests and qualified qcow2 envelope verification, not format %q", format)
	}
	m, err := assemble.NewDiskManifestContext(c.Context, diskKind, raw, c.Int("block-size"))
	if err != nil {
		return err
	}
	if env := envelope; env != "" {
		codec, err := assemble.OpenQemuImgCodec(c.Context, c.String("qemu-img"), c.String("qemu-img-digest"))
		if err != nil {
			return err
		}
		defer func() { _ = codec.Close() }()
		if err := m.AddEnvelope(c.Context, codec, c.String("format"), env); err != nil {
			return err
		}
	}
	dg, err := m.Digest()
	if err != nil {
		return err
	}
	if out := c.String("manifest-out"); out != "" {
		b, err := m.Marshal()
		if err != nil {
			return err
		}
		if err := writeAtomic(c.Context, out, b, 0o644); err != nil {
			return err
		}
	}
	fmt.Printf("DiskManifest %s (%s, %d bytes, logical %s)\n", dg, m.DiskKind, m.VirtualSizeBytes, m.LogicalDigest)
	return nil
}

// verifyDecode is the `verify --decode` body (ADR-14 §2): decode a derived disk
// envelope to its canonical RAW logical stream via a shelled qemu-img and compare THAT
// digest to the expected manifest — never the container-file bytes. It fails closed if
// the envelope is encrypted (ciphertext is never a reproduction), on any decode error,
// and on a digest mismatch, naming the divergence. No rebuild happens.
func verifyDecode(c *cli.Context, want *attest.Manifest, envelope string) error {
	// Choose the expected RAW logical digest: an explicit --decode-component, else the
	// sole disk component (outputType raw/qcow2/iso/disk), else fail closed.
	var expected, name string
	if cn := c.String("decode-component"); cn != "" {
		for _, comp := range want.Components {
			if comp.Name == cn {
				expected, name = comp.SHA256, comp.Name
				break
			}
		}
		if expected == "" {
			return e.New(e.CodeVerifyFailed, "--decode-component %q not found in the manifest", cn)
		}
	} else {
		disk := map[string]bool{"raw": true, "qcow2": true, "iso": true, "disk": true}
		matches := 0
		for _, comp := range want.Components {
			if disk[comp.OutputType] {
				expected, name = comp.SHA256, comp.Name
				matches++
			}
		}
		if matches == 0 {
			return e.New(e.CodeVerifyFailed, "no disk component in the manifest to compare; pass --decode-component <name>")
		}
		if matches > 1 {
			return e.New(e.CodeUsage, "manifest has multiple disk components; disambiguate with --decode-component <name>")
		}
	}
	codec, err := assemble.OpenQemuImgCodec(c.Context, c.String("qemu-img"), c.String("qemu-img-digest"))
	if err != nil {
		return err
	}
	defer func() { _ = codec.Close() }()
	got, size, err := codec.EnvelopeLogicalDigest(c.Context, c.String("decode-format"), envelope)
	if err != nil {
		return err
	}
	if got != expected {
		return e.New(e.CodeVerifyFailed,
			"envelope %s decodes to logical digest %s, expected %s (component %q) — the derived envelope is NOT a faithful encoding of the reproduced RAW",
			envelope, got, expected, name)
	}
	fmt.Printf("VERIFIED: %s decodes to the expected RAW logical digest %s (component %q, %d bytes) — envelope faithful (ADR-14 §2)\n", envelope, got, name, size)
	return nil
}

// cmdAssemble retains the planned delivery surface while failing every unqualified
// producer lane before I/O. Its currently supported operation measures existing
// RAW/ISO bytes and, on Linux, verifies a derived qcow2 through the held codec seam.
func cmdAssemble() *cli.Command {
	flags := []cli.Flag{
		&cli.StringFlag{Name: "format", Required: true, Usage: "raw | iso | qcow2 for existing-byte measurement; producer formats currently fail closed"},
		&cli.BoolFlag{Name: "boot", Usage: "reserved; fails closed until a qualified boot-observation adapter exists"},
		&cli.IntFlag{Name: "boot-timeout", Value: 900},
		&cli.BoolFlag{Name: "encrypt", Usage: "reserved; fails closed until a qualified post-attestation encryption adapter exists"},
		&cli.StringFlag{Name: "from", Usage: "reserved input for the disabled --encrypt adapter"},
		&cli.StringFlag{Name: "from-raw", Usage: "emit a unified DiskManifest for this canonical RAW/ISO stream (ADR-14 §1)"},
		&cli.StringFlag{Name: "envelope", Usage: "with --from-raw: also verify this derived envelope decodes to the canonical logical digest and record it as provenance"},
		&cli.IntFlag{Name: "block-size", Value: 1 << 20,
			Usage: fmt.Sprintf("with --from-raw (raw): per-block manifest size in bytes (%d..%d; at most %d blocks)",
				assemble.MinDiskBlockSizeBytes, assemble.MaxDiskBlockSizeBytes, assemble.MaxDiskManifestBlocks)},
		&cli.StringFlag{Name: "manifest-out", Usage: "with --from-raw: write the DiskManifest here"},
		&cli.StringFlag{Name: "qemu-img", Value: assemble.QemuImgDefault, Usage: "with --envelope: the qemu-img binary (shelled)"},
		&cli.StringFlag{Name: "qemu-img-digest", Usage: "with --envelope: exact sha256 digest of the qemu-img executable"},
	}
	return &cli.Command{
		Name:  "assemble",
		Usage: "measure existing canonical disk bytes; qualified delivery producers are disabled",
		Flags: flags,
		Action: func(c *cli.Context) error {
			// These flags name producer/observer workflows whose authority protocols are
			// not implemented. Reject them before --from-raw, catalog resolution, path
			// inspection, output creation, or subprocess execution.
			if c.Bool("encrypt") {
				return e.New(e.CodeEngineCapability,
					"assemble --encrypt is disabled until a qualified post-attestation encryption adapter can return a bound non-reproduction receipt")
			}
			if c.Bool("boot") {
				return e.New(e.CodeEngineCapability,
					"assemble --boot is disabled until a qualified boot observer can return authenticated execution evidence")
			}
			// Existing canonical RAW/ISO bytes may still be measured into the typed
			// DiskManifest; this is the supported non-producer path.
			if c.String("from-raw") != "" {
				return emitDiskManifest(c)
			}
			switch f := c.String("format"); f {
			case "raw", "qcow2", "iso":
				return runDisk(c)
			default:
				return e.New(e.CodeUnsupported, "assemble --format %q not supported (raw|qcow2|iso)", f)
			}
		},
	}
}

// cmdPublish owns the operator-facing delivery representation. The subcommands
// create and move exact OCI/APT layouts; none converts diagnostic inputs into
// release authority. Qualified publication remains an SDK capability boundary.
func cmdPublish() *cli.Command {
	return &cli.Command{
		Name:            "publish",
		Usage:           "build, inspect, push, pull, and consume exact operator delivery layouts",
		HideHelpCommand: true,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "artifacts", Hidden: true},
			&cli.StringFlag{Name: "oci", Hidden: true},
			&cli.StringFlag{Name: "apt", Hidden: true},
			&cli.StringFlag{Name: "object", Hidden: true},
			&cli.BoolFlag{Name: "verified", Hidden: true},
			&cli.BoolFlag{Name: "release", Hidden: true},
			&cli.BoolFlag{Name: "complete", Hidden: true},
			&cli.BoolFlag{Name: "check", Hidden: true},
		},
		Subcommands: []*cli.Command{
			publishBundleCmd(), publishRootCmd(), publishPushCmd(), publishPullCmd(),
			publishInspectCmd(), publishGetCmd(), publishExtractCmd(), publishImageExportCmd(), publishChartCmd(), publishAPTCmd(),
		},
		Action: func(*cli.Context) error {
			return e.New(e.CodeUnsupported,
				"standalone publish is disabled: --verified/--release/--complete booleans are not authority; qualified release publication is not implemented")
		},
	}
}

func publishChartCmd() *cli.Command {
	return &cli.Command{Name: "chart", Usage: "diagnostically verify a pinned Helm OCI layout and extract its unchanged archive (no Release authority)",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "layout", Required: true}, &cli.StringFlag{Name: "manifest", Required: true},
			&cli.StringFlag{Name: "name", Required: true}, &cli.StringFlag{Name: "version", Required: true},
			&cli.StringFlag{Name: "out", Required: true},
		}, Action: func(c *cli.Context) error {
			result, err := delivery.ExportHelmChart(c.Context, c.String("layout"), c.String("manifest"), c.String("name"), c.String("version"), c.String("out"))
			if err != nil {
				return err
			}
			raw, err := canonical.Marshal(result)
			if err != nil {
				return e.Wrap(e.CodeInternal, err, "encoding chart extraction observation")
			}
			fmt.Println(string(raw))
			return nil
		}}
}

func publishBundleCmd() *cli.Command {
	return &cli.Command{
		Name: "bundle", Usage: "build a deterministic OCI binary bundle from exact regular files",
		Flags: []cli.Flag{
			&cli.StringSliceFlag{Name: "file", Required: true, Usage: "NAME=PATH (repeatable)"},
			&cli.StringSliceFlag{Name: "install", Usage: "NAME=/absolute/install/path (repeatable; default /usr/local/bin/NAME)"},
			&cli.StringFlag{Name: "os", Value: "linux"},
			&cli.StringFlag{Name: "arch", Required: true},
			&cli.StringFlag{Name: "purpose", Value: "operator-tools"},
			&cli.Int64Flag{Name: "epoch", Value: 0, Usage: "deterministic tar timestamp"},
			&cli.StringFlag{Name: "release-set-digest"},
			&cli.StringFlag{Name: "inventory-digest"},
			&cli.StringFlag{Name: "out", Required: true, Usage: "new OCI layout directory"},
		},
		Action: func(c *cli.Context) error {
			files, err := assignmentMap(c.StringSlice("file"), "--file")
			if err != nil {
				return err
			}
			installs, err := assignmentMap(c.StringSlice("install"), "--install")
			if err != nil {
				return err
			}
			inputs := make([]delivery.BinaryFileInput, 0, len(files))
			for name, path := range files {
				install := installs[name]
				if install == "" {
					install = "/usr/local/bin/" + name
				}
				inputs = append(inputs, delivery.BinaryFileInput{Name: name, Path: path, InstallPath: install, Mode: 0o755})
				delete(installs, name)
			}
			if len(installs) != 0 {
				return e.New(e.CodeUsage, "--install names a file not present in --file")
			}
			result, err := delivery.BuildBinaryBundle(c.Context, c.String("out"), delivery.BinaryBundleRequest{
				OS: c.String("os"), Arch: c.String("arch"), Purpose: c.String("purpose"), Epoch: c.Int64("epoch"),
				ReleaseSetDigest: c.String("release-set-digest"), InventoryDigest: c.String("inventory-digest"), Files: inputs,
			})
			if err != nil {
				return err
			}
			fmt.Printf("built OCI binary bundle %s (semantic %s) → %s\n", result.RootDigest, result.SemanticDigest, result.LayoutPath)
			return nil
		},
	}
}

func publishRootCmd() *cli.Command {
	return &cli.Command{
		Name: "root", Usage: "build an UNQUALIFIED diagnostic OCI publication root",
		Flags: []cli.Flag{
			&cli.StringSliceFlag{Name: "bundle", Usage: "NAME=OCI_LAYOUT (repeatable)"},
			&cli.StringFlag{Name: "inventory", Usage: "canonical Inventory JSON"},
			&cli.StringFlag{Name: "release-set", Usage: "canonical ReleaseSet JSON"},
			&cli.StringFlag{Name: "out", Required: true, Usage: "new OCI layout directory"},
		},
		Action: func(c *cli.Context) error {
			bundles, err := assignmentMap(c.StringSlice("bundle"), "--bundle")
			if err != nil {
				return err
			}
			request := delivery.PublicationRequest{}
			for name, path := range bundles {
				_, result, err := delivery.InspectBinaryBundle(c.Context, path)
				if err != nil {
					return err
				}
				request.Objects = append(request.Objects, delivery.ObjectInput{Name: name, Kind: "binary-bundle", Path: path,
					ArtifactType: delivery.MediaBinaryBundle, SemanticDigest: result.SemanticDigest, Layout: true})
			}
			if (c.String("inventory") == "") != (c.String("release-set") == "") {
				return e.New(e.CodeUsage, "--inventory and --release-set must be supplied together")
			}
			if path := c.String("inventory"); path != "" {
				var inventory release.Inventory
				if err := readCanonicalJSONContext(c.Context, path, &inventory); err != nil {
					return err
				}
				digest, err := inventory.Digest()
				if err != nil {
					return err
				}
				request.InventoryDigest = digest
				request.Objects = append(request.Objects, delivery.ObjectInput{Name: "inventory", Kind: "inventory", Path: path,
					ArtifactType: delivery.MediaInventory, LayerMediaType: delivery.MediaInventory, SemanticDigest: digest})
			}
			if path := c.String("release-set"); path != "" {
				var set release.ReleaseSet
				if err := readCanonicalJSONContext(c.Context, path, &set); err != nil {
					return err
				}
				digest, err := set.Digest()
				if err != nil {
					return err
				}
				if set.InventoryRef != request.InventoryDigest {
					return e.New(e.CodeInventoryUnpinned, "ReleaseSet inventoryRef does not match --inventory")
				}
				request.ReleaseSetDigest = digest
				request.Objects = append(request.Objects, delivery.ObjectInput{Name: "release-set", Kind: "release-set", Path: path,
					ArtifactType: delivery.MediaReleaseSet, LayerMediaType: delivery.MediaReleaseSet, SemanticDigest: digest})
			}
			result, err := delivery.BuildPublicationRoot(c.Context, c.String("out"), request)
			if err != nil {
				return err
			}
			fmt.Printf("built UNQUALIFIED diagnostic OCI publication root %s (semantic %s) → %s\n", result.RootDigest, result.SemanticDigest, result.LayoutPath)
			return nil
		},
	}
}

func publishPushCmd() *cli.Command {
	return &cli.Command{Name: "push", Usage: "push a complete OCI publication root and print its digest-pinned operator reference",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "layout", Required: true}, &cli.StringFlag{Name: "to", Required: true, Usage: "registry/repository:tag"},
			&cli.BoolFlag{Name: "plain-http", Usage: "allow HTTP (loopback development registries only)"},
			&cli.BoolFlag{Name: "anonymous", Usage: "do not consult ambient registry credentials"},
			&cli.BoolFlag{Name: "allow-diagnostic", Usage: "explicitly allow pushing a non-authoritative diagnostic root"},
		}, Action: func(c *cli.Context) error {
			inspection, err := delivery.Inspect(c.Context, c.String("layout"))
			if err != nil {
				return err
			}
			if inspection.Root.Qualification == delivery.QualificationDiagnostic && !c.Bool("allow-diagnostic") {
				return e.New(e.CodeGateUnmet, "refusing to publish an UNQUALIFIED diagnostic root without --allow-diagnostic")
			}
			ref, err := delivery.Push(c.Context, c.String("layout"), c.String("to"), delivery.RegistryOptions{PlainHTTP: c.Bool("plain-http"), Anonymous: c.Bool("anonymous")})
			if err != nil {
				return err
			}
			fmt.Printf("published %s root: %s\n", qualificationLabel(inspection.Root.Qualification), ref)
			return nil
		}}
}

// qualificationLabel renders a publication root's self-declared Qualification honestly.
// Push/Pull/Inspect verify OCI structure and digest-pinned extraction, but they do NOT
// authenticate the embedded clearance, so a QUALIFIED value is a DECLARED claim, not
// authenticated release authority. Printing a bare "QUALIFIED" next to "verified" would
// conflate the two; an operator must still authenticate the clearance against its policy.
func qualificationLabel(qualification string) string {
	if qualification == delivery.QualificationQualified {
		return "declared-QUALIFIED (clearance not authenticated by this command)"
	}
	return strings.ToUpper(qualification)
}

func publishPullCmd() *cli.Command {
	return &cli.Command{Name: "pull", Usage: "pull and verify a digest-pinned OCI publication root",
		Flags: []cli.Flag{&cli.StringFlag{Name: "ref", Required: true, Usage: "repository@sha256:digest"}, &cli.StringFlag{Name: "out", Required: true},
			&cli.BoolFlag{Name: "plain-http"}, &cli.BoolFlag{Name: "anonymous"}},
		Action: func(c *cli.Context) error {
			inspection, err := delivery.Pull(c.Context, c.String("ref"), c.String("out"), delivery.RegistryOptions{PlainHTTP: c.Bool("plain-http"), Anonymous: c.Bool("anonymous")})
			if err != nil {
				return err
			}
			fmt.Printf("pulled and digest-verified %s publication root %s → %s\n", qualificationLabel(inspection.Root.Qualification), inspection.RootDigest, c.String("out"))
			return nil
		}}
}

func publishInspectCmd() *cli.Command {
	return &cli.Command{Name: "inspect", Usage: "verify and display a local OCI publication root",
		Flags: []cli.Flag{&cli.StringFlag{Name: "layout", Required: true}, &cli.BoolFlag{Name: "json"}},
		Action: func(c *cli.Context) error {
			inspection, err := delivery.Inspect(c.Context, c.String("layout"))
			if err != nil {
				return err
			}
			if c.Bool("json") {
				raw, err := canonical.Marshal(inspection.Root)
				if err != nil {
					return err
				}
				fmt.Println(string(raw))
			} else {
				fmt.Printf("%s publication root %s (%d objects)\n", qualificationLabel(inspection.Root.Qualification), inspection.RootDigest, len(inspection.Root.Objects))
				for _, object := range inspection.Root.Objects {
					fmt.Printf("  %-24s %-18s %s\n", object.Name, object.Kind, object.SemanticDigest)
				}
			}
			return nil
		}}
}

func publishExtractCmd() *cli.Command {
	return &cli.Command{Name: "extract", Usage: "verify a publication root and extract one named binary bundle",
		Flags: []cli.Flag{&cli.StringFlag{Name: "layout", Required: true}, &cli.StringFlag{Name: "bundle", Required: true}, &cli.StringFlag{Name: "out", Required: true}},
		Action: func(c *cli.Context) error {
			bundle, err := delivery.ExtractBinaryBundle(c.Context, c.String("layout"), c.String("bundle"), c.String("out"))
			if err != nil {
				return err
			}
			fmt.Printf("verified and extracted %d files from %s → %s\n", len(bundle.Entries), c.String("bundle"), c.String("out"))
			for _, entry := range bundle.Entries {
				fmt.Printf("  %s -> %s (%s)\n", entry.Name, entry.InstallPath, entry.SHA256)
			}
			return nil
		}}
}

func publishGetCmd() *cli.Command {
	return &cli.Command{Name: "get", Usage: "verify a publication root and extract one exact metadata or artifact object",
		Flags: []cli.Flag{&cli.StringFlag{Name: "layout", Required: true}, &cli.StringFlag{Name: "object", Required: true}, &cli.StringFlag{Name: "out", Required: true}},
		Action: func(c *cli.Context) error {
			object, err := delivery.ExtractObject(c.Context, c.String("layout"), c.String("object"), c.String("out"))
			if err != nil {
				return err
			}
			fmt.Printf("verified and extracted %s/%s %s → %s\n", object.Kind, object.Name, object.SemanticDigest, c.String("out"))
			return nil
		}}
}

func publishImageExportCmd() *cli.Command {
	return &cli.Command{Name: "image-export", Usage: "export one exact native published image to an unchanged OCI archive (no Release authority)",
		Flags: []cli.Flag{&cli.StringFlag{Name: "layout"}, &cli.StringFlag{Name: "object"}, &cli.StringFlag{Name: "reference"}, &cli.StringFlag{Name: "platform"}, &cli.StringFlag{Name: "out"}},
		Action: func(c *cli.Context) error {
			if c.Args().Present() || c.String("layout") == "" || c.String("object") == "" || c.String("reference") == "" || c.String("out") == "" {
				return e.New(e.CodeUsage, "image-export requires --layout, --object, --reference and --out, with no positional arguments")
			}
			result, err := delivery.ExportPublishedImage(c.Context, c.String("layout"), c.String("object"), c.String("reference"), c.String("platform"), c.String("out"))
			if err != nil {
				return err
			}
			if err := json.NewEncoder(c.App.Writer).Encode(result); err != nil {
				return e.Wrap(e.CodeVerifyFailed, err, "writing image export description")
			}
			return nil
		}}
}

func publishAPTCmd() *cli.Command {
	return &cli.Command{Name: "apt", Usage: "project exact .deb files into a deterministic local APT repository",
		Flags: []cli.Flag{&cli.StringSliceFlag{Name: "package", Required: true}, &cli.StringFlag{Name: "distribution", Value: "stable"},
			&cli.StringFlag{Name: "component", Value: "main"}, &cli.Int64Flag{Name: "epoch", Value: 0},
			&cli.StringFlag{Name: "release-set-digest"}, &cli.StringFlag{Name: "inventory-digest"}, &cli.StringFlag{Name: "out", Required: true}},
		Action: func(c *cli.Context) error {
			packages := make([]delivery.DebianPackageInput, 0, len(c.StringSlice("package")))
			for _, path := range c.StringSlice("package") {
				packages = append(packages, delivery.DebianPackageInput{Path: path})
			}
			result, err := delivery.BuildAPTRepository(c.Context, c.String("out"), delivery.APTRequest{Distribution: c.String("distribution"),
				Component: c.String("component"), Epoch: c.Int64("epoch"), ReleaseSetDigest: c.String("release-set-digest"),
				InventoryDigest: c.String("inventory-digest"), Packages: packages})
			if err != nil {
				return err
			}
			fmt.Printf("built UNSIGNED local APT projection %s (%d packages) → %s\n", result.Digest, len(result.Repository.Packages), result.Path)
			return nil
		}}
}

func assignmentMap(values []string, flag string) (map[string]string, error) {
	out := make(map[string]string, len(values))
	for _, value := range values {
		name, path, ok := strings.Cut(value, "=")
		if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(path) == "" || name != strings.TrimSpace(name) || path != strings.TrimSpace(path) {
			return nil, e.New(e.CodeUsage, "%s expects NAME=VALUE", flag)
		}
		if _, duplicate := out[name]; duplicate {
			return nil, e.New(e.CodeUsage, "%s repeats name %q", flag, name)
		}
		out[name] = path
	}
	return out, nil
}
