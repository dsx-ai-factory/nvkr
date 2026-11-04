// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

type RestoredPublication struct {
	Reference, RootDigest, Path string
	DiskPayloads                []MirrorDiskPayload
}
type RestoredProof struct {
	Subject               sign.Subject
	Signature, Provenance []byte
}

// RestoredMirror describes checked transport bytes only. Lifecycle must still
// authenticate every original proof, policy, exact inputs and source membership.
type RestoredMirror struct {
	RootReference string
	Publications  []RestoredPublication
	Proofs        []RestoredProof
}

// RestoreMirror checks the bounded transport inventory and every restored byte,
// then reuses OpenPublication's complete OCI graph and payload validation before
// publishing any directories. It never queries a registry or trusts a pathname
// from the inventory; only exact OCI index/blob names reach the existing writer.
func RestoreMirror(ctx context.Context, store ObjectStore, locator StoredObject, out string, limits MirrorLimits) (result *RestoredMirror, returnErr error) {
	if err := deliveryContext(ctx); err != nil {
		return nil, err
	}
	if store == nil || strings.TrimSpace(out) == "" {
		return nil, e.New(e.CodeUsage, "mirror restore requires a store and new destination")
	}
	if err := limits.validate(); err != nil {
		return nil, err
	}
	if !isDigest(locator.SHA256) || locator.Size <= 0 || locator.Size > maxMirrorInventoryBytes || locator.MediaType != "application/json" {
		return nil, e.New(e.CodeVerifyFailed, "mirror inventory locator is not bounded exact JSON")
	}
	if locator.Size > limits.MaxObjectBytes || locator.Size > limits.MaxTotalBytes {
		return nil, e.New(e.CodeEngineCapability, "mirror inventory exceeds independent consumer byte budget")
	}
	var raw bytes.Buffer
	if err := readMirrorObject(ctx, store, locator, &raw); err != nil {
		return nil, err
	}
	if err := canonical.ValidateJSONDocument(raw.Bytes()); err != nil {
		return nil, err
	}
	var header struct {
		SchemaVersion string `json:"schemaVersion"`
	}
	if err := json.Unmarshal(raw.Bytes(), &header); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "reading mirror inventory schema version")
	}
	if header.SchemaVersion != mirrorInventorySchema {
		return nil, e.New(e.CodeVerifyFailed, "unsupported or missing mirror inventory schema version")
	}
	var inventory mirrorInventory
	if err := canonical.UnmarshalExact(raw.Bytes(), &inventory); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "decoding exact mirror transport inventory")
	}
	objects, err := validateMirrorInventory(inventory, locator, limits)
	if err != nil {
		return nil, err
	}
	outer, _, cleanup, err := stageLayout(out)
	if err != nil {
		return nil, e.Wrap(e.CodeImmutableViolation, err, "staging restored original publications")
	}
	defer cleanup()
	stage := filepath.Join(outer, filepath.Base(filepath.Clean(out)))
	if err := os.Mkdir(stage, 0700); err != nil {
		return nil, e.Wrap(e.CodeImmutableViolation, err, "creating private restore container")
	}
	result = &RestoredMirror{RootReference: inventory.RootReference}
	var roots []string
	for _, publication := range inventory.Publications {
		name := strings.TrimPrefix(publication.RootDigest, "sha256:")
		// Several repositories can identify the exact same original layout.
		if !containsMirrorRoot(roots, name) {
			writers := make(map[string]func(io.Writer) error, len(publication.Members))
			var index StoredObject
			for _, member := range publication.Members {
				object := objects[member.SHA256]
				if member.Path == "index.json" {
					index = object
					continue
				}
				writers[member.SHA256] = func(destination io.Writer) error { return readMirrorObject(ctx, store, object, destination) }
			}
			if err := writeHeldLayout(ctx, filepath.Join(stage, name), func(destination io.Writer) error { return readMirrorObject(ctx, store, index, destination) }, writers); err != nil {
				return nil, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "restoring exact mirrored layout bytes"))
			}
			layout := filepath.Join(stage, name)
			disks := map[string]DiskArtifactInspection{}
			checked, err := inspectPublicationWithDisks(ctx, layout, nil, disks)
			if err != nil {
				return nil, err
			}
			if checked.RootDigest != publication.RootDigest || checked.Root.Release == nil {
				return nil, e.New(e.CodeVerifyFailed, "restored publication changed original root")
			}
			inspectionStage, err := os.MkdirTemp(outer, ".inspect-original-*")
			if err != nil {
				return nil, e.Wrap(e.CodeImmutableViolation, err, "creating private original-publication inspection scratch")
			}
			outputs, inspectionErr := inspectOutputs(ctx, layout, checked, inspectionStage, map[string]publicationPlatform{}, disks)
			cleanupErr := os.RemoveAll(inspectionStage)
			if cleanupErr != nil {
				cleanupErr = e.Wrap(e.CodeImmutableViolation, cleanupErr, "cleaning private original-publication inspection scratch")
			}
			if err := errors.Join(inspectionErr, cleanupErr); err != nil {
				return nil, err
			}
			content, err := ContentDigest(outputs)
			if err != nil || content != checked.Root.Release.ContentSubject {
				return nil, e.New(e.CodeVerifyFailed, "restored publication content differs from its complete original closure")
			}
			var rawDisks []MirrorDiskPayload
			for _, output := range outputs {
				if output.Disk != nil {
					rawDisks = append(rawDisks, MirrorDiskPayload{Name: output.Name, SHA256: output.Disk.PayloadDigest, Size: output.Disk.Size, Key: mirrorBlobKey(output.Disk.PayloadDigest)})
				}
			}
			if !reflect.DeepEqual(rawDisks, publication.DiskPayloads) {
				return nil, e.New(e.CodeVerifyFailed, "mirrored disk mappings differ from checked original outputs")
			}
			roots = append(roots, name)
		}
		result.Publications = append(result.Publications, RestoredPublication{Reference: publication.Reference, RootDigest: publication.RootDigest, Path: filepath.Join(out, name), DiskPayloads: append([]MirrorDiskPayload(nil), publication.DiskPayloads...)})
	}
	proofBytes := make(map[string][]byte)
	for _, proof := range inventory.Proofs {
		for _, digest := range []string{proof.Signature, proof.Provenance} {
			if _, exists := proofBytes[digest]; exists {
				continue
			}
			var raw bytes.Buffer
			if err := readMirrorObject(ctx, store, objects[digest], &raw); err != nil {
				return nil, err
			}
			proofBytes[digest] = raw.Bytes()
		}
		result.Proofs = append(result.Proofs, RestoredProof{Subject: proof.Subject.signingSubject(), Signature: proofBytes[proof.Signature], Provenance: proofBytes[proof.Provenance]})
	}
	// Reuse the no-replace descriptor-relative transport publication owner.
	if err := transportarchive.PublishRestoredRoots(ctx, outer, filepath.Dir(filepath.Clean(out)), []string{filepath.Base(filepath.Clean(out))}); err != nil {
		return nil, err
	}
	return result, nil
}

