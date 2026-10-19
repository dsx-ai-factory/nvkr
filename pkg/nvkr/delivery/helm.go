// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/versionpolicy"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/oci"
	"sigs.k8s.io/yaml"
)

const maxHelmChartYAMLBytes = 1 << 20

// maxHelmChartEntries bounds Chart.yaml discovery against the same 1,000,000-
// entry ceiling the transport layer already enforces on this exact content
// (internal/transportarchive.maxTransportEntries), rather than the much
// tighter maxDeliveryObjects (4096) — a bound sized for publication-object
// counts, unrelated to how many files one chart archive may legitimately
// contain (e.g. a CRD-heavy umbrella chart).
const maxHelmChartEntries = 1_000_000

// maxHelmChartDecompressedBytes bounds the aggregate decompressed size
// helmChartMetadata reads while scanning a chart archive for Chart.yaml, the
// same defense internal/transportarchive.transportLimitReader applies to its
// own gzip lane. helmChartMetadata's sole caller today (BuildHelmChartArtifact)
// feeds it NVKR-produced, already size-checked data, but both are exported, so
// this bound does not depend on caller provenance to stop a small
// decompression bomb from expanding without limit.
const maxHelmChartDecompressedBytes = 512 << 20

// boundedGzipReader fails closed once a decompressed gzip stream has yielded
// more than its byte budget, mirroring
// internal/transportarchive.transportLimitReader: the boundary is confirmed
// with a one-byte probe read of the underlying stream, not merely inferred
// from the counter reaching zero, so a maximum-size but otherwise legal
// stream is not rejected.
type boundedGzipReader struct {
	reader    io.Reader
	remaining int64
}

