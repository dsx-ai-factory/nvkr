// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package delivery owns publication bytes, output membership, and content identity.
// BuildPublicationRoot assembles exact artifacts into an OCI layout; OpenPublication
// and PullPublication check and hold its closure for consumption. Close each returned
// Publication after use. ContentDigest gives producers and consumers one canonical
// output identity. Consumer policy and release authorization remain separate checks.
package delivery

import (
	"encoding/hex"
	"sort"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

const (
	BinaryBundleSchemaVersion  = "nvkr.dev/binary-bundle/v1"
	PublicationRootSchema      = "nvkr.dev/publication-root/v4"
	ObjectConfigSchema         = "nvkr.dev/delivery-object/v1"
	QualificationDiagnostic    = "diagnostic"
	QualificationQualified     = "qualified"
	MediaOCIIndex              = "application/vnd.oci.image.index.v1+json"
	MediaOCIManifest           = "application/vnd.oci.image.manifest.v1+json"
	MediaBinaryBundle          = "application/vnd.nvkr.binary-bundle.v1"
	MediaBinaryBundleConfig    = "application/vnd.nvkr.binary-bundle.config.v1+json"
	MediaBinaryBundleLayer     = "application/vnd.nvkr.binary-bundle.layer.v1.tar+gzip"
	MediaPublicationRoot       = "application/vnd.nvkr.publication-root.v4"
	MediaPublicationRootConfig = "application/vnd.nvkr.publication-root.config.v4+json"
	MediaDeliveryObject        = "application/vnd.nvkr.delivery-object.v1"
	MediaDeliveryObjectConfig  = "application/vnd.nvkr.delivery-object.config.v1+json"
	MediaReleaseSet            = "application/vnd.nvkr.release-set.v4+json"
	MediaInventory             = "application/vnd.nvkr.inventory.v4+json"
	MediaOctetStream           = "application/octet-stream"
)

// BinaryEntry is one regular file in an installable binary bundle. SHA256 is the
// ordinary byte digest; InstallPath is an absolute clean deployment suggestion,
// not an instruction that extraction executes.
type BinaryEntry struct {
	Name        string `json:"name"`
	InstallPath string `json:"installPath"`
	Mode        uint32 `json:"mode"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
}

// BinaryBundle is the canonical semantic config carried by a binary-bundle OCI
// artifact. The tar layer must reproduce this exact ordered entry set.
type BinaryBundle struct {
	SchemaVersion    string        `json:"schemaVersion"`
	OS               string        `json:"os"`
	Arch             string        `json:"arch"`
	Purpose          string        `json:"purpose"`
	ReleaseSetDigest string        `json:"releaseSetDigest,omitempty"`
	InventoryDigest  string        `json:"inventoryDigest,omitempty"`
	Entries          []BinaryEntry `json:"entries"`
}

func (bundle BinaryBundle) Validate() error {
	if bundle.SchemaVersion != BinaryBundleSchemaVersion {
		return e.New(e.CodeVerifyFailed, "binary bundle schemaVersion %q is unsupported", bundle.SchemaVersion)
	}
	if !canonicalToken(bundle.OS) || !canonicalToken(bundle.Arch) || !canonicalToken(bundle.Purpose) {
		return e.New(e.CodeInvalidCatalog, "binary bundle os, arch, and purpose must be canonical non-empty values")
	}
	if (bundle.ReleaseSetDigest == "") != (bundle.InventoryDigest == "") {
		return e.New(e.CodeUnverifiedRef, "binary bundle releaseSetDigest and inventoryDigest must be supplied together")
	}
	if bundle.ReleaseSetDigest != "" && (!isDigest(bundle.ReleaseSetDigest) || !isDigest(bundle.InventoryDigest)) {
		return e.New(e.CodeUnverifiedRef, "binary bundle release/inventory identities must be exact sha256 digests")
	}
	if len(bundle.Entries) == 0 {
		return e.New(e.CodeReleaseIncomplete, "binary bundle contains no files")
	}
	previous := ""
	paths := map[string]bool{}
	for _, entry := range bundle.Entries {
		if !canonicalToken(entry.Name) || strings.ContainsAny(entry.Name, "/\\") || entry.Name <= previous || !validInstallPath(entry.InstallPath) ||
			entry.Mode == 0 || entry.Mode > 0o777 || !isDigest(entry.SHA256) || entry.Size < 0 || paths[entry.InstallPath] {
			return e.New(e.CodeVerifyFailed, "binary bundle entries must be sorted, unique, regular-file bindings with exact digests")
		}
		paths[entry.InstallPath] = true
		previous = entry.Name
	}
	return nil
}

func (bundle BinaryBundle) Digest() (string, error) {
	if err := bundle.Validate(); err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindBinaryBundle, bundle)
}

// PublishedObject is one exact child descriptor reachable from a publication
// root. DescriptorDigest identifies its OCI manifest or nested index;
// SemanticDigest identifies the NVKR object or source bytes it represents.
type PublishedObject struct {
	Output           *OutputBinding   `json:"output,omitempty"`
	Evidence         *EvidenceBinding `json:"evidence,omitempty"`
	Name             string           `json:"name"`
	Kind             string           `json:"kind"`
	ArtifactType     string           `json:"artifactType"`
	DescriptorDigest string           `json:"descriptorDigest"`
	SemanticDigest   string           `json:"semanticDigest"`
	BlobDigest       string           `json:"blobDigest"`
	Size             int64            `json:"size"`
	OS               string           `json:"os,omitempty"`
	Arch             string           `json:"arch,omitempty"`
}

// PublicationRoot is the canonical config behind the immutable OCI index that an
// operator pins. A tag may discover the index; it is never part of this identity.
type PublicationRoot struct {
	Release             *ReleaseMetadata  `json:"release,omitempty"`
	SchemaVersion       string            `json:"schemaVersion"`
	Qualification       string            `json:"qualification"`
	PublicationIdentity string            `json:"publicationIdentity,omitempty"`
	PublicationManifest string            `json:"publicationManifestDigest,omitempty"`
	ReleaseSetDigest    string            `json:"releaseSetDigest,omitempty"`
	InventoryDigest     string            `json:"inventoryDigest,omitempty"`
	ClearanceDigest     string            `json:"clearanceDigest,omitempty"`
	Objects             []PublishedObject `json:"objects"`
}

func (root PublicationRoot) Validate() error {
	if err := validateReleaseMetadata(root); err != nil {
		return err
	}
	if root.SchemaVersion != PublicationRootSchema {
		return e.New(e.CodeVerifyFailed, "publication root schemaVersion %q is unsupported", root.SchemaVersion)
	}
	switch root.Qualification {
	case QualificationDiagnostic:
		if root.PublicationIdentity != "" || root.PublicationManifest != "" || root.ClearanceDigest != "" {
			return e.New(e.CodeGateUnmet, "diagnostic publication root may not claim clearance or publication authority")
		}
	case QualificationQualified:
		for field, value := range map[string]string{
			"publicationIdentity": root.PublicationIdentity, "publicationManifestDigest": root.PublicationManifest,
			"releaseSetDigest": root.ReleaseSetDigest, "inventoryDigest": root.InventoryDigest,
			"clearanceDigest": root.ClearanceDigest,
		} {
			if !isDigest(value) {
				return e.New(e.CodeGateUnmet, "qualified publication root has no exact %s", field)
			}
		}
		for _, object := range root.Objects {
			if object.Output != nil && object.Output.Disk != nil && object.Output.Disk.EvidenceMode == sbom.DiskBuildProvenance {
				return e.New(e.CodeGateUnmet, "qualified publication root cannot accept producer-provenance-only disk output %q", object.Name)
			}
		}
	default:
		return e.New(e.CodeVerifyFailed, "publication root qualification %q is unsupported", root.Qualification)
	}
	for field, value := range map[string]string{
		"releaseSetDigest": root.ReleaseSetDigest, "inventoryDigest": root.InventoryDigest,
	} {
		if value != "" && !isDigest(value) {
			return e.New(e.CodeUnverifiedRef, "publication root %s is not an exact digest", field)
		}
	}
	if (root.ReleaseSetDigest == "") != (root.InventoryDigest == "") {
		return e.New(e.CodeUnverifiedRef, "publication root releaseSetDigest and inventoryDigest must be supplied together")
	}
	if len(root.Objects) == 0 {
		return e.New(e.CodeReleaseIncomplete, "publication root contains no delivery objects")
	}
	previous := ""
	seenDescriptors := map[string]bool{}
	for _, object := range root.Objects {
		key := object.Name + "\x00" + object.Kind
		if !canonicalToken(object.Name) || !canonicalToken(object.Kind) || key <= previous ||
			!validMediaType(object.ArtifactType) || !isDigest(object.DescriptorDigest) ||
			!isDigest(object.SemanticDigest) || !isDigest(object.BlobDigest) || object.Size < 0 || seenDescriptors[object.DescriptorDigest] {
			return e.New(e.CodeVerifyFailed, "publication objects must be canonical, sorted, unique, and digest-bound")
		}
		if (object.OS == "") != (object.Arch == "") ||
			(object.OS != "" && (!canonicalToken(object.OS) || !canonicalToken(object.Arch))) {
			return e.New(e.CodeVerifyFailed, "publication object platform must bind os and arch together")
		}
		seenDescriptors[object.DescriptorDigest] = true
		previous = key
	}
	return nil
}

func (root PublicationRoot) Digest() (string, error) {
	if err := root.Validate(); err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindPublicationRoot, root)
}

func sortObjects(objects []PublishedObject) {
	sort.Slice(objects, func(i, j int) bool {
		if objects[i].Name != objects[j].Name {
			return objects[i].Name < objects[j].Name
		}
		return objects[i].Kind < objects[j].Kind
	})
}

func canonicalToken(value string) bool {
	return strings.TrimSpace(value) != "" && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\x00\r\n")
}

func validInstallPath(value string) bool {
	if !strings.HasPrefix(value, "/") || strings.Contains(value, "//") || strings.ContainsAny(value, "\x00\r\n") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(value, "/"), "/")
	if len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func isDigest(value string) bool {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func validMediaType(value string) bool {
	parts := strings.Split(value, "/")
	return len(parts) == 2 && canonicalToken(parts[0]) && canonicalToken(parts[1]) && !strings.ContainsAny(value, " \\;,")
}