func containsMirrorRoot(roots []string, name string) bool {
	for _, root := range roots {
		if root == name {
			return true
		}
	}
	return false
}

func validateMirrorInventory(inventory mirrorInventory, locator StoredObject, limits MirrorLimits) (map[string]StoredObject, error) {
	if inventory.SchemaVersion != mirrorInventorySchema || len(inventory.Publications) == 0 || len(inventory.Publications) > maxDeliveryObjects || len(inventory.Proofs) == 0 || len(inventory.Proofs) > maxMirrorObjects || len(inventory.Objects)+1 > limits.MaxObjects {
		return nil, e.New(e.CodeVerifyFailed, "mirror inventory schema or complete membership is invalid")
	}
	if err := ValidatePublicationReference(inventory.RootReference); err != nil {
		return nil, err
	}
	rootDigest := strings.SplitN(inventory.RootReference, "@", 2)[1]
	if locator.Key != "publications/"+strings.TrimPrefix(rootDigest, "sha256:")+"/"+strings.TrimPrefix(locator.SHA256, "sha256:")+".json" {
		return nil, e.New(e.CodeVerifyFailed, "mirror inventory locator differs from its original root and exact bytes")
	}
	objects := make(map[string]StoredObject, len(inventory.Objects))
	total := locator.Size
	if locator.Size > limits.MaxObjectBytes || locator.Size > limits.MaxTotalBytes {
		return nil, e.New(e.CodeEngineCapability, "mirror inventory exceeds consumer byte limits")
	}
	previous := ""
	for _, object := range inventory.Objects {
		if !isDigest(object.SHA256) || object.SHA256 <= previous || object.Key != mirrorBlobKey(object.SHA256) || object.Size < 0 || object.MediaType != MediaOctetStream || len(object.VersionID) > 1024 || len(object.ETag) > 1024 {
			return nil, e.New(e.CodeVerifyFailed, "mirror inventory contains invalid or repeated exact objects")
		}
		if object.Size > limits.MaxObjectBytes || object.Size > limits.MaxTotalBytes-total {
			return nil, e.New(e.CodeEngineCapability, "complete restored mirror exceeds consumer byte limits")
		}
		objects[object.SHA256] = object.storedObject()
		total += object.Size
		previous = object.SHA256
	}
	used := map[string]bool{}
	closure := map[string]bool{}
	references := map[string]bool{}
	rootFound := false
	previous = ""
	byRoot := map[string]mirroredPublication{}
	for _, publication := range inventory.Publications {
		if err := ValidatePublicationReference(publication.Reference); err != nil {
			return nil, err
		}
		if publication.Reference <= previous || !strings.HasSuffix(publication.Reference, "@"+publication.RootDigest) || !isDigest(publication.RootDigest) || len(publication.Members) == 0 || len(publication.Members) > maxMirrorObjects {
			return nil, e.New(e.CodeVerifyFailed, "mirror publication has invalid source identity or layout membership")
		}
		references[publication.Reference] = true
		if previousRoot, exists := byRoot[publication.RootDigest]; exists && (!reflect.DeepEqual(previousRoot.Members, publication.Members) || !reflect.DeepEqual(previousRoot.DiskPayloads, publication.DiskPayloads)) {
			return nil, e.New(e.CodeVerifyFailed, "one original layout has conflicting mirror memberships")
		}
		byRoot[publication.RootDigest] = publication
		rootFound = rootFound || publication.Reference == inventory.RootReference
		previous = publication.Reference
		last := ""
		indexFound := false
		for _, member := range publication.Members {
			if member.Path <= last || objects[member.SHA256].Key == "" {
				return nil, e.New(e.CodeVerifyFailed, "mirror layout member is missing or repeated")
			}
			if member.Path == "index.json" {
				if member.SHA256 != publication.RootDigest {
					return nil, e.New(e.CodeVerifyFailed, "mirror layout index substitutes original source")
				}
				indexFound = true
			} else if member.Path != mirrorBlobKey(member.SHA256) {
				return nil, e.New(e.CodeVerifyFailed, "mirror layout contains a noncanonical OCI blob path")
			}
			last = member.Path
			used[member.SHA256] = true
			closure[member.SHA256] = true
		}
		if !indexFound {
			return nil, e.New(e.CodeVerifyFailed, "mirror publication lacks its original index")
		}
		last = ""
		for _, disk := range publication.DiskPayloads {
			object := objects[disk.SHA256]
			if disk.Name <= last || disk.Name == "" || disk.Size <= 0 || object.Key != disk.Key || object.Size != disk.Size || !closure[disk.SHA256] {
				return nil, e.New(e.CodeVerifyFailed, "mirror raw disk mapping differs from original layout")
			}
			last = disk.Name
		}
	}
	if !rootFound {
		return nil, e.New(e.CodeVerifyFailed, "mirror inventory omits its original root")
	}
	previous = ""
	proved := map[string]bool{}
	proofBytes := map[string]bool{}
	var retainedProofBytes int64
	for _, proof := range inventory.Proofs {
		if err := proof.Subject.signingSubject().Validate(); err != nil {
			return nil, err
		}
		if proof.Subject.Ref <= previous || !closure[proof.Subject.Digest] || objects[proof.Signature].Key == "" || objects[proof.Provenance].Key == "" {
			return nil, e.New(e.CodeVerifyFailed, "mirror proof has an absent or repeated original subject")
		}
		proved[proof.Subject.Ref] = true
		for _, digest := range []string{proof.Signature, proof.Provenance} {
			if objects[digest].Size > maxMirrorProofBundleBytes {
				return nil, e.New(e.CodeSignatureInvalid, "original proof bundle exceeds verification bound")
			}
			if !proofBytes[digest] {
				if objects[digest].Size > maxMirrorProofBytes-retainedProofBytes {
					return nil, e.New(e.CodeEngineCapability, "complete original proof inventory exceeds retained-byte bound")
				}
				retainedProofBytes += objects[digest].Size
				proofBytes[digest] = true
			}
		}
		used[proof.Signature] = true
		used[proof.Provenance] = true
		previous = proof.Subject.Ref
	}
	for reference := range references {
		if !proved[reference] {
			return nil, e.New(e.CodeSignatureInvalid, "mirror lacks an original publication proof")
		}
	}
	if len(used) != len(objects) {
		return nil, e.New(e.CodeVerifyFailed, "mirror inventory contains unbound objects")
	}
	return objects, nil
}
