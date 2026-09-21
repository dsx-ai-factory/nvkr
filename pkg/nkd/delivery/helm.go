package delivery

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/oci"
	"sigs.k8s.io/yaml"
)

const maxHelmChartYAMLBytes = 1 << 20

// helmChartConfig mirrors the Chart.yaml fields Helm's own OCI config blob
// carries. It is re-encoded (not copied verbatim) so the config blob is exactly
// this schema, regardless of what extra fields an upstream Chart.yaml contains.
type helmChartConfig struct {
	APIVersion  string            `json:"apiVersion,omitempty"`
	Name        string            `json:"name"`
	Version     string            `json:"version"`
	AppVersion  string            `json:"appVersion,omitempty"`
	Description string            `json:"description,omitempty"`
	Type        string            `json:"type,omitempty"`
	KubeVersion string            `json:"kubeVersion,omitempty"`
	Keywords    []string          `json:"keywords,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// helmChartMetadata reads the chart's own Chart.yaml out of a gzip-compressed
// chart archive (the same shape `helm package` produces: a tar with exactly one
// top-level directory, holding Chart.yaml at its root) and fails closed if the
// archive is not well-formed or the chart identity is incomplete.
func helmChartMetadata(chartData []byte) (helmChartConfig, error) {
	if len(chartData) < 2 || chartData[0] != 0x1f || chartData[1] != 0x8b {
		return helmChartConfig{}, e.New(e.CodeVerifyFailed, "Helm chart archive is not gzip-compressed")
	}
	gz, err := gzip.NewReader(bytes.NewReader(chartData))
	if err != nil {
		return helmChartConfig{}, e.Wrap(e.CodeVerifyFailed, err, "opening Helm chart archive")
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	root := ""
	var raw []byte
	entries := 0
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return helmChartConfig{}, e.Wrap(e.CodeVerifyFailed, err, "reading Helm chart archive")
		}
		entries++
		if entries > maxDeliveryObjects {
			return helmChartConfig{}, e.New(e.CodeVerifyFailed, "Helm chart archive exceeds %d entries", maxDeliveryObjects)
		}
		name := strings.TrimSuffix(header.Name, "/")
		segments := strings.Split(name, "/")
		switch {
		case len(segments) == 1 && segments[0] != "":
			if root != "" && root != segments[0] {
				return helmChartConfig{}, e.New(e.CodeVerifyFailed, "Helm chart archive has multiple top-level roots")
			}
			root = segments[0]
		case len(segments) == 2 && segments[1] == "Chart.yaml":
			if header.Size < 0 || header.Size > maxHelmChartYAMLBytes {
				return helmChartConfig{}, e.New(e.CodeVerifyFailed, "Helm chart archive Chart.yaml exceeds %d bytes", maxHelmChartYAMLBytes)
			}
			raw = make([]byte, header.Size)
			if _, err := io.ReadFull(tr, raw); err != nil {
				return helmChartConfig{}, e.Wrap(e.CodeVerifyFailed, err, "reading Helm chart Chart.yaml")
			}
		}
	}
	if raw == nil {
		return helmChartConfig{}, e.New(e.CodeVerifyFailed, "Helm chart archive has no Chart.yaml at its root")
	}
	var config helmChartConfig
	if err := yaml.Unmarshal(raw, &config); err != nil {
		return helmChartConfig{}, e.Wrap(e.CodeVerifyFailed, err, "decoding Helm chart Chart.yaml")
	}
	if config.Name == "" || config.Version == "" {
		return helmChartConfig{}, e.New(e.CodeVerifyFailed, "Helm chart Chart.yaml requires a name and version")
	}
	return config, nil
}

// BuildHelmChartArtifact packages chartData — a `helm package`-shaped,
// gzip-compressed chart tarball — as a standards-defined Helm OCI chart artifact:
// config media type application/vnd.cncf.helm.config.v1+json (real Chart.yaml
// metadata) and one layer application/vnd.cncf.helm.chart.content.v1.tar+gzip
// (the chart bytes themselves), exactly what `helm push oci://...` produces. The
// ORAS-go library that Helm's own registry client is built on constructs the
// local OCI Image Layout at out; this function performs no network I/O — the
// separate delivery Push path (registry.go) is solely responsible for
// transporting the committed layout to a registry, so any conformant client
// (stock `helm pull`, `helm install oci://...`, `oras pull`, ...) can read it
// without an NKD-specific decoder. artifactType is NKD's own package identity,
// carried on the manifest alongside the standard fields; Helm's Pull never reads
// a manifest-level artifactType, so this addition is inert to it.
func BuildHelmChartArtifact(ctx context.Context, out, artifactType string, chartData []byte) (Result, error) {
	if err := deliveryContext(ctx); err != nil {
		return Result{}, err
	}
	if artifactType == "" || !validMediaType(artifactType) {
		return Result{}, e.New(e.CodeInvalidCatalog, "Helm chart artifact requires a valid artifact type")
	}
	config, err := helmChartMetadata(chartData)
	if err != nil {
		return Result{}, err
	}
	configData, err := json.Marshal(config)
	if err != nil {
		return Result{}, e.Wrap(e.CodeInternal, err, "encoding Helm chart config")
	}
	stage, commit, cleanup, err := stageLayout(out)
	if err != nil {
		return Result{}, err
	}
	defer cleanup()
	store, err := oci.NewWithContext(ctx, stage)
	if err != nil {
		return Result{}, e.Wrap(e.CodeInternal, err, "opening Helm chart OCI store")
	}
	chartDescriptor, err := oras.PushBytes(ctx, store, MediaHelmChartLayer, chartData)
	if err != nil {
		return Result{}, e.Wrap(e.CodeInternal, err, "storing Helm chart layer")
	}
	configDescriptor, err := oras.PushBytes(ctx, store, MediaHelmConfig, configData)
	if err != nil {
		return Result{}, e.Wrap(e.CodeInternal, err, "storing Helm chart config")
	}
	manifest := ociManifest{
		SchemaVersion: 2,
		MediaType:     MediaOCIManifest,
		ArtifactType:  artifactType,
		Config:        Descriptor{MediaType: configDescriptor.MediaType, Digest: configDescriptor.Digest.String(), Size: configDescriptor.Size},
		Layers:        []Descriptor{{MediaType: chartDescriptor.MediaType, Digest: chartDescriptor.Digest.String(), Size: chartDescriptor.Size}},
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		return Result{}, e.Wrap(e.CodeInternal, err, "encoding Helm chart manifest")
	}
	if _, err := oras.PushBytes(ctx, store, MediaOCIManifest, manifestData); err != nil {
		return Result{}, e.Wrap(e.CodeInternal, err, "storing Helm chart manifest")
	}
	// oras-go's OCI store keeps an empty "ingest" staging directory alongside the
	// committed blobs/index; remove it so the published layout holds only the
	// scaffolding every other layout builder in this package produces.
	if err := removeEmptyLayoutDir(stage, "ingest"); err != nil {
		return Result{}, err
	}
	if _, err := PayloadDescriptor(ctx, stage); err != nil {
		return Result{}, err
	}
	_, indexRaw, err := readIndex(ctx, stage)
	if err != nil {
		return Result{}, err
	}
	if err := commit(); err != nil {
		return Result{}, err
	}
	return Result{LayoutPath: out, RootDigest: digestBytes(indexRaw), SemanticDigest: chartDescriptor.Digest.String()}, nil
}

// helmChartVersionTag derives the registry tag from the chart's own already-
// published config blob (application/vnd.cncf.helm.config.v1+json), so
// publication never needs a second, separately maintained source of truth for
// the chart's version. Per ADR-20 a registry tag is a non-authoritative
// discovery hint only: digest-plus-signature verification remains mandatory
// and unaffected by this tag.
func helmChartVersionTag(ref name.Reference, img v1.Image) (name.Tag, error) {
	raw, err := img.RawConfigFile()
	if err != nil {
		return name.Tag{}, e.Wrap(e.CodeVerifyFailed, err, "reading Helm chart config")
	}
	var config helmChartConfig
	if err := json.Unmarshal(raw, &config); err != nil || config.Version == "" {
		return name.Tag{}, e.New(e.CodeVerifyFailed, "Helm chart config has no version")
	}
	tag, err := name.NewTag(ref.Context().Name()+":"+config.Version, name.StrictValidation)
	if err != nil {
		return name.Tag{}, e.Wrap(e.CodeVerifyFailed, err, "Helm chart version %q is not a valid registry tag", config.Version)
	}
	return tag, nil
}

// HelmChartName reads the chart's own name from an already-packaged local
// Helm-OCI layout's config blob (application/vnd.cncf.helm.config.v1+json), for
// callers that need the chart's published identity after packaging — e.g.
// defaulting its publication repository name to match a real `helm pull
// oci://<registry>/<chart-name>` consumer's expectation.
func HelmChartName(ctx context.Context, root string) (string, error) {
	descriptor, err := PayloadDescriptor(ctx, root)
	if err != nil {
		return "", err
	}
	manifest, err := flatManifest(ctx, root, PublishedObject{DescriptorDigest: descriptor.Digest, Size: descriptor.Size})
	if err != nil {
		return "", err
	}
	if manifest.Config.MediaType != MediaHelmConfig {
		return "", e.New(e.CodeVerifyFailed, "artifact is not a Helm chart")
	}
	raw, err := readBlob(ctx, root, manifest.Config, 8<<20)
	if err != nil {
		return "", err
	}
	var config helmChartConfig
	if err := json.Unmarshal(raw, &config); err != nil || config.Name == "" {
		return "", e.New(e.CodeVerifyFailed, "Helm chart config has no name")
	}
	return config.Name, nil
}

// removeEmptyLayoutDir removes an empty scaffolding directory the OCI store
// writer leaves in the staged layout, failing closed if it unexpectedly holds
// content rather than silently dropping it.
func removeEmptyLayoutDir(stage, name string) error {
	path := filepath.Join(stage, name)
	entries, err := os.ReadDir(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "inspecting Helm chart OCI store staging")
	}
	if len(entries) != 0 {
		return e.New(e.CodeInternal, "Helm chart OCI store staging directory %q is not empty", name)
	}
	if err := os.Remove(path); err != nil {
		return e.Wrap(e.CodeInternal, err, "removing Helm chart OCI store staging directory")
	}
	return nil
}
