// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"errors"
	"io"
	"slices"
	"sync"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

// BootAssetBinding describes exact original output metadata and raw bytes. It
// grants no signer, membership, profile acceptance or serving authority.
type BootAssetBinding struct {
	Output           string `json:"output"`
	Kind             string `json:"kind"`
	Platform         string `json:"platform"`
	Member           string `json:"member,omitempty"`
	Profile          string `json:"profile,omitempty"`
	Publication      string `json:"publication"`
	DescriptorDigest string `json:"descriptorDigest"`
	BuildDigest      string `json:"buildDigest"`
	SHA256           string `json:"sha256"`
	Size             int64  `json:"size"`
}

type bootAssetState struct {
	mu          sync.RWMutex
	closed      bool
	binding     BootAssetBinding
	publication *Publication
	member      *heldfile.File
}

// BootAsset lends exact raw bytes, never an OCI wrapper or bundle archive. Disk
// assets borrow their retained output-local Publication, which the caller keeps
// open through use. EFI assets retain an extracted immutable regular member.
// Shallow copies share revocation; Close joins active synchronous callbacks.
type BootAsset struct{ state *bootAssetState }

func (publication *Publication) OpenBootDisk(ctx context.Context, reference string, expected definition.BootDisk) (*BootAsset, error) {
	output, err := publication.bootOutput(ctx, reference, expected.Output, expected.Platform, "disk-image")
	if err != nil {
		return nil, err
	}
	if output.Disk == nil || output.Disk.Format != "qcow2" || output.Disk.Size <= 0 {
		return nil, e.New(e.CodeVerifyFailed, "selected disk has no exact raw QCOW2 binding")
	}
	binding := bootBinding(reference, output)
	binding.SHA256, binding.Size = output.Disk.PayloadDigest, output.Disk.Size
	return &BootAsset{state: &bootAssetState{binding: binding, publication: publication}}, nil
}

