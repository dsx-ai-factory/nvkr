// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package material

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/url"
	"slices"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

const (
	// External capture envelope emitted by the pinned NVKE producer. Repository
	// branding does not rewrite producer-owned bytes; internal identities use NVKR.
	AcquisitionSchemaVersion               = "nkd.dev/package-acquisitions/v1"
	MaxAcquisitionArchiveBytes       int64 = 8 << 30
	maxAcquisitionManifestBytes      int64 = 8 << 20
	maxAcquisitionPackageBytes       int64 = 1 << 30
	maxAcquisitionPackages                 = 16384
	maxAcquisitionRepositoryURIs           = 32
	maxAcquisitionRepositoryURIBytes       = 4096
)

// AcquiredPackage describes one archive retained by an APT pre-install hook.
// RepositoryURIs are locations from repository metadata, not an observation of
// the actual transport, redirect chain or authenticated repository signatures.
// Package metadata remains source-reported until an admitted package reader
// checks the .deb; matching an archive digest cannot establish those fields.
type AcquiredPackage struct {
	Name           string   `json:"name"`
	Version        string   `json:"version"`
	Architecture   string   `json:"architecture"`
	Digest         string   `json:"digest"`
	Size           int64    `json:"size"`
	Path           string   `json:"path"`
	RepositoryURIs []string `json:"repositoryUris"`
}

// AcquisitionManifest is a descriptive package-capture document. It is separate
// from Manifest: producer input bindings cannot model guest package events, and
// this document does not grant source, Run, completeness or acquisition authority.
type AcquisitionManifest struct {
	SchemaVersion string            `json:"schemaVersion"`
	Architecture  string            `json:"architecture"`
	Scope         string            `json:"scope"`
	Packages      []AcquiredPackage `json:"packages"`
}

// AcquisitionObservation records verified archive-to-manifest byte correspondence.
// It deliberately cannot authorize a live Run or assert acquisition origin,
// installed content, complete closure, qualified reproduction or a disk SBOM.
// A nil or zero value is an empty description, never verification authority.
type AcquisitionObservation struct {
	archiveDigest      string
	archiveSize        int64
	manifestBlobSHA256 string
	manifest           AcquisitionManifest
}

// Description returns a deep copy of untrusted source metadata whose referenced
// package bytes were checked. Reloading it does not recreate this observation.
func (observation *AcquisitionObservation) Description() AcquisitionManifest {
	if observation == nil {
		return AcquisitionManifest{}
	}
	result := observation.manifest
	result.Packages = slices.Clone(result.Packages)
	for index := range result.Packages {
		result.Packages[index].RepositoryURIs = slices.Clone(result.Packages[index].RepositoryURIs)
	}
	return result
}

// Binding returns exact archive and manifest-blob digests, not structured
// lifecycle identities or a receipt over acquisition origin.
func (observation *AcquisitionObservation) Binding() (archive string, size int64, manifestBlobSHA256 string) {
	if observation == nil {
		return "", 0, ""
	}
	return observation.archiveDigest, observation.archiveSize, observation.manifestBlobSHA256
}

