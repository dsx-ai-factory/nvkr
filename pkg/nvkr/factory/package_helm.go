// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/helmarchive"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"sigs.k8s.io/yaml"
)

func executeHelmPackage(ctx context.Context, plan *Plan, node Node, output Artifact, inputs []Artifact, artifacts map[string]Artifact, stdout io.Writer) error {
	imageDigests := map[string]string{}
	if node.HelmImage != nil {
		var ids []string
		for _, input := range inputs {
			ids = append(ids, input.ID)
		}
		if err := validatePackageInputs(packageSpec{Kind: PackageHelm, HelmImage: node.HelmImage}, ids, artifacts); err != nil {
			return err
		}
		for _, slot := range sortedKeys(node.HelmImage.Images) {
			binding := node.HelmImage.Images[slot]

			imageArtifact := artifacts[binding.From]
			digest, err := factoryImagePayloadDigest(ctx, plan, imageArtifact)
			if err != nil {
				return err
			}
			imageDigests[slot] = digest
			fmt.Fprintf(stdout, "Verified Helm image slot %q at %s\n", slot, digest)
		}
		for _, input := range inputs {
			if input.Kind == "tarball" {
				inputs = []Artifact{input}
				break
			}
		}
	}
	if len(inputs) != 1 {
		return e.New(e.CodeInvalidCatalog, "Helm package %q requires exactly one tree or source archive", node.ID)
	}
	if inputs[0].Kind == "raw" {
		return executeAcquiredHelmPackage(ctx, plan, output, inputs[0], artifacts)
	}
	// legacyCarrier is not written by this function itself; it reclaims a
	// same-named carrier a prior interrupted run (or an older build of this
	// binary) may have left beside output.Path, so an owned factory workspace
	// is genuinely rerunnable.
	legacyCarrier := filepath.Join(filepath.Dir(output.Path), "."+output.Name+".helm.tar")
	if err := removeFactoryCarrier(legacyCarrier); err != nil {
		return err
	}
	root, relative, cleanup, err := helmPackageTree(ctx, inputs[0])
	if err != nil {
		return err
	}
	defer cleanup()
	// chartPrefix names the packaged tree's own chart directory, relative to
	// root. An unbound tree (a discovered source-archive root) is already the
	// chart; an image-bound tree is a wrapper one level above the chart, named
	// by node.HelmImage.Chart.
	chartPrefix := relative
	if node.HelmImage != nil {
		if err := bindHelmImages(ctx, filepath.Join(root, relative), *node.HelmImage, imageDigests); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Bound Helm chart %q to %d image slots\n", node.HelmImage.Chart, len(imageDigests))
		chartPrefix = filepath.Join(relative, node.HelmImage.Chart)
	}
	chartName, err := readHelmChartName(filepath.Join(root, chartPrefix, "Chart.yaml"))
	if err != nil {
		return err
	}
	// Unlike legacyCarrier, archive lives inside root, so cleanup() reclaims it.
	archive := filepath.Join(root, "bundle.tar")
	if err := transportarchive.Pack(ctx, archive, root, []string{relative}); err != nil {
		return err
	}
	// The Helm OCI chart-content layer is exactly what `helm package` produces: a
	// tar rooted at the chart's own name (Chart.yaml one level in), gzip-
	// compressed with a fixed header (see rerootHelmChartLayer for toolchain
	// byte-stability). The transport archive just packed may carry the chart under a
	// deeper, carrier-specific prefix (chartPrefix); re-root it onto a single
	// <chartName>/ entry instead of writing into the (possibly shared, not
	// node-owned) source tree to relocate it on disk. A bound tree may carry
	// sibling charts and files outside chartPrefix (e.g. other local charts, a
	// wrapper-level checksums.txt); rerootHelmChartLayer fails closed on those
	// rather than silently publishing only the bound chart and discarding the rest.
	chartArchive := archive + ".tgz"
	if err := removeFactoryCarrier(chartArchive); err != nil {
		return err
	}
	defer func() { _ = os.Remove(chartArchive) }()
	if err := rerootHelmChartLayer(archive, chartArchive, filepath.ToSlash(relative), filepath.ToSlash(chartPrefix), chartName); err != nil {
		return err
	}
	chartInfo, err := os.Stat(chartArchive)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "stating packaged Helm chart archive")
	}
	if chartInfo.Size() > maxHelmChartArchiveBytes {
		return e.New(e.CodeVerifyFailed, "packaged Helm chart archive exceeds %d bytes", maxHelmChartArchiveBytes)
	}
	chartData, err := os.ReadFile(chartArchive)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "reading packaged Helm chart archive")
	}
	_, err = delivery.BuildHelmChartArtifact(ctx, output.Path, output.MediaType, chartData)
	return err
}

