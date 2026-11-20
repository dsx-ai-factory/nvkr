// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// CompleteStagedDisk publishes attested controls over an already staged disk.
// The preparation is read back by digest; only evidence and the release root
// are written. This establishes byte integrity, not signer or release authority.
// The ordinary independent publication verifier must still check the payload.
func CompleteStagedDisk(ctx context.Context, reference, prepared, attested, expectedAttestedRoot string, options RegistryOptions,
	verify func(context.Context, string, []byte) error,
) (published string, err error) {
	defer attestationBoundary(ctx, &err, "completing staged disk")
	if err := deliveryContext(ctx); err != nil {
		return "", err
	}
	if verify == nil {
		return "", e.New(e.CodeEngineCapability, "staged completion requires SBOM verification")
	}
	if err := options.Validate(); err != nil {
		return "", err
	}
	if !canonical.IsDigest(expectedAttestedRoot) {
		return "", e.New(e.CodeUsage, "staged completion requires an exact attested root")
	}
	ctx, cancel := registryPublicationContext(ctx, 20*time.Minute)
	defer cancel()
	defer func() {
		if err != nil && registryWriteAttempted(ctx) {
			err = uncertainRegistryPublication(err)
		}
	}()
	parse := []name.Option{name.StrictValidation}
	if options.PlainHTTP {
		parse = append(parse, name.Insecure)
	}
	ref, err := name.NewDigest(reference, parse...)
	if err != nil || ref.Name() != reference {
		return "", e.New(e.CodeUnpinnedSource, "staged disk requires a canonical digest reference")
	}
	readback, err := readStagedDisk(ctx, ref, prepared, options)
	if err != nil {
		return "", err
	}
	original := readback.inspection
	signed, err := InspectMetadata(ctx, attested)
	if err != nil {
		return "", err
	}
	if signed.RootDigest != expectedAttestedRoot {
		return "", e.New(e.CodeVerifyFailed, "attested root changed after admission")
	}
	if err := unchangedAttestationBindings(original.Root, signed.Root); err != nil {
		return "", err
	}
	// Verify every bundle before the first write, including exact original SBOM bytes.
	for i, object := range original.Root.Objects {
		if object.Evidence != nil {
			if err := verifyCompletionEvidence(ctx, prepared, attested, object, signed.Root.Objects[i], verify); err != nil {
				return "", err
			}
		}
	}
	refs, err := publicationReferences(ref.Context().Digest(signed.RootDigest), signed.Root, options.PlainHTTP)
	if err != nil {
		return "", err
	}
	for _, object := range signed.Root.Objects {
		if object.Evidence == nil {
			continue
		}
		image, cleanup, err := outputImage(ctx, attested, object)
		if err != nil {
			return "", err
		}
		writeErr := remote.Write(refs[object.Name], image, registryOptions(ctx, options)...)
		cleanup()
		if writeErr != nil {
			return "", registryPublishError(ctx, writeErr, "publishing staged disk evidence")
		}
	}
	return PushMetadataWithOptions(ctx, attested, ref.Context().Name(), PushOptions{Registry: options, ExpectedRootDigest: signed.RootDigest})
}

// This process-local receipt is issued only after all served controls, evidence
// and the exact disk payload have passed readback. It is never serialized or
// accepted from the factory; the post-sign verifier repeats its own full read.
type stagedDiskReadback struct{ inspection Inspection }

func readStagedDisk(ctx context.Context, ref name.Digest, controls string, options RegistryOptions) (*stagedDiskReadback, error) {
	inspection, err := PullMetadata(ctx, ref.Name(), controls, options)
	if err != nil {
		return nil, err
	}
	if err := requireSinglePreparedDisk(inspection); err != nil {
		return nil, err
	}
	refs, err := publicationReferences(ref, inspection.Root, options.PlainHTTP)
	if err != nil {
		return nil, err
	}
	for _, object := range inspection.Root.Objects {
		if object.Output == nil {
			continue
		}
		// PullMetadata already authenticated the wrapper, config, platform,
		// payload descriptor and evidence bindings. Stream that exact layer;
		// retain neither a payload-sized buffer nor a local payload file.
		disk := object.Output.Disk
		layer, err := remote.Layer(refs[object.Name].Context().Digest(disk.PayloadDigest), registryOptions(ctx, options)...)
		if err != nil {
			return nil, registryReadbackError(ctx, err, "fetching staged disk payload")
		}
		body, err := layer.Compressed()
		if err != nil {
			return nil, registryReadbackError(ctx, err, "opening staged disk payload")
		}
		hash := sha256.New()
		size, copyErr := io.CopyBuffer(hash, io.LimitReader(body, disk.Size+1), make([]byte, 128<<10))
		readErr := errors.Join(copyErr, body.Close())
		if err := deliveryContext(ctx); err != nil {
			return nil, err
		}
		if size != disk.Size || "sha256:"+hex.EncodeToString(hash.Sum(nil)) != disk.PayloadDigest {
			return nil, e.Wrap(e.CodeVerifyFailed, readErr, "served staged disk differs from its exact size or digest")
		}
		if readErr != nil {
			return nil, registryReadbackError(ctx, readErr, "reading staged disk payload")
		}
	}
	return &stagedDiskReadback{inspection: inspection}, nil
}

func requireSinglePreparedDisk(inspection Inspection) error {
	if err := requirePreparedEvidence(inspection.Root); err != nil {
		return err
	}
	outputs := 0
	for _, object := range inspection.Root.Objects {
		if object.Output != nil {
			outputs++
			if object.Output.Format != "disk-image" || object.Output.Publication != nil {
				return e.New(e.CodeEngineCapability, "staging requires one local disk output")
			}
		} else if object.Evidence == nil {
			return e.New(e.CodeEngineCapability, "staging contains an unrelated object")
		}
	}
	if outputs != 1 {
		return e.New(e.CodeEngineCapability, "staging requires exactly one disk output")
	}
	return nil
}

// StagePreparedDisk uploads an unsigned preparation without creating tags.
// Its returned reference remains a description until independently read back.
func StagePreparedDisk(ctx context.Context, source, repository, expectedRoot string, options RegistryOptions) (string, error) {
	if err := deliveryContext(ctx); err != nil {
		return "", err
	}
	if err := options.Validate(); err != nil {
		return "", err
	}
	if !canonical.IsDigest(expectedRoot) {
		return "", e.New(e.CodeUsage, "disk staging requires an exact preparation root")
	}
	if strings.ContainsAny(repository[strings.LastIndex(repository, "/")+1:], ":@") {
		return "", e.New(e.CodeUsage, "disk staging requires an untagged repository")
	}
	parse := []name.Option{name.StrictValidation}
	if options.PlainHTTP {
		parse = append(parse, name.Insecure)
	}
	repo, err := name.NewRepository(repository, parse...)
	if err != nil || repo.Name() != repository {
		return "", e.New(e.CodeUsage, "disk staging requires a canonical repository")
	}
	inspection, err := Inspect(ctx, source)
	if err != nil {
		return "", err
	}
	if inspection.RootDigest != expectedRoot {
		return "", e.New(e.CodeVerifyFailed, "disk preparation changed after admission")
	}
	if err := requireSinglePreparedDisk(inspection); err != nil {
		return "", err
	}
	return PushWithOptions(ctx, source, repository, PushOptions{Registry: options, ExpectedRootDigest: expectedRoot})
}