func (publication *Publication) OpenBootImager(ctx context.Context, reference string, expected definition.BootImager) (_ *BootAsset, resultErr error) {
	if err := expected.Validate(); err != nil {
		return nil, err
	}
	output, err := publication.bootOutput(ctx, reference, expected.Output, expected.Platform, "executable")
	if err != nil {
		return nil, err
	}
	publication.mu.RLock()
	defer publication.mu.RUnlock()
	if publication.closed {
		return nil, e.New(e.CodeGateUnmet, "imager publication is closed")
	}
	executable, exists := publication.executables[expected.Output]
	if !exists {
		return nil, e.New(e.CodeVerifyFailed, "selected imager has no retained binary bundle")
	}
	entry, err := executable.bundle.BootMember(expected)
	if err != nil {
		return nil, err
	}
	var member *heldfile.File
	defer func() {
		if resultErr != nil && member != nil {
			resultErr = errors.Join(resultErr, member.Close())
		}
	}()
	if err := walkBundleLayer(ctx, executable.layer, executable.bundle, func(candidate BinaryEntry, source io.Reader) error {
		if candidate.Name != expected.Member {
			_, err := io.Copy(io.Discard, source)
			return err
		}
		if member != nil {
			return e.New(e.CodeVerifyFailed, "selected imager member is ambiguous")
		}
		var err error
		member, err = heldfile.FromReaderContext(ctx, source, heldfile.Options{Label: "selected imager", ExpectedDigest: entry.SHA256, MaxBytes: entry.Size})
		if err != nil {
			return classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "retaining raw imager bytes"))
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if member == nil || member.Size() != entry.Size {
		return nil, e.New(e.CodeVerifyFailed, "selected imager member is missing or truncated")
	}
	binding := bootBinding(reference, output)
	binding.Member, binding.Profile = expected.Member, expected.Profile
	binding.SHA256, binding.Size = member.Digest(), member.Size()
	return &BootAsset{state: &bootAssetState{binding: binding, member: member}}, nil
}

func (publication *Publication) bootOutput(ctx context.Context, reference, name, platform, kind string) (PublicationOutput, error) {
	if err := deliveryContext(ctx); err != nil {
		return PublicationOutput{}, err
	}
	if err := ValidatePublicationReference(reference); err != nil {
		return PublicationOutput{}, err
	}
	description, err := publication.Description(reference)
	if err != nil {
		return PublicationOutput{}, err
	}
	if len(description.Outputs) != 1 {
		return PublicationOutput{}, e.New(e.CodeVerifyFailed, "selected boot source must be an exact output-local publication")
	}
	output := description.Outputs[0]
	if output.Name != name || output.Kind != kind || output.OS+"/"+output.Arch != platform {
		return PublicationOutput{}, e.New(e.CodeVerifyFailed, "selected boot source differs from the exact named output, kind or platform")
	}
	return output, nil
}

func bootBinding(reference string, output PublicationOutput) BootAssetBinding {
	return BootAssetBinding{Output: output.Name, Kind: output.Kind, Platform: output.OS + "/" + output.Arch,
		Publication: reference, DescriptorDigest: output.DescriptorDigest, BuildDigest: output.BuildDigest}
}

func (asset *BootAsset) Binding() (BootAssetBinding, error) {
	if asset == nil || asset.state == nil {
		return BootAssetBinding{}, e.New(e.CodeGateUnmet, "boot asset was not issued")
	}
	asset.state.mu.RLock()
	defer asset.state.mu.RUnlock()
	if asset.state.closed {
		return BootAssetBinding{}, e.New(e.CodeGateUnmet, "boot asset is closed")
	}
	if publication := asset.state.publication; publication != nil {
		publication.mu.RLock()
		defer publication.mu.RUnlock()
		if publication.closed {
			return BootAssetBinding{}, e.New(e.CodeGateUnmet, "borrowed boot disk publication is closed")
		}
	}
	return asset.state.binding, nil
}

// Visit lends the raw file under a callback-scoped read lease. The caller owns
// bounded writes and must not reenter or close this asset or its source owner.
func (asset *BootAsset) Visit(ctx context.Context, visit func(context.Context, BootAssetBinding, io.ReadSeeker) error) error {
	if err := deliveryContext(ctx); err != nil {
		return err
	}
	if asset == nil || asset.state == nil {
		return e.New(e.CodeGateUnmet, "boot asset was not issued")
	}
	if visit == nil {
		return e.New(e.CodeUsage, "boot asset requires a visitor")
	}
	state := asset.state
	state.mu.RLock()
	defer state.mu.RUnlock()
	if state.closed {
		return e.New(e.CodeGateUnmet, "boot asset is closed")
	}
	if state.publication != nil {
		seen := false
		err := state.publication.VisitObjects(ctx, func(ctx context.Context, object PublicationObject, source io.ReadSeeker) error {
			if !slices.Contains(object.DiskOutputs, state.binding.Output) {
				return nil
			}
			if seen || object.SHA256 != state.binding.SHA256 || object.Size != state.binding.Size {
				return e.New(e.CodeVerifyFailed, "retained disk differs from the selected raw binding")
			}
			seen = true
			return visit(ctx, state.binding, source)
		})
		if err != nil {
			return err
		}
		if !seen {
			return e.New(e.CodeVerifyFailed, "selected disk has no retained raw bytes")
		}
		return nil
	}
	if state.member == nil {
		return e.New(e.CodeGateUnmet, "boot asset has no raw bytes")
	}
	source := publicationObjectSource{
		description: PublicationObject{SHA256: state.binding.SHA256, Size: state.binding.Size, MediaType: MediaOctetStream},
		source:      state.member,
		check:       func() error { return checkPublicationObjectSize(ctx, state.member.OSFile(), state.binding.Size) },
	}
	if err := source.check(); err != nil {
		return err
	}
	err := visitPublicationObject(ctx, source, func(ctx context.Context, _ PublicationObject, reader io.ReadSeeker) error {
		return visit(ctx, state.binding, reader)
	})
	var coded *e.Error
	if err != nil && !errors.As(err, &coded) {
		err = e.Wrap(e.CodeVerifyFailed, err, "visiting raw imager bytes")
	}
	return classifyContext(ctx, err)
}

func (asset *BootAsset) Close() error {
	if asset == nil || asset.state == nil {
		return nil
	}
	state := asset.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return nil
	}
	state.closed = true
	if state.member != nil {
		if err := state.member.Close(); err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "closing raw imager bytes")
		}
	}
	return nil
}
