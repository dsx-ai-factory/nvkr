package delivery

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/oci"
	"sigs.k8s.io/yaml"
)

const maxHelmChartYAMLBytes = 1 << 20

// helmChartDependency mirrors helm.sh/helm/v3/pkg/chart.Dependency exactly, so
// the config blob is byte-for-byte what a real `helm package`+`helm push` would
// encode for a chart's dependency list.
type helmChartDependency struct {
	Name         string   `json:"name" yaml:"name"`
	Version      string   `json:"version,omitempty" yaml:"version,omitempty"`
	Repository   string   `json:"repository" yaml:"repository"`
	Condition    string   `json:"condition,omitempty" yaml:"condition,omitempty"`
	Tags         []string `json:"tags,omitempty" yaml:"tags,omitempty"`
	Enabled      bool     `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	ImportValues []any    `json:"import-values,omitempty" yaml:"import-values,omitempty"`
	Alias        string   `json:"alias,omitempty" yaml:"alias,omitempty"`
}

// helmChartMaintainer mirrors helm.sh/helm/v3/pkg/chart.Maintainer exactly.
type helmChartMaintainer struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
	URL   string `json:"url,omitempty"`
}

// helmChartConfig mirrors helm.sh/helm/v3/pkg/chart.Metadata field-for-field
// (same names, same json tags, same order), so the config blob this produces is
// exactly what a real `helm package`+`helm push` would encode for the same
// Chart.yaml: every field is a pure, deterministic function of Chart.yaml's own
// bytes, so re-running this on the same chart reproduces the same config blob.
type helmChartConfig struct {
	Name         string                 `json:"name,omitempty"`
	Home         string                 `json:"home,omitempty"`
	Sources      []string               `json:"sources,omitempty"`
	Version      string                 `json:"version,omitempty"`
	Description  string                 `json:"description,omitempty"`
	Keywords     []string               `json:"keywords,omitempty"`
	Maintainers  []*helmChartMaintainer `json:"maintainers,omitempty"`
	Icon         string                 `json:"icon,omitempty"`
	APIVersion   string                 `json:"apiVersion,omitempty"`
	Condition    string                 `json:"condition,omitempty"`
	Tags         string                 `json:"tags,omitempty"`
	AppVersion   string                 `json:"appVersion,omitempty"`
	Deprecated   bool                   `json:"deprecated,omitempty"`
	Annotations  map[string]string      `json:"annotations,omitempty"`
	KubeVersion  string                 `json:"kubeVersion,omitempty"`
	Dependencies []*helmChartDependency `json:"dependencies,omitempty"`
	Type         string                 `json:"type,omitempty"`
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
		Annotations:   helmChartAnnotations(config),
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

// helmChartAnnotations mirrors the subset of Helm's own
// generateChartOCIAnnotations that is a pure function of Chart.yaml bytes: the
// standard org.opencontainers.image.* keys derived from name, version,
// description, homepage and primary source. Helm also sets
// org.opencontainers.image.created from the packaged .tgz's mtime and copies any
// Chart.yaml annotations verbatim; the former is a wall-clock value excluded
// here so the manifest stays a pure function of the chart's own bytes, and the
// latter already round-trips through helmChartConfig.Annotations in the config
// blob without needing a manifest-level copy.
func helmChartAnnotations(config helmChartConfig) map[string]string {
	annotations := map[string]string{}
	add := func(key, value string) {
		if value != "" {
			annotations[key] = value
		}
	}
	add(ocispec.AnnotationTitle, config.Name)
	add(ocispec.AnnotationVersion, config.Version)
	add(ocispec.AnnotationDescription, config.Description)
	add(ocispec.AnnotationURL, config.Home)
	if len(config.Sources) > 0 {
		add(ocispec.AnnotationSource, config.Sources[0])
	}
	if len(annotations) == 0 {
		return nil
	}
	return annotations
}

// helmChartVersionTag derives the registry tag from the chart's own already-
// published config blob (application/vnd.cncf.helm.config.v1+json) and the
// exact manifest digest being tagged, so publication never needs a second,
// separately maintained source of truth for the chart's version, and two
// different chart builds published under the same nominal version can never
// collide on the same mutable tag: the tag itself is a deterministic, pure
// function of the chart's version and its published content. Per ADR-20 a
// registry tag remains a non-authoritative discovery hint only:
// digest-plus-signature verification remains mandatory and unaffected by this
// tag; embedding the digest only makes the hint itself collision-free.
func helmChartVersionTag(ref name.Reference, img v1.Image) (name.Tag, error) {
	raw, err := img.RawConfigFile()
	if err != nil {
		return name.Tag{}, e.Wrap(e.CodeVerifyFailed, err, "reading Helm chart config")
	}
	var config helmChartConfig
	if err := json.Unmarshal(raw, &config); err != nil || config.Version == "" {
		return name.Tag{}, e.New(e.CodeVerifyFailed, "Helm chart config has no version")
	}
	digest, err := img.Digest()
	if err != nil {
		return name.Tag{}, e.Wrap(e.CodeVerifyFailed, err, "digesting Helm chart manifest")
	}
	// A SemVer build-metadata suffix ("1.2.3+build.5") is legal in Chart.yaml but
	// illegal in an OCI tag, as is the "sha256:" digest separator; both would
	// collide with "+" and ":" being outside the OCI tag grammar. Substitute "_"
	// for "+" the way Helm's own OCI client does, and append the manifest
	// digest's hex behind a "-" so the tag stays a single conformant token;
	// config.Version and the digest itself stay exact everywhere else.
	tagVersion := strings.ReplaceAll(config.Version, "+", "_")
	tag, err := name.NewTag(fmt.Sprintf("%s:%s-%s", ref.Context().Name(), tagVersion, digest.Hex), name.StrictValidation)
	if err != nil {
		return name.Tag{}, e.Wrap(e.CodeVerifyFailed, err, "Helm chart version %q is not a valid registry tag", config.Version)
	}
	return tag, nil
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