// maxHelmChartArchiveBytes bounds the packaged Helm chart archive read fully
// into memory before publication. It is generous for any real chart (which
// are KB-to-low-MB in practice) while still failing closed on an unexpectedly
// large input instead of allocating an unbounded amount of memory.
const maxHelmChartArchiveBytes = 512 << 20

// helmPackageTree normalizes an observed source archive to a deterministic
// carrier. Source archives (e.g. a real `helm package` .tgz) have exactly one
// top-level root, whose name is discovered from the archive itself rather than
// assumed; transport extraction rejects escaping links, traversal, duplicates
// and extra roots. Discovery and extraction share one held file descriptor
// (DetectAndUnpackChartRoot) rather than opening input.Path twice, closing the
// pathname-replacement window a two-open sequence would otherwise leave.
func helmPackageTree(ctx context.Context, input Artifact) (string, string, func(), error) {
	noop := func() {}
	if input.Kind != "tarball" || input.BuildTarget == "" {
		return "", "", noop, e.New(e.CodeInvalidCatalog, "Helm input %q is not a build source archive", input.ID)
	}
	stage, err := os.MkdirTemp("", "nvkr-helm-")
	if err != nil {
		return "", "", noop, e.Wrap(e.CodeInternal, err, "staging Helm source archive")
	}
	cleanup := func() { _ = os.RemoveAll(stage) }
	chartRoot, err := transportarchive.DetectAndUnpackChartRoot(ctx, input.Path, stage)
	if err != nil {
		cleanup()
		return "", "", noop, err
	}
	info, err := os.Lstat(filepath.Join(stage, chartRoot))
	if err != nil || !info.IsDir() {
		cleanup()
		return "", "", noop, e.New(e.CodeVerifyFailed, "Helm source archive requires a chart directory")
	}
	return stage, chartRoot, cleanup, nil
}

// readHelmChartName reads only the chart's own declared name from its Chart.yaml,
// so the final packaged archive can be rooted the way `helm package` roots it
// (at the chart's own name), regardless of the name of the directory or archive
// that carried the chart tree up to this point.
func readHelmChartName(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", e.Wrap(e.CodeVerifyFailed, err, "reading Helm chart Chart.yaml")
	}
	var chart struct {
		Name string `json:"name"`
	}
	if err := yaml.Unmarshal(raw, &chart); err != nil {
		return "", e.Wrap(e.CodeVerifyFailed, err, "decoding Helm chart Chart.yaml")
	}
	if !canonicalName.MatchString(chart.Name) {
		return "", e.New(e.CodeVerifyFailed, "Helm chart Chart.yaml requires a canonical name")
	}
	return chart.Name, nil
}