func (b *boundedGzipReader) Read(p []byte) (int, error) {
	if b.remaining <= 0 {
		probe := make([]byte, 1)
		n, err := b.reader.Read(probe)
		if n > 0 {
			return 0, fmt.Errorf("helm chart archive exceeds its %d-byte decompressed bound", maxHelmChartDecompressedBytes)
		}
		return 0, err
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.reader.Read(p)
	b.remaining -= int64(n)
	return n, err
}

// helmChartValidAPIVersions are the two Chart.yaml apiVersion values Helm's
// own loader accepts: "v1" (legacy Helm 2 chart, still loadable by Helm 3)
// and "v2" (the current chart API). Any other value is rejected by the real
// Helm loader before a chart is packaged or pushed.
var helmChartValidAPIVersions = map[string]bool{"v1": true, "v2": true}

// helmChartDependency mirrors the field names, JSON tags and order of
// helm.sh/helm/v3/pkg/chart.Dependency (helm.sh/helm is not imported here;
// this is a hand-kept structural mirror, not a binary comparison against a
// real `helm package` output).
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

// helmChartConfig mirrors the field names, JSON tags and order of
// helm.sh/helm/v3/pkg/chart.Metadata (a hand-kept structural mirror;
// helm.sh/helm is not imported here, and no test drives a real `helm
// package`+`helm push` to confirm byte-identical output). Every field is a
// pure, deterministic function of Chart.yaml's own bytes, so re-running this
// on the same chart reproduces the same config blob.
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
	tr := tar.NewReader(&boundedGzipReader{reader: gz, remaining: maxHelmChartDecompressedBytes})
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
		if entries > maxHelmChartEntries {
			return helmChartConfig{}, e.New(e.CodeVerifyFailed, "Helm chart archive exceeds %d entries", maxHelmChartEntries)
		}
		name := strings.TrimSuffix(header.Name, "/")
		segments := strings.Split(name, "/")
		if segments[0] == "" {
			continue
		}
		// Track the declared root from every entry's own first path segment,
		// not only entries that happen to carry an explicit top-level
		// directory tar header: some writers omit directory entries and emit
		// only file entries, and a second root's Chart.yaml must still be
		// caught as a conflict rather than silently overwriting the first.
		if root == "" {
			root = segments[0]
		} else if root != segments[0] {
			return helmChartConfig{}, e.New(e.CodeVerifyFailed, "Helm chart archive has multiple top-level roots")
		}
		if len(segments) == 2 && segments[1] == "Chart.yaml" && header.Typeflag == tar.TypeReg {
			if raw != nil {
				return helmChartConfig{}, e.New(e.CodeVerifyFailed, "Helm chart archive has multiple Chart.yaml entries")
			}
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
	if err := validateHelmChartMetadata(config); err != nil {
		return helmChartConfig{}, err
	}
	return config, nil
}

// validateHelmChartMetadata applies the same authoritative rules Helm's own
// chart.Metadata.Validate enforces before a chart can be loaded or pushed:
// apiVersion, name and version are required, apiVersion must be a value
// Helm's loader accepts, type (when set) must be a value Helm's loader
// accepts, and version must satisfy Helm-compatible SemVer. Accepting a Chart.yaml that
// fails these rules would package it under the standard Helm media types
// anyway, deferring the real failure to whatever consumer later runs the
// authoritative Helm loader against it.
func validateHelmChartMetadata(config helmChartConfig) error {
	if config.Name == "" {
		return e.New(e.CodeVerifyFailed, "Helm chart Chart.yaml requires a name")
	}
	if config.Version == "" {
		return e.New(e.CodeVerifyFailed, "Helm chart Chart.yaml requires a version")
	}
	if !helmChartValidAPIVersions[config.APIVersion] {
		return e.New(e.CodeVerifyFailed, "Helm chart Chart.yaml apiVersion %q must be %q or %q", config.APIVersion, "v1", "v2")
	}
	if config.Type != "" && config.Type != "application" && config.Type != "library" {
		return e.New(e.CodeVerifyFailed, "Helm chart Chart.yaml type %q must be %q or %q", config.Type, "application", "library")
	}
	if !versionpolicy.Helm(config.Version) {
		return e.New(e.CodeVerifyFailed, "Helm chart Chart.yaml version %q is not a valid SemVer", config.Version)
	}
	return nil
}

// BuildHelmChartArtifact packages chartData — a `helm package`-shaped,
// gzip-compressed chart tarball — as a structurally standards-conformant Helm
// OCI chart artifact: config media type application/vnd.cncf.helm.config.v1+json
// (real Chart.yaml metadata) and one layer
// application/vnd.cncf.helm.chart.content.v1.tar+gzip (the chart bytes
// themselves), matching what `helm push oci://...` documents producing. The
// ORAS-go library that Helm's own registry client is built on constructs the
// local OCI Image Layout at out; this function performs no network I/O — the
// separate delivery Push path (registry.go) is solely responsible for
// transporting the committed layout to a registry. This is contract-tested
// against generic OCI-layout readers (go-containerregistry, oras-go); reading
// the produced layout with an unmodified `helm pull`/`helm show`/`oras pull`
// binary is not yet externally exercised. artifactType is NVKR's own package
// identity, carried on the manifest alongside the standard fields; Helm's Pull
// never reads a manifest-level artifactType, so this addition is inert to it.
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

// helmChartTags derives two registry tags from the chart's own already-
// published config blob (application/vnd.cncf.helm.config.v1+json), so
// publication never needs a second, separately maintained source of truth for
// the chart's version. Per ADR-20 a registry tag remains a non-authoritative
// discovery hint only: digest-plus-signature verification remains mandatory
// and unaffected by either tag.
//
// discovery is the plain `<version>` tag real Helm itself pushes, so an
// unmodified `helm show/pull --version <version>` resolves it; like any
// mutable Helm tag it can be re-pointed by a later push under the same
// nominal version.
//
// content additionally embeds the exact manifest digest
// (`<version>-<digest>`), so two different chart builds published under the
// same nominal version can never collide on the same mutable tag: it is a
// deterministic, pure function of the chart's version and its published
// content, safe to treat as immutable and skip re-pushing when it already
// resolves to the same digest.
//
// options extends tag parsing (e.g. name.Insecure for a plain-HTTP registry);
// callers must pass the same transport options used to publish ref itself.
func helmChartTags(ref name.Reference, img v1.Image, options ...name.Option) (discovery, content name.Tag, err error) {
	raw, err := img.RawConfigFile()
	if err != nil {
		return name.Tag{}, name.Tag{}, e.Wrap(e.CodeVerifyFailed, err, "reading Helm chart config")
	}
	var config helmChartConfig
	if err := json.Unmarshal(raw, &config); err != nil || config.Version == "" {
		return name.Tag{}, name.Tag{}, e.New(e.CodeVerifyFailed, "Helm chart config has no version")
	}
	digest, err := img.Digest()
	if err != nil {
		return name.Tag{}, name.Tag{}, e.Wrap(e.CodeVerifyFailed, err, "digesting Helm chart manifest")
	}
	// A SemVer build-metadata suffix ("1.2.3+build.5") is legal in Chart.yaml but
	// illegal in an OCI tag, as is the "sha256:" digest separator; both would
	// collide with "+" and ":" being outside the OCI tag grammar. Substitute "_"
	// for "+" the way Helm's own OCI client does, and append the manifest
	// digest's hex behind a "-" for the content tag so it stays a single
	// conformant token; config.Version and the digest itself stay exact
	// everywhere else.
	tagVersion := strings.ReplaceAll(config.Version, "+", "_")
	parseOptions := append([]name.Option{name.StrictValidation}, options...)
	// SemVer places no length ceiling on config.Version, but the OCI tag
	// grammar caps a whole tag at maxOCITagLength; boundedHelmTagToken keeps
	// an over-length version from making name.NewTag fail below by
	// deterministically shortening it to a collision-resistant token that
	// still fits, rather than leaving publication of that chart permanently
	// impossible. The content tag additionally reserves room for the "-" and
	// the manifest digest's 64 hex characters.
	discoveryToken := boundedHelmTagToken(tagVersion, maxOCITagLength)
	discovery, err = name.NewTag(fmt.Sprintf("%s:%s", ref.Context().Name(), discoveryToken), parseOptions...)
	if err != nil {
		return name.Tag{}, name.Tag{}, e.Wrap(e.CodeVerifyFailed, err, "Helm chart version %q is not a valid registry tag", config.Version)
	}
	contentToken := boundedHelmTagToken(tagVersion, maxOCITagLength-1-len(digest.Hex))
	content, err = name.NewTag(fmt.Sprintf("%s:%s-%s", ref.Context().Name(), contentToken, digest.Hex), parseOptions...)
	if err != nil {
		return name.Tag{}, name.Tag{}, e.Wrap(e.CodeVerifyFailed, err, "Helm chart version %q is not a valid registry tag", config.Version)
	}
	return discovery, content, nil
}

// maxOCITagLength is the OCI/Distribution tag grammar's maximum length:
// [a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}, 128 characters total.
const maxOCITagLength = 128

// boundedHelmTagToken returns token unchanged when it already fits within
// budget characters. Otherwise it deterministically shortens token to a
// collision-resistant form that fits: a truncated prefix plus a short hex
// digest of the full token, so two different over-length tokens that share
// the same truncated prefix cannot collide on the same shortened tag.
func boundedHelmTagToken(token string, budget int) string {
	if len(token) <= budget {
		return token
	}
	sum := sha256.Sum256([]byte(token))
	suffix := fmt.Sprintf("-%x", sum[:8])
	prefix := budget - len(suffix)
	if prefix < 0 {
		prefix = 0
	}
	return token[:prefix] + suffix
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
