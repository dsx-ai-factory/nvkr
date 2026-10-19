// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/cistore"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/provenance"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

// This closed set selects validators, never permission to upload arbitrary
// contents. Unknown and mixed artifacts continue through private GHCR.
func githubMetadataKind(kind string) bool {
	switch kind {
	case ArtifactPublicationMetadata, ArtifactSigningMetadata, ArtifactPreparedEvidenceMetadata, ArtifactAttestedEvidenceMetadata,
		ArtifactSigningRecord, ArtifactVerificationRecord, ArtifactProvenancePredicate,
		"published-reference", "producer-result", "source-receipt", "sbom-set", string(TierTerminalObservation):
		return true
	default:
		return false
	}
}

// UploadGitHubCarrier owns the public/private policy. Only an exact generated
// Plan member whose held carrier passes its schema and complete inventory can
// enter Actions storage. Invalid metadata fails; it never falls back to upload.
func UploadGitHubCarrier(ctx context.Context, plan *Plan, input cistore.GitHubUpload) (string, error) {
	if err := cistore.ValidateGitHubRequest(ctx, input.Request); err != nil {
		return "", err
	}
	_, nodeJobs, artifacts, err := admitGitHubCarriers(ctx, plan)
	if err != nil {
		return "", err
	}
	for _, id := range sortedKeys(artifacts) {
		artifact := artifacts[id]
		if githubArtifactName(id) != input.Request.Name {
			continue
		}
		if !githubMetadataKind(artifact.Kind) {
			break
		}
		if input.Root != "." || input.Paths != carrierPath(id) || !githubArtifactNeedsCarrier(plan, artifact, nodeJobs) {
			return "", e.New(e.CodePipelineDrift, "metadata upload differs from its exact Plan carrier")
		}
		content, err := admitGitHubMetadata(ctx, artifact, input.Paths)
		if err != nil {
			return "", err
		}
		return content.publish(ctx, input.Request, input.Paths, input.OutputDirectory)
	}
	if err := cistore.ValidateGitHubTransfer(ctx, input.Request, input.Options); err != nil {
		return "", err
	}
	return cistore.UploadGitHub(ctx, input)
}

// Read the held carrier once. Only the validated text inventory is published;
// TAR headers, padding, extra members and executable modes cannot escape via
// an otherwise innocuous metadata filename. Snapshotting these bounded control
// files lets existing layout validators inspect exactly the bytes being exported.
type githubMetadataReceipt struct {
	content cistore.MetadataContent
	member  string
}

func (receipt *githubMetadataReceipt) publish(ctx context.Context, request cistore.Request, member, directory string) (string, error) {
	if receipt == nil || receipt.member != member {
		return "", e.New(e.CodeVerifyFailed, "metadata export requires an admitted snapshot")
	}
	return cistore.PublishGitHubMetadata(ctx, request, member, directory, receipt.content)
}

func admitGitHubMetadata(ctx context.Context, artifact Artifact, carrier string) (*githubMetadataReceipt, error) {
	content, err := snapshotGitHubMetadata(ctx, artifact, carrier)
	if err != nil {
		return nil, err
	}
	return &githubMetadataReceipt{content: content, member: carrier}, nil
}