func (manifest *AcquisitionManifest) validate() error {
	if manifest.SchemaVersion != AcquisitionSchemaVersion {
		return e.New(e.CodeVerifyFailed, "unsupported package acquisition schema")
	}
	if manifest.Scope != "apt-pre-install-pkgs" {
		return e.New(e.CodeVerifyFailed, "unsupported package acquisition scope")
	}
	if manifest.Architecture != "amd64" && manifest.Architecture != "arm64" {
		return e.New(e.CodeVerifyFailed, "unsupported package acquisition architecture")
	}
	if len(manifest.Packages) == 0 || len(manifest.Packages) > maxAcquisitionPackages {
		return e.New(e.CodeVerifyFailed, "package acquisition count outside supported bounds")
	}
	previous := ""
	for _, entry := range manifest.Packages {
		// These are bounded display tokens, not Debian name/version validation.
		// An admitted dpkg reader must check them against the actual package bytes.
		if !canonicalToken(entry.Name, maxMaterialNameBytes) || !canonicalToken(entry.Version, maxMaterialNameBytes) {
			return e.New(e.CodeVerifyFailed, "acquired package has invalid display metadata")
		}
		if entry.Architecture != "all" && entry.Architecture != manifest.Architecture {
			return e.New(e.CodeVerifyFailed, "acquired package architecture differs from capture")
		}
		if !canonical.IsDigest(entry.Digest) {
			return e.New(e.CodeVerifyFailed, "acquired package has invalid blob digest")
		}
		if entry.Digest <= previous {
			return e.New(e.CodeVerifyFailed, "acquired package digests must be strictly sorted and unique")
		}
		if entry.Size <= 0 || entry.Size > maxAcquisitionPackageBytes {
			return e.New(e.CodeVerifyFailed, "acquired package size outside supported bounds")
		}
		if entry.Path != "packages/"+strings.TrimPrefix(entry.Digest, "sha256:")+".deb" {
			return e.New(e.CodeVerifyFailed, "acquired package path is not digest-derived")
		}
		previous = entry.Digest
		if len(entry.RepositoryURIs) == 0 || len(entry.RepositoryURIs) > maxAcquisitionRepositoryURIs {
			return e.New(e.CodeVerifyFailed, "acquired package %q requires bounded repository metadata locations", entry.Name)
		}
		previousURI := ""
		for _, raw := range entry.RepositoryURIs {
			if !canonicalToken(raw, maxAcquisitionRepositoryURIBytes) {
				return e.New(e.CodeVerifyFailed, "acquired package repository location is not bounded display text")
			}
			if raw <= previousURI {
				return e.New(e.CodeVerifyFailed, "acquired package repository locations must be strictly sorted and unique")
			}
			location, err := url.Parse(raw)
			if err != nil {
				// net/url errors can echo the input, including credentials. The raw
				// source description is deliberately not included in diagnostics.
				return e.New(e.CodeVerifyFailed, "parsing acquired package repository location")
			}
			if location.Scheme != "http" && location.Scheme != "https" {
				return e.New(e.CodeVerifyFailed, "acquired package repository location requires HTTP(S)")
			}
			if location.Hostname() == "" {
				return e.New(e.CodeVerifyFailed, "acquired package repository location requires a host")
			}
			if location.User != nil {
				return e.New(e.CodeVerifyFailed, "acquired package repository location contains credentials")
			}
			if location.RawQuery != "" || location.ForceQuery {
				return e.New(e.CodeVerifyFailed, "acquired package repository location contains a query")
			}
			if location.Fragment != "" {
				return e.New(e.CodeVerifyFailed, "acquired package repository location contains a fragment")
			}
			previousURI = raw
		}
	}
	return nil
}

