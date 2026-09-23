package factory

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/transportarchive"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"sigs.k8s.io/yaml"
)

func executeHelmPackage(ctx context.Context, plan *Plan, node Node, output Artifact, inputs []Artifact, artifacts map[string]Artifact, stdout io.Writer) error {
	var imageDigest string
	if node.HelmImage != nil {
		var ids []string
		for _, input := range inputs {
			ids = append(ids, input.ID)
		}
		if err := validatePackageInputs(packageSpec{Kind: PackageHelm, HelmImage: node.HelmImage}, ids, artifacts); err != nil {
			return err
		}
		image, err := inspectFactoryImage(ctx, plan, artifacts[node.HelmImage.Image])
		if err != nil {
			return err
		}
		imageDigest = image.ManifestDigest
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
	archive := filepath.Join(filepath.Dir(output.Path), "."+output.Name+".helm.tar")
	// The transport archive is an internal carrier, not a graph output. Always
	// reclaim it, including after interruption or a previous failed run, so an
	// owned factory workspace is genuinely rerunnable without weakening the
	// archive publisher's no-replace guarantee.
	if err := removeFactoryCarrier(archive); err != nil {
		return err
	}
	defer func() { _ = os.Remove(archive) }()
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
		if err := bindHelmImage(ctx, filepath.Join(root, relative), node.HelmImage.Chart, imageDigest); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Bound Helm chart %q to image %q at %s; checksums refreshed\n", node.HelmImage.Chart, artifacts[node.HelmImage.Image].Name, imageDigest)
		chartPrefix = filepath.Join(relative, node.HelmImage.Chart)
	}
	chartName, err := readHelmChartName(filepath.Join(root, chartPrefix, "Chart.yaml"))
	if err != nil {
		return err
	}
	archive = filepath.Join(root, "bundle.tar")
	if err := transportarchive.Pack(ctx, archive, root, []string{relative}); err != nil {
		return err
	}
	// The Helm OCI chart-content layer is exactly what `helm package` produces: a
	// tar rooted at the chart's own name (Chart.yaml one level in), gzip-
	// compressed with a fixed header so the compressed bytes stay reproducible.
	// The transport archive just packed may carry the chart under a deeper,
	// carrier-specific prefix (chartPrefix); re-root it onto a single
	// <chartName>/ entry instead of writing into the (possibly shared, not
	// node-owned) source tree to relocate it on disk.
	chartArchive := archive + ".tgz"
	if err := removeFactoryCarrier(chartArchive); err != nil {
		return err
	}
	defer func() { _ = os.Remove(chartArchive) }()
	if err := rerootHelmChartLayer(archive, chartArchive, filepath.ToSlash(chartPrefix), chartName); err != nil {
		return err
	}
	chartData, err := os.ReadFile(chartArchive)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "reading packaged Helm chart archive")
	}
	_, err = delivery.BuildHelmChartArtifact(ctx, output.Path, output.MediaType, chartData)
	return err
}

// helmPackageTree normalizes an observed source archive to a deterministic
// carrier. Source archives (e.g. a real `helm package` .tgz) have exactly one
// top-level root, whose name is discovered from the archive itself rather than
// assumed; transport extraction rejects escaping links, traversal, duplicates
// and extra roots.
func helmPackageTree(ctx context.Context, input Artifact) (string, string, func(), error) {
	noop := func() {}
	if input.Kind != "tarball" || input.BuildTarget == "" {
		return "", "", noop, e.New(e.CodeInvalidCatalog, "Helm input %q is not a build source archive", input.ID)
	}
	chartRoot, err := transportarchive.DetectTransportChartRoot(ctx, input.Path)
	if err != nil {
		return "", "", noop, err
	}
	stage, err := os.MkdirTemp("", "nkd-helm-")
	if err != nil {
		return "", "", noop, e.Wrap(e.CodeInternal, err, "staging Helm source archive")
	}
	cleanup := func() { _ = os.RemoveAll(stage) }
	if err := transportarchive.Unpack(ctx, input.Path, stage, []string{chartRoot}); err != nil {
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
func rerootHelmChartLayer(source, destination, prefix, chartName string) error {
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
	// A fixed gzip header keeps the compressed bytes a pure function of content,
	// per this repo's deterministic-output rule.
	gz.ModTime = time.Time{}
	gz.OS = 255
	tw := tar.NewWriter(gz)
	tr := tar.NewReader(in)
	found := false
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
		default:
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
