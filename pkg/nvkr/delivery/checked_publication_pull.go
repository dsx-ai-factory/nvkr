// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// PullCheckedPublication independently checks every output's complete payload
// closure, one output at a time. Only controls, evidence and executable layers
// survive issuance. Non-executable downloads and extraction scratch are released
// before the next output, without a second retained disk snapshot.
func PullCheckedPublication(ctx context.Context, reference, workspace string, options RegistryOptions) (*CheckedPublication, error) {
	return (&checkedPublicationPull{}).run(ctx, reference, workspace, options)
}

// The acquisition owner keeps its state private until every output and cleanup
// succeeds. Retaining this owner in tests also permits checking failed lifetimes.
type checkedPublicationPull struct{ state *publicationState }

func (pull *checkedPublicationPull) run(ctx context.Context, reference, workspace string, options RegistryOptions) (result *CheckedPublication, err error) {
	if err := deliveryContext(ctx); err != nil {
		return nil, err
	}
	if err := ValidatePublicationReference(reference); err != nil {
		return nil, err
	}
	if err := options.Validate(); err != nil {
		return nil, err
	}
	if _, bounded := ctx.Deadline(); !bounded {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, pullReadTimeout)
		defer cancel()
	}
	if workspace != "" {
		if err := os.MkdirAll(workspace, 0o700); err != nil {
			return nil, e.Wrap(e.CodeImmutableViolation, err, "creating publication check workspace")
		}
	}
	stage, err := os.MkdirTemp(workspace, ".check-publication-*")
	if err != nil {
		return nil, e.Wrap(e.CodeImmutableViolation, err, "creating private publication check stage")
	}
	state := &publicationState{platforms: map[string]publicationPlatform{}, executables: map[string]publicationExecutable{}}
	pull.state = state
	defer func() {
		if cleanupErr := os.RemoveAll(stage); cleanupErr != nil {
			err = errors.Join(err, e.Wrap(e.CodeImmutableViolation, cleanupErr, "removing publication check scratch"))
		}
		if err != nil {
			err = errors.Join(err, state.close())
			result = nil
		}
	}()
	layout := filepath.Join(stage, "metadata")
	inspection, err := PullMetadata(ctx, reference, layout, options)
	if err != nil {
		return nil, err
	}
	state.inspection = inspection
	controls, err := retainCheckedControls(ctx, layout, inspection.RootDigest, state)
	if err != nil {
		return nil, err
	}
	parseOptions := []name.Option{name.StrictValidation}
	if options.PlainHTTP {
		parseOptions = append(parseOptions, name.Insecure)
	}
	ref, err := name.NewDigest(reference, parseOptions...)
	if err != nil {
		return nil, e.Wrap(e.CodeUnverifiedRef, err, "parsing checked publication reference")
	}
	references, err := publicationReferences(ref, inspection.Root, options.PlainHTTP)
	if err != nil {
		return nil, err
	}
	for _, object := range inspection.Root.Objects {
		if object.Output == nil {
			descriptor := Descriptor{MediaType: publishedManifestMediaType(object), Digest: object.DescriptorDigest, Size: object.Size, ArtifactType: object.ArtifactType}
			if err := verifyDescriptorForInspection(ctx, layout, descriptor, map[string]bool{}, nil); err != nil {
				return nil, err
			}
			if err := verifyPublishedObjectBinding(ctx, layout, object, descriptor, nil); err != nil {
				return nil, err
			}
			continue
		}
		child, ok := references[object.Name]
		if !ok {
			return nil, e.New(e.CodeVerifyFailed, "checked output has no exact repository binding")
		}
		if err := checkPublicationOutput(ctx, layout, stage, object, child, controls, state, options); err != nil {
			return nil, err
		}
	}
	sort.Slice(state.outputs, func(i, j int) bool { return state.outputs[i].Name < state.outputs[j].Name })
	content, err := ContentDigest(state.outputs)
	if err != nil {
		return nil, err
	}
	if inspection.Root.Release == nil || content != inspection.Root.Release.ContentSubject {
		return nil, e.New(e.CodeVerifyFailed, "checked publication content differs from complete observed outputs")
	}
	// Revalidate controls after all payload scratch is removed. The retained
	// projection is the original root; no rewritten partial root grants authority.
	final, err := InspectMetadata(ctx, layout)
	if err != nil {
		return nil, err
	}
	if final.RootDigest != inspection.RootDigest {
		return nil, e.New(e.CodeVerifyFailed, "publication controls changed during output checks")
	}
	if err := deliveryContext(ctx); err != nil {
		return nil, err
	}
	state.inspection = inspection
	return &CheckedPublication{state: state}, nil
}

func retainCheckedControls(ctx context.Context, layout, rootDigest string, state *publicationState) (map[string]bool, error) {
	index, err := heldfile.OpenContext(ctx, filepath.Join(layout, "index.json"), heldfile.Options{
		Label: "checked publication index", ExpectedDigest: rootDigest, MaxBytes: 8 << 20,
	})
	if err != nil {
		return nil, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "holding checked publication index"))
	}
	state.files = append(state.files, index)
	entries, err := os.ReadDir(filepath.Join(layout, "blobs", "sha256"))
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "reading checked metadata inventory")
	}
	controls := make(map[string]bool, len(entries))
	for _, entry := range entries {
		digest := "sha256:" + entry.Name()
		if !entry.Type().IsRegular() || !isDigest(digest) {
			return nil, e.New(e.CodeVerifyFailed, "checked metadata contains an invalid inventory member")
		}
		file, err := heldfile.OpenContext(ctx, filepath.Join(layout, "blobs", "sha256", entry.Name()), heldfile.Options{
			Label: "checked publication control", ExpectedDigest: digest, MaxBytes: maxMetadataBytes,
		})
		if err != nil {
			return nil, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "holding publication control"))
		}
		state.files = append(state.files, file)
		controls[digest] = true
	}
	return controls, nil
}

