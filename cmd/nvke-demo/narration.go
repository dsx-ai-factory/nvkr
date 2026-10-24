// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"slices"

	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const demoDocsURL = "https://github.com/dsx-ai-factory/nvkr/blob/68b09b8e71048fdbcbd4a11b4b7d1264a1e04c6f/docs/"

// presentDemoNarration reads authored inputs through their owning decoder. It
// only explains the selection: compilation and runtime admission remain with
// the existing build and deployment owners. No input contents are echoed.
func presentDemoNarration(ctx context.Context, out io.Writer, cfg nvkedemo.BuildConfig) error {
	if ctx == nil || out == nil {
		return nvkrerr.New(nvkrerr.CodeUsage, "walkthrough introduction requires a context and writer")
	}
	definitionName, environmentName, err := nvkedemo.LocalArtifactInputsFor(cfg.Network, cfg.Conformance, runtime.GOOS)
	if err != nil {
		return err
	}
	selected := filepath.Base(definitionName)
	network := "NVIDIA OVN (default)"
	switch cfg.Network {
	case "", "ovn":
	case "upstream-ovn":
		network = "upstream OVN diagnostic; does not validate the NVIDIA fork"
	case "kind-calico":
		network = "Kind/Calico diagnostic; does not validate NVIDIA OVN"
	default:
		return nvkrerr.New(nvkrerr.CodeUsage, "unsupported walkthrough network")
	}
	visibleRoot := filepath.Join("inputs", "definitions", "demo")
	root := filepath.Join(cfg.Repository, visibleRoot)
	definitionPath := filepath.Join(root, selected)
	environmentPath := filepath.Join(cfg.Repository, environmentName)
	if cfg.Definition != "" {
		definitionPath, environmentPath = cfg.Definition, cfg.Environment
		if !filepath.IsAbs(definitionPath) {
			definitionPath = filepath.Join(cfg.Repository, definitionPath)
		}
		if !filepath.IsAbs(environmentPath) {
			environmentPath = filepath.Join(cfg.Repository, environmentPath)
		}
		visibleRoot, selected = filepath.Dir(definitionPath), filepath.Base(definitionPath)
		environmentName = environmentPath
	}
	authored, err := definition.LoadDefinition(ctx, definitionPath)
	if err != nil {
		return err
	}
	if _, err := definition.LoadEnvironment(ctx, environmentPath); err != nil {
		return err
	}
	// An output-specific catalog is a complete stack, not an inherited patch.
	stacks := []definition.Catalog{authored.Catalog}
	for _, output := range authored.Outputs {
		if output.Catalog != nil {
			stacks = append(stacks, *output.Catalog)
		}
	}
	catalogFiles := make(map[string]bool)
	for _, stack := range stacks {
		catalogRoot := stack.Base
		if !filepath.IsAbs(catalogRoot) {
			catalogRoot = filepath.Join(visibleRoot, catalogRoot)
		}
		catalogFiles[filepath.Join(catalogRoot, "registry.yaml")] = true
		catalogFiles[filepath.Join(catalogRoot, "pins.lock.yaml")] = true
		for _, overlay := range stack.Overlays {
			if !filepath.IsAbs(overlay) {
				overlay = filepath.Join(visibleRoot, overlay)
			}
			catalogFiles[overlay] = true
		}
	}
	paths := make([]string, 0, len(catalogFiles))
	for path := range catalogFiles {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	var text bytes.Buffer
	fmt.Fprintln(&text, "NVKR → NVKE walkthrough: catalog + Environment → build → local publication → cluster")
	fmt.Fprintf(&text, "Networking: %s\n", network)
	fmt.Fprintln(&text, "Relative paths below use the checkout selected by --repo; absolute selections retain their location.")
	fmt.Fprintf(&text, "1. Definition requests %d named outputs: %q\n", len(authored.Outputs), filepath.Join(visibleRoot, selected))
	fmt.Fprintln(&text, "2. Reusable catalog producers describe how to build; pins select exact source, tool and artifact inputs.")
	fmt.Fprintln(&text, "   Open these selected catalog-stack files (reading paths is not execution or pin verification):")
	for _, path := range paths {
		fmt.Fprintf(&text, "   %q\n", path)
	}
	fmt.Fprintf(&text, "3. Environment selects execution settings: %q\n", environmentName)
	fmt.Fprintln(&text, "4. NVKR compiles the selection into a generated Plan, then records each required output's result.")
	fmt.Fprintf(&text, "   Attempt Plan, created during preparation: %q\n", filepath.Join(".nvkr", "laptop-bootstrap", cfg.AttemptPrefix, "selected-plan.json"))
	fmt.Fprintf(&text, "   Run record, written by execution: %q\n", filepath.Join(".nvkr", "runs", cfg.AttemptPrefix, "lifecycle.json"))
	fmt.Fprintln(&text, "5. Local publication distributes exact artifacts to NVKE; it is not a signed or qualified Release.")
	fmt.Fprintln(&text, "6. NVKE installs management services, boots the tenant VMs, checks workload/DNS, then follows the selected lifetime policy.")
	if cfg.Conformance {
		fmt.Fprintln(&text, "   Conformance uses the same fresh tenant and the version-matched tools from this Run; original JUnit and API observations are retained before cleanup.")
	}
	fmt.Fprintln(&text, "   With --cleanup: natural tenant deletion and joined owned cleanup must finish before success.")
	fmt.Fprintln(&text, "A Plan describes intent; observed artifact bytes, runtime checks and the natural exit establish the result.")
	fmt.Fprintln(&text, "Reference documentation at the reviewed source baseline:")
	for _, page := range []string{"user-concepts.md", "catalogs-and-components.md", "README.md", "local-nvke-demo.md"} {
		fmt.Fprintln(&text, demoDocsURL+page)
	}
	if err := ctx.Err(); err != nil {
		return nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "walkthrough introduction canceled")
	}
	if err := writeAll(out, text.Bytes()); err != nil {
		return nvkrerr.New(nvkrerr.CodePreflightFailed, "write walkthrough introduction")
	}
	return nil
}
