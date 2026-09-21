package factory

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/NVIDIA/nke-recipes/internal/transportarchive"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
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
	archive := filepath.Join(filepath.Dir(output.Path), "."+output.Name+".aicr.tar")
	// The transport archive is an internal carrier, not a graph output. Always
	// reclaim it, including after interruption or a previous failed run, so an
	// owned factory workspace is genuinely rerunnable without weakening the
	// archive publisher's no-replace guarantee.
	if err := removeFactoryCarrier(archive); err != nil {
		return err
	}
	defer func() { _ = os.Remove(archive) }()
	root, relative, cleanup, err := helmPackageTree(ctx, plan, inputs[0])
	if err != nil {
		return err
	}
	defer cleanup()
	if node.HelmImage != nil {
		if err := bindHelmImage(ctx, filepath.Join(root, relative), node.HelmImage.Chart, imageDigest); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Bound Helm chart %q to image %q at %s; checksums refreshed\n", node.HelmImage.Chart, artifacts[node.HelmImage.Image].Name, imageDigest)
	}
	if inputs[0].Kind == "tarball" {
		archive = filepath.Join(root, "bundle.tar")
	}
	if err := transportarchive.Pack(ctx, archive, root, []string{relative}); err != nil {
		return err
	}
	digest, err := digestFactoryPath(ctx, archive)
	if err != nil {
		return err
	}
	_, err = delivery.BuildArtifact(ctx, output.Path, delivery.ObjectInput{
		Name: output.Name, Kind: "aicr-bundle", Path: archive,
		ArtifactType: output.MediaType, LayerMediaType: "application/vnd.nkd.aicr-bundle.v1.tar",
		SemanticDigest: digest, BlobDigest: digest,
	})
	return err
}

func lifecycleTransportMember(plan *Plan, observed Artifact) (string, string, error) {
	var canonicalPath string
	for _, artifact := range plan.Graph.Artifacts {
		if artifact.ID == observed.ID {
			canonicalPath = filepath.ToSlash(filepath.Clean(artifact.Path))
			break
		}
	}
	prefix := filepath.ToSlash(filepath.Join(".nkd", "factory")) + "/"
	if canonicalPath == "" || !strings.HasPrefix(canonicalPath, prefix) {
		return "", "", e.New(e.CodePipelineDrift, "package input %q has no canonical factory path", observed.ID)
	}
	relative := strings.TrimPrefix(canonicalPath, prefix)
	observedClean := filepath.Clean(observed.Path)
	relativeNative := filepath.FromSlash(relative)
	if observedClean != relativeNative && !strings.HasSuffix(observedClean, string(filepath.Separator)+relativeNative) {
		return "", "", e.New(e.CodePipelineDrift, "package input %q moved outside its canonical workspace location", observed.ID)
	}
	root := strings.TrimSuffix(observedClean, relativeNative)
	root = strings.TrimSuffix(root, string(filepath.Separator))
	if root == "" {
		root = "."
	}
	return root, relative, nil
}

// helmPackageTree normalizes a generated tree or an observed source archive to
// the same deterministic carrier. Source archives have exactly one helm/ root;
// transport extraction rejects escaping links, traversal, duplicates and extra roots.
func helmPackageTree(ctx context.Context, plan *Plan, input Artifact) (string, string, func(), error) {
	noop := func() {}
	if input.Kind == "aicr-bundle" {
		root, relative, err := lifecycleTransportMember(plan, input)
		return root, relative, noop, err
	}
	if input.Kind != "tarball" || input.BuildTarget == "" {
		return "", "", noop, e.New(e.CodeInvalidCatalog, "Helm input %q is not a build source archive", input.ID)
	}
	stage, err := os.MkdirTemp("", "nkd-helm-")
	if err != nil {
		return "", "", noop, e.Wrap(e.CodeInternal, err, "staging Helm source archive")
	}
	cleanup := func() { _ = os.RemoveAll(stage) }
	if err := transportarchive.Unpack(ctx, input.Path, stage, []string{"helm"}); err != nil {
		cleanup()
		return "", "", noop, err
	}
	info, err := os.Lstat(filepath.Join(stage, "helm"))
	if err != nil || !info.IsDir() {
		cleanup()
		return "", "", noop, e.New(e.CodeVerifyFailed, "Helm source archive requires a helm directory")
	}
	return stage, "helm", cleanup, nil
}