func snapshotGitHubMetadata(ctx context.Context, artifact Artifact, carrier string) (content cistore.MetadataContent, result error) {
	held, err := heldfile.OpenContext(ctx, carrier, heldfile.Options{Label: "public metadata carrier", MaxBytes: cistore.MaxMetadataRecordBytes})
	if err != nil {
		return content, err
	}
	raw, readErr := held.ReadAllContext(ctx, cistore.MaxMetadataRecordBytes)
	closeErr := held.Close()
	if readErr != nil {
		return content, readErr
	}
	if closeErr != nil {
		return content, e.Wrap(e.CodeVerifyFailed, closeErr, "close metadata carrier")
	}
	content.Root = artifact.Path
	reader := tar.NewReader(bytes.NewReader(raw))
	seen, total := map[string]bool{}, int64(0)
	for {
		if err := githubCarrierContext(ctx); err != nil {
			return content, err
		}
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return content, e.Wrap(e.CodeVerifyFailed, err, "read metadata carrier")
		}
		member := strings.TrimSuffix(header.Name, "/")
		if !securepath.WorkspaceRelative(member) || path.Clean(member) != member || seen[member] || len(seen) >= cistore.MaxMetadataEntries ||
			(member != artifact.Path && !strings.HasPrefix(member, artifact.Path+"/")) || header.Linkname != "" {
			return content, e.New(e.CodeVerifyFailed, "metadata carrier contains an unexpected member")
		}
		seen[member] = true
		entry := cistore.MetadataEntry{Path: member, Mode: header.Mode}
		switch header.Typeflag {
		case tar.TypeDir:
			if header.Size != 0 || (header.Mode != 0o755 && header.Mode != 0o700) {
				return content, e.New(e.CodeVerifyFailed, "metadata directory has unsupported mode or content")
			}
			entry.Directory = true
		case tar.TypeReg:
			total += header.Size
			if header.Size <= 0 || header.Size > 8<<20 || total > cistore.MaxMetadataTextBytes || (header.Mode != 0o644 && header.Mode != 0o600) {
				return content, e.New(e.CodeVerifyFailed, "metadata file exceeds its bound or has executable permissions")
			}
			text, err := io.ReadAll(reader)
			if err != nil {
				return content, e.Wrap(e.CodeVerifyFailed, err, "read metadata document")
			}
			if artifact.Kind != "published-reference" && canonical.ValidateJSONDocument(text) != nil {
				return content, e.New(e.CodeVerifyFailed, "metadata carrier contains a non-JSON document")
			}
			entry.Text = string(text)
		default:
			return content, e.New(e.CodeVerifyFailed, "metadata carrier contains a link or unsupported entry")
		}
		content.Entries = append(content.Entries, entry)
	}
	slices.SortFunc(content.Entries, func(a, b cistore.MetadataEntry) int { return strings.Compare(a.Path, b.Path) })
	if len(content.Entries) == 0 || content.Entries[0].Path != artifact.Path {
		return content, e.New(e.CodeVerifyFailed, "metadata carrier omits its exact root")
	}
	if artifact.Kind != ArtifactPublicationMetadata && artifact.Kind != ArtifactSigningMetadata && artifact.Kind != ArtifactPreparedEvidenceMetadata && artifact.Kind != ArtifactAttestedEvidenceMetadata {
		return content, validateGitHubMetadata(ctx, artifact, "", content)
	}
	temporary, err := os.MkdirTemp("", "nvkr-metadata-admission-*")
	if err != nil {
		return content, e.Wrap(e.CodeBuildFailed, err, "create metadata validation snapshot")
	}
	defer func() {
		if err := os.RemoveAll(temporary); err != nil {
			result = e.Wrap(e.CodeImmutableViolation, err, "remove metadata validation snapshot")
		}
	}()
	snapshot, err := os.OpenRoot(temporary)
	if err != nil {
		return content, e.Wrap(e.CodeBuildFailed, err, "hold metadata validation snapshot")
	}
	defer func() {
		if err := snapshot.Close(); err != nil {
			result = e.Wrap(e.CodeImmutableViolation, err, "close metadata validation snapshot")
		}
	}()
	for _, entry := range content.Entries {
		if err := githubCarrierContext(ctx); err != nil {
			return content, err
		}
		location := filepath.FromSlash(entry.Path)
		if !filepath.IsLocal(location) {
			return content, e.New(e.CodeVerifyFailed, "metadata snapshot member escapes its root")
		}
		if entry.Directory {
			err = snapshot.MkdirAll(location, 0o700)
		} else {
			err = snapshot.MkdirAll(filepath.Dir(location), 0o700)
			if err == nil {
				err = snapshot.WriteFile(location, []byte(entry.Text), 0o600)
			}
		}
		if err != nil {
			return content, e.Wrap(e.CodeBuildFailed, err, "snapshot metadata document")
		}
	}
	return content, validateGitHubMetadata(ctx, artifact, temporary, content)
}

func metadataJSON(text string, target any) error {
	// Generated records permit their single file terminator. Preserve the exact
	// original bytes in transport, especially the predicate's signed newline.
	return canonical.UnmarshalExact([]byte(strings.TrimSuffix(text, "\n")), target)
}