// ObserveAcquisitionArchive verifies a held, bounded uncompressed tar without
// extracting it. manifest.json must be first, followed by every declared package
// exactly once in digest order. A source report or pathname cannot replace the
// held byte capability. The caller retains archive ownership; no live Run or
// source correspondence is established by this diagnostic operation.
func ObserveAcquisitionArchive(ctx context.Context, archive *heldfile.File) (*AcquisitionObservation, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "package acquisition observation requires context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "package acquisition observation canceled")
	}
	if err := validateAcquisitionArchiveSize(archive.Size()); err != nil {
		return nil, err
	}
	if err := archive.AssertUnchangedContext(ctx); err != nil {
		return nil, acquisitionReadError(ctx, err)
	}
	source := acquisitionReader{ctx: ctx, source: io.NewSectionReader(archive, 0, archive.Size())}
	reader := tar.NewReader(source)
	header, err := reader.Next()
	if err != nil {
		return nil, acquisitionReadError(ctx, err)
	}
	if source.offset() != 512 {
		return nil, e.New(e.CodeVerifyFailed, "acquisition archive has hidden headers before manifest")
	}
	if !acquisitionRegular(header) {
		return nil, e.New(e.CodeVerifyFailed, "acquisition manifest requires a regular USTAR member")
	}
	if header.Name != "manifest.json" {
		return nil, e.New(e.CodeVerifyFailed, "acquisition archive requires manifest.json first")
	}
	if header.Size <= 0 || header.Size > maxAcquisitionManifestBytes {
		return nil, e.New(e.CodeVerifyFailed, "acquisition manifest size outside supported bounds")
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		return nil, acquisitionReadError(ctx, err)
	}
	var manifest AcquisitionManifest
	if err := canonical.UnmarshalExact(raw, &manifest); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "decoding package acquisition manifest")
	}
	if err := manifest.validate(); err != nil {
		return nil, err
	}
	nextHeader := int64(512) + acquisitionPaddedSize(header.Size)
	for _, entry := range manifest.Packages {
		header, err = reader.Next()
		if err != nil {
			return nil, acquisitionReadError(ctx, err)
		}
		if source.offset() != nextHeader+512 {
			return nil, e.New(e.CodeVerifyFailed, "acquisition archive has hidden headers before package")
		}
		if !acquisitionRegular(header) {
			return nil, e.New(e.CodeVerifyFailed, "acquired package requires a regular USTAR member")
		}
		if header.Name != entry.Path {
			return nil, e.New(e.CodeVerifyFailed, "acquired package member path differs from manifest")
		}
		if header.Size != entry.Size {
			return nil, e.New(e.CodeVerifyFailed, "acquired package member size differs from manifest")
		}
		hash := sha256.New()
		if _, err := io.Copy(hash, reader); err != nil {
			return nil, acquisitionReadError(ctx, err)
		}
		if "sha256:"+hex.EncodeToString(hash.Sum(nil)) != entry.Digest {
			return nil, e.New(e.CodeVerifyFailed, "acquired package %q differs from its manifest digest", entry.Name)
		}
		nextHeader += 512 + acquisitionPaddedSize(header.Size)
	}
	if _, err := reader.Next(); err != io.EOF {
		if err != nil {
			return nil, acquisitionReadError(ctx, err)
		}
		return nil, e.New(e.CodeVerifyFailed, "acquisition archive has an extra or malformed member")
	}
	if source.offset() != nextHeader+1024 {
		return nil, e.New(e.CodeVerifyFailed, "acquisition archive has hidden extension members or an incomplete terminator")
	}
	// Next may consume exactly two blocks of hidden GNU metadata and then return
	// EOF. Its byte count alone cannot prove the required zero-block terminator.
	var terminator [1024]byte
	footer := acquisitionReader{ctx: ctx, source: io.NewSectionReader(archive, nextHeader, int64(len(terminator)))}
	if _, err := io.ReadFull(footer, terminator[:]); err != nil {
		return nil, acquisitionReadError(ctx, err)
	}
	for _, value := range terminator {
		if value != 0 {
			return nil, e.New(e.CodeVerifyFailed, "acquisition archive has a nonzero terminator")
		}
	}
	// archive/tar owns tar parsing. Its end marker does not consume arbitrary
	// trailing bytes, so reject nonzero trailing data rather than silently hide
	// another parser-visible archive after the terminator. Header slack and member
	// padding remain opaque archive bytes bound by its digest, not package claims.
	buffer := make([]byte, 32<<10)
	for {
		n, err := source.Read(buffer)
		for _, value := range buffer[:n] {
			if value != 0 {
				return nil, e.New(e.CodeVerifyFailed, "acquisition archive has nonzero trailing data")
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, acquisitionReadError(ctx, err)
		}
	}
	if err := archive.AssertUnchangedContext(ctx); err != nil {
		return nil, acquisitionReadError(ctx, err)
	}
	manifestHash := sha256.Sum256(raw)
	return &AcquisitionObservation{archiveDigest: archive.Digest(), archiveSize: archive.Size(),
		manifestBlobSHA256: "sha256:" + hex.EncodeToString(manifestHash[:]), manifest: manifest}, nil
}

// Keep the observer's limit independent of the caller-selected held-file bound.
func validateAcquisitionArchiveSize(size int64) error {
	if size <= 0 || size > MaxAcquisitionArchiveBytes {
		return e.New(e.CodeVerifyFailed, "package acquisition archive is absent or oversized")
	}
	return nil
}

func acquisitionRegular(header *tar.Header) bool {
	return header != nil && header.Typeflag == tar.TypeReg && header.Linkname == "" &&
		header.Format == tar.FormatUSTAR && len(header.PAXRecords) == 0 && header.Size >= 0
}

// archive/tar owns header decoding. Its Next method can consume invisible GNU
// or PAX metadata records, including immediately before EOF. Checking the held
// SectionReader's position enforces one USTAR header per declared member and the
// two-block terminator, without reimplementing the tar header grammar.
func acquisitionPaddedSize(size int64) int64 { return (size + 511) / 512 * 512 }

func acquisitionReadError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return e.Wrap(e.CodeTimeout, ctx.Err(), "package acquisition observation canceled")
	}
	return e.Wrap(e.CodeVerifyFailed, err, "reading held package acquisition archive")
}

type acquisitionReader struct {
	ctx    context.Context
	source *io.SectionReader
}

func (reader acquisitionReader) offset() int64 {
	offset, _ := reader.source.Seek(0, io.SeekCurrent)
	return offset
}

func (reader acquisitionReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.source.Read(buffer)
}