func checkPublicationOutput(ctx context.Context, layout, stage string, object PublishedObject, reference name.Digest,
	controls map[string]bool, state *publicationState, options RegistryOptions,
) error {
	for attempt := 1; attempt <= pullReadAttempts; attempt++ {
		retry, err := checkPublicationOutputAttempt(ctx, layout, stage, object, reference, controls, state, options)
		if err == nil || !retry || attempt == pullReadAttempts {
			return err
		}
		if err := waitForPullRead(ctx, time.Duration(attempt)*time.Second); err != nil {
			return e.Wrap(e.CodeTimeout, err, "waiting to reacquire interrupted checked output")
		}
	}
	return e.New(e.CodeInternal, "checked output acquisition attempt bound exceeded")
}

func checkPublicationOutputAttempt(ctx context.Context, layout, stage string, object PublishedObject, reference name.Digest,
	controls map[string]bool, state *publicationState, options RegistryOptions,
) (retry bool, err error) {
	if err := deliveryContext(ctx); err != nil {
		return false, err
	}
	extraction, err := os.MkdirTemp(stage, "output-*")
	if err != nil {
		return false, e.Wrap(e.CodeImmutableViolation, err, "creating output inspection scratch")
	}
	defer func() {
		cleanupErr := errors.Join(os.RemoveAll(extraction), removeCheckedPayloads(layout, controls))
		if cleanupErr != nil {
			retry = false
			err = errors.Join(err, e.Wrap(e.CodeImmutableViolation, cleanupErr, "releasing checked output scratch"))
		}
	}()
	if err := fetchCheckedOutput(ctx, layout, object, reference, options); err != nil {
		return isInterruptedPullBody(err), err
	}
	descriptor := Descriptor{MediaType: publishedManifestMediaType(object), Digest: object.DescriptorDigest, Size: object.Size, ArtifactType: object.ArtifactType}
	visited, disks := map[string]bool{}, map[string]DiskArtifactInspection{}
	if err := verifyDescriptorForInspection(ctx, layout, descriptor, visited, disks); err != nil {
		return false, err
	}
	if err := verifyPublishedObjectBinding(ctx, layout, object, descriptor, disks); err != nil {
		return false, err
	}
	for digest := range controls {
		visited[digest] = true
	}
	if err := verifyBlobInventory(layout, visited); err != nil {
		return false, err
	}
	// This private selection comes only from the already checked complete root.
	// It selects the existing format inspectors, never a substitute root identity.
	selection := state.inspection
	selection.Root.Objects = []PublishedObject{object}
	outputs, err := inspectOutputs(ctx, layout, selection, extraction, state.platforms, disks)
	if err != nil {
		return false, err
	}
	if object.Output.Format == "executable" {
		manifest, err := flatManifest(ctx, layout, object)
		if err != nil {
			return false, err
		}
		for _, descriptor := range manifest.Layers {
			file, err := holdBlob(ctx, layout, descriptor, maxDeliveryFileBytes)
			if err != nil {
				return false, err
			}
			state.files = append(state.files, file)
		}
		executables, err := retainPublicationExecutables(ctx, layout, selection, state.files)
		if err != nil {
			return false, err
		}
		for output, executable := range executables {
			state.executables[output] = executable
		}
	}
	state.outputs = append(state.outputs, outputs...)
	return false, deliveryContext(ctx)
}

func fetchCheckedOutput(ctx context.Context, layout string, object PublishedObject, reference name.Digest, options RegistryOptions) error {
	remoteOptions := registryOptions(ctx, options)
	if object.Output.ImageIndex != nil {
		index, err := fetchRegistryImageIndex(ctx, reference, object.Size, object.Output.ImageIndex.RequiredPlatforms, remoteOptions)
		if err != nil {
			return err
		}
		importErr := materializeRegistryImageIndex(ctx, layout, object, index)
		return registryImageImportResult(ctx, importErr, index.Close())
	}
	descriptor, err := remote.Get(reference, remoteOptions...)
	if err != nil {
		return metadataFetchError(ctx, err, "fetch checked output manifest")
	}
	if digestBytes(descriptor.Manifest) != object.DescriptorDigest || int64(len(descriptor.Manifest)) != object.Size || string(descriptor.MediaType) != publishedManifestMediaType(object) {
		return e.New(e.CodeVerifyFailed, "registry substituted checked output %q", object.Name)
	}
	image, err := descriptor.Image()
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "opening checked output manifest")
	}
	if err := writePulledImage(ctx, layout, object, image); err != nil {
		if isInterruptedPullBody(err) {
			return e.Wrap(e.CodeUnavailable, err, "materialize interrupted checked output")
		}
		return metadataFetchError(ctx, err, "materialize checked output")
	}
	return nil
}

func removeCheckedPayloads(layout string, controls map[string]bool) error {
	directory := filepath.Join(layout, "blobs", "sha256")
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	var failures []error
	for _, entry := range entries {
		if !controls["sha256:"+entry.Name()] {
			failures = append(failures, os.Remove(filepath.Join(directory, entry.Name())))
		}
	}
	return errors.Join(failures...)
}