// rerootHelmChartLayer reads the deterministic, uncompressed tar at source, keeps
// only the entries under prefix (the chart's own directory within it), rewrites
// their names so prefix becomes a single top-level chartName entry, and
// gzip-compresses the result deterministically into destination. This is the
// exact shape a real `helm package` archive has: a tar rooted directly at the
// chart, one path segment a Helm-conformant reader strips to find Chart.yaml.
func rerootHelmChartLayer(source, destination, wrapperRoot, prefix, chartName string) error {
	in, err := os.Open(source)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "opening Helm chart tar")
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "creating Helm chart archive")
	}
	gz, err := gzip.NewWriterLevel(out, gzip.BestCompression)
	if err != nil {
		_ = out.Close()
		return e.Wrap(e.CodeInternal, err, "initializing compressor")
	}
	// A fixed gzip header keeps the compressed bytes independent of wall-clock
	// time and the local OS, per this repo's deterministic-output rule; exact
	// byte reproduction additionally depends on the pinned Go toolchain (see
	// executeHelmPackage), since compress/gzip's DEFLATE output is not
	// guaranteed byte-stable across toolchain versions.
	gz.ModTime = time.Time{}
	gz.OS = 255
	tw := tar.NewWriter(gz)
	tr := tar.NewReader(in)
	found := false
	extra := map[string]bool{}
	for {
		header, readErr := tr.Next()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			_ = tw.Close()
			_ = gz.Close()
			_ = out.Close()
			return e.Wrap(e.CodeInternal, readErr, "reading Helm chart tar")
		}
		name := strings.TrimSuffix(header.Name, "/")
		var rerooted string
		switch {
		case name == prefix:
			rerooted = chartName
		case strings.HasPrefix(name, prefix+"/"):
			rerooted = chartName + strings.TrimPrefix(name, prefix)
		case name == wrapperRoot || strings.HasPrefix(prefix, name+"/"):
			// An ancestor directory of prefix (including the wrapper root
			// itself): not chart content, safe to omit from the chart layer.
			continue
		case name == wrapperRoot+"/checksums.txt":
			// For a nested chart, bindHelmImages' wrapper bookkeeping (see
			// helm_binding.go), not a real Helm chart file in any chart it
			// writes about; it was never meant to reach a published chart.
			// A root chart owns this filename and matches the prefix case above.
			continue
		default:
			// Content that lives in the wrapper alongside the bound chart but
			// is not part of it (a sibling chart, ...). Silently omitting this
			// from the published chart layer would drop it from the release
			// with no signal; record it and fail closed below instead.
			extra[topLevelHelmEntry(name, wrapperRoot)] = true
			continue
		}
		found = true
		header.Name = rerooted
		if err := tw.WriteHeader(header); err != nil {
			_ = tw.Close()
			_ = gz.Close()
			_ = out.Close()
			return e.Wrap(e.CodeInternal, err, "writing Helm chart tar header")
		}
		if header.Typeflag == tar.TypeReg {
			if _, err := io.Copy(tw, tr); err != nil {
				_ = tw.Close()
				_ = gz.Close()
				_ = out.Close()
				return e.Wrap(e.CodeInternal, err, "writing Helm chart tar member")
			}
		}
	}
	if !found {
		_ = tw.Close()
		_ = gz.Close()
		_ = out.Close()
		return e.New(e.CodeVerifyFailed, "Helm source archive has no chart directory %q", prefix)
	}
	if len(extra) > 0 {
		_ = tw.Close()
		_ = gz.Close()
		_ = out.Close()
		names := make([]string, 0, len(extra))
		for name := range extra {
			names = append(names, name)
		}
		sort.Strings(names)
		return e.New(e.CodeInvalidCatalog,
			"Helm source archive carries unpublished content outside the bound chart %q: %s; split it into separate Definition outputs",
			chartName, strings.Join(names, ", "))
	}
	if err := tw.Close(); err != nil {
		_ = gz.Close()
		_ = out.Close()
		return e.Wrap(e.CodeInternal, err, "closing Helm chart tar")
	}
	if err := gz.Close(); err != nil {
		_ = out.Close()
		return e.Wrap(e.CodeInternal, err, "closing Helm chart archive")
	}
	return out.Close()
}

// topLevelHelmEntry reduces a tar entry name to its first path segment
// beneath base, so a whole sibling tree (many files) collapses to one
// reported name instead of flooding the fail-closed error with every member.
func topLevelHelmEntry(name, base string) string {
	trimmed := strings.TrimPrefix(name, base+"/")
	if idx := strings.IndexByte(trimmed, '/'); idx >= 0 {
		trimmed = trimmed[:idx]
	}
	return trimmed
}

// executeAcquiredHelmPackage preserves the original compressed bytes. The result
// record is checked against the exact pull instruction, then a held snapshot is
// rechecked against that digest so pathname replacement cannot change the layer.
func executeAcquiredHelmPackage(ctx context.Context, plan *Plan, output, input Artifact, artifacts map[string]Artifact) error {
	producer := planNode(plan, input.Producer)
	_, instruction, err := plan.producerInstruction(producer)
	if err != nil {
		return err
	}
	if instruction.Kind != ir.KindPull || instruction.OutputType != "raw" {
		return e.New(e.CodeInvalidCatalog, "acquired Helm input %q requires a raw pull producer", input.ID)
	}
	result, err := observeProducerPayload(ctx, plan, input, artifacts)
	if err != nil {
		return err
	}
	held, err := heldfile.OpenContext(ctx, input.Path, heldfile.Options{Label: "acquired-helm", MaxBytes: helmarchive.MaxPackageBytes, ExpectedDigest: instruction.Digest})
	if err != nil {
		if ctx.Err() != nil {
			return e.Wrap(e.CodeTimeout, ctx.Err(), "holding acquired Helm package")
		}
		return e.Wrap(e.CodeVerifyFailed, err, "holding acquired Helm package")
	}
	defer func() { _ = held.Close() }()
	if held.Size() != result.Size {
		return e.New(e.CodeVerifyFailed, "acquired Helm package size differs from observed producer")
	}
	raw, err := held.ReadAllContext(ctx, helmarchive.MaxPackageBytes)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "reading held Helm package")
	}
	if _, err := helmarchive.Inspect(ctx, raw); err != nil {
		return err
	}
	_, err = delivery.BuildHelmChartArtifact(ctx, output.Path, output.MediaType, raw)
	return err
}