func validateGitHubMetadata(ctx context.Context, artifact Artifact, snapshot string, content cistore.MetadataContent) error {
	root := filepath.Join(snapshot, filepath.FromSlash(artifact.Path))
	switch artifact.Kind {
	case ArtifactPublicationMetadata, ArtifactSigningMetadata, ArtifactPreparedEvidenceMetadata, ArtifactAttestedEvidenceMetadata:
		for _, entry := range content.Entries {
			relative := strings.TrimPrefix(entry.Path, artifact.Path+"/")
			if entry.Directory {
				if entry.Path != artifact.Path && relative != "blobs" && relative != "blobs/sha256" {
					return e.New(e.CodeVerifyFailed, "metadata layout contains an extra directory")
				}
			} else if relative != "index.json" && relative != "oci-layout" && !(strings.HasPrefix(relative, "blobs/sha256/") && canonical.IsDigest("sha256:"+strings.TrimPrefix(relative, "blobs/sha256/"))) {
				return e.New(e.CodeVerifyFailed, "metadata layout contains an extra control file")
			}
		}
		return delivery.ValidatePublicMetadata(ctx, root)
	case "sbom-set":
		return validateGitHubSBOMMetadata(content)
	}
	if len(content.Entries) != 1 || content.Entries[0].Directory {
		return e.New(e.CodeVerifyFailed, "metadata record requires exactly one regular file")
	}
	raw := content.Entries[0].Text
	switch artifact.Kind {
	case ArtifactSigningRecord:
		var record sign.SigningRecord
		if err := metadataJSON(raw, &record); err != nil {
			return err
		}
		return record.Validate()
	case ArtifactVerificationRecord:
		var record sign.VerificationRecord
		if err := metadataJSON(raw, &record); err != nil {
			return err
		}
		return record.Validate()
	case ArtifactProvenancePredicate:
		var record provenance.Predicate
		if err := metadataJSON(raw, &record); err != nil {
			return err
		}
		return provenance.ValidateProduction(record)
	case "published-reference":
		reference := strings.TrimSuffix(raw, "\n")
		if err := delivery.ValidatePublicationReference(reference); err != nil {
			return err
		}
		at := strings.LastIndex(reference, "@")
		if at < 0 {
			return e.New(e.CodeVerifyFailed, "metadata publication reference is not immutable")
		}
		return (sign.Subject{Ref: reference, Digest: reference[at+1:]}).Validate()
	case "producer-result":
		var record pipeline.ArtifactResult
		if err := metadataJSON(raw, &record); err != nil {
			return err
		}
		if record.Component != artifact.Component || !canonical.IsDigest(record.SHA256) || record.Size < 0 {
			return e.New(e.CodeVerifyFailed, "producer metadata differs from the selected component")
		}
		return nil
	case "source-receipt":
		return pipeline.ValidateSourceReceiptMetadata([]byte(strings.TrimSuffix(raw, "\n")))
	case string(TierTerminalObservation):
		var record factoryObservation
		if err := metadataJSON(raw, &record); err != nil {
			return err
		}
		if record.SchemaVersion != "nvkr.dev/factory-observation/v2" || record.BuildTarget != artifact.BuildTarget || !canonical.IsDigest(record.BuildDigest) {
			return e.New(e.CodeVerifyFailed, "observation metadata differs from its selected build")
		}
		for _, input := range record.Inputs {
			if input.ArtifactID == "" || input.Kind == "" || !canonical.IsDigest(input.Digest) {
				return e.New(e.CodeVerifyFailed, "observation metadata has an invalid input")
			}
		}
		return nil
	default:
		return e.New(e.CodeEngineCapability, "artifact has no public metadata validator")
	}
}

func validateGitHubSBOMMetadata(content cistore.MetadataContent) error {
	files := map[string]string{}
	for _, entry := range content.Entries {
		if entry.Directory {
			if entry.Path != content.Root {
				return e.New(e.CodeVerifyFailed, "SBOM metadata contains an extra directory")
			}
			continue
		}
		files[strings.TrimPrefix(entry.Path, content.Root+"/")] = entry.Text
	}
	var index sbom.Index
	if err := metadataJSON(files["index.json"], &index); err != nil {
		return err
	}
	if err := index.Validate(); err != nil {
		return err
	}
	if len(files) != len(index.Entries)+1 {
		return e.New(e.CodeVerifyFailed, "SBOM metadata differs from its complete index")
	}
	for _, entry := range index.Entries {
		var bom sbom.BOM
		if err := metadataJSON(files[entry.Artifact+".cdx.json"], &bom); err != nil {
			return err
		}
		digest, err := bom.Digest()
		if err != nil {
			return err
		}
		if digest != entry.SBOMDigest {
			return e.New(e.CodeVerifyFailed, "SBOM metadata differs from its indexed digest")
		}
	}
	return nil
}
