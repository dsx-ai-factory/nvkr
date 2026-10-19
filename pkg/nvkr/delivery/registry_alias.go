// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"errors"
	"net/http"
	"os"
	"sort"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/discoverytags"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// PushOptions keeps publication mutations separate from transport options used
// by read-only registry operations. ImageTags maps exact aggregate output names
// to literal discovery tags in their existing sibling repositories. There is no
// inferred version, signature authority, multi-tag transaction or compare-and-swap.
type PushOptions struct {
	Registry  RegistryOptions
	ImageTags map[string]string
	// ExpectedRootDigest binds a prior metadata admission to the actual push.
	// Empty retains the ordinary standalone publication contract.
	ExpectedRootDigest string
}

// Validate admits transport credentials and tag syntax before local or remote I/O.
func (options PushOptions) Validate() error {
	if err := validateRegistryOptions(options.Registry); err != nil {
		return err
	}
	if options.ExpectedRootDigest != "" && !isDigest(options.ExpectedRootDigest) {
		return e.New(e.CodeUsage, "publication expected root must be an exact SHA-256 digest")
	}
	return ValidateImageTags(options.ImageTags)
}

// ValidateImageTags admits names and literal tag syntax without I/O. Publication
// separately joins each selection to the checked aggregate output membership.
func ValidateImageTags(tags map[string]string) error {
	return discoverytags.Validate(tags)
}

func imageTagOutputs(tags map[string]string) []string {
	outputs := make([]string, 0, len(tags))
	for output := range tags {
		outputs = append(outputs, output)
	}
	sort.Strings(outputs)
	return outputs
}

type imageAlias struct {
	output     string
	tag        name.Tag
	subject    name.Digest
	descriptor Descriptor
}

// The request and complete local publication are admitted before any registry
// operation. Only immutable scalar selections leave this preflight.
func prepareImageAliases(root PublicationRoot, references map[string]name.Digest, tags map[string]string) ([]imageAlias, error) {
	if err := ValidateImageTags(tags); err != nil {
		return nil, err
	}
	aliases := make([]imageAlias, 0, len(tags))
	for _, output := range imageTagOutputs(tags) {
		var object PublishedObject
		matches := 0
		for _, candidate := range root.Objects {
			if candidate.Name == output {
				object = candidate
				matches++
			}
		}
		subject, ok := references[output]
		if matches != 1 || !ok || root.Release == nil || object.Output == nil || object.Output.ImageIndex == nil {
			return nil, e.New(e.CodeInvalidCatalog, "image discovery tag %q must select exactly one aggregate OCI release output", output)
		}
		if err := validateImageIndexObject(object); err != nil {
			return nil, err
		}
		aliases = append(aliases, imageAlias{output: output, tag: subject.Context().Tag(tags[output]), subject: subject,
			descriptor: Descriptor{MediaType: MediaOCIIndex, Digest: object.DescriptorDigest, Size: object.Size}})
	}
	return aliases, nil
}

func publishImageAliases(ctx context.Context, root string, aliases []imageAlias, options RegistryOptions) error {
	if len(aliases) == 0 {
		return nil
	}
	if err := deliveryContext(ctx); err != nil {
		return err
	}
	workspace, err := os.MkdirTemp("", "nvkr-image-alias-readback-*")
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "creating image discovery readback workspace")
	}
	defer func() { _ = os.RemoveAll(workspace) }()
	// Re-read the entire exact release, including every sibling payload and SBOM,
	// before the first alias mutation. An earlier upload/HEAD is not this receipt.
	publication, err := PullPublication(ctx, root, workspace, options)
	if err != nil {
		return err
	}
	if err := publication.Close(); err != nil {
		return err
	}
	remoteOptions := registryOptions(ctx, options)
	for _, alias := range aliases { // Already sorted by exact output name.
		if err := publishImageAlias(ctx, alias, remoteOptions); err != nil {
			return err
		}
	}
	return nil
}

// aliasManifest supplies the exact already-checked index bytes to remote.Tag.
// Tag uploads only that manifest; full closure readback must precede this call.
type aliasManifest []byte

func (manifest aliasManifest) RawManifest() ([]byte, error) { return manifest, nil }
func (aliasManifest) MediaType() (types.MediaType, error)   { return types.OCIImageIndex, nil }

func publishImageAlias(ctx context.Context, alias imageAlias, options []remote.Option) error {
	raw, err := registryManifest(ctx, alias.subject, alias.descriptor, options)
	if err != nil {
		return err
	}
	existing, err := remote.Head(alias.tag, options...)
	switch {
	case err == nil:
		if existing.Digest.String() == alias.descriptor.Digest {
			if string(existing.MediaType) != MediaOCIIndex || existing.Size != alias.descriptor.Size {
				return e.New(e.CodeVerifyFailed, "existing image discovery tag %q has inconsistent metadata", alias.output)
			}
			return verifyImageAlias(ctx, alias, false, options)
		}
	case isNotFoundError(err):
		// Only a definitive absence permits creation without an existing subject.
	default:
		return classifyContext(ctx, e.Wrap(e.CodeUnavailable, err, "checking image discovery tag %q before mutation", alias.output))
	}
	if err := deliveryContext(ctx); err != nil {
		return err
	}
	if err := remote.Tag(alias.tag, aliasManifest(raw), options...); err != nil {
		return imageAliasPublishError(ctx, err, alias)
	}
	return verifyImageAlias(ctx, alias, true, options)
}

func verifyImageAlias(ctx context.Context, alias imageAlias, afterWrite bool, options []remote.Option) error {
	description := "checking existing image discovery tag"
	if afterWrite {
		description = "checking image discovery tag after PUT; tag may have advanced, read it before retrying"
	}
	got, err := remote.Get(alias.tag, options...)
	if err != nil {
		code := e.CodeUnavailable
		if ctx.Err() != nil {
			code = e.CodeTimeout
		}
		return e.Wrap(code, err, "%s for %q", description, alias.output)
	}
	if got.Digest.String() != alias.descriptor.Digest || string(got.MediaType) != MediaOCIIndex ||
		int64(len(got.Manifest)) != alias.descriptor.Size || digestBytes(got.Manifest) != alias.descriptor.Digest {
		return e.New(e.CodeVerifyFailed, "%s for %q returned a different index; no rollback was attempted", description, alias.output)
	}
	return nil
}

func imageAliasPublishError(ctx context.Context, err error, alias imageAlias) error {
	if ctx.Err() != nil {
		return e.Wrap(e.CodeTimeout, ctx.Err(), "publishing image discovery tag %q for %q; tag may have advanced, read it before retrying; no rollback was attempted", alias.tag.TagStr(), alias.output)
	}
	var rejected *transport.Error
	if errors.As(err, &rejected) && rejected.StatusCode >= 400 && rejected.StatusCode < 500 &&
		rejected.StatusCode != http.StatusRequestTimeout && rejected.StatusCode != http.StatusTooManyRequests {
		return e.Wrap(e.CodeInvalidCatalog, err, "registry rejected image discovery tag %q for %q; no delete or rollback was attempted", alias.tag.TagStr(), alias.output)
	}
	return e.Wrap(e.CodeUnavailable, err,
		"publishing image discovery tag %q for %q; tag may have advanced, read it before retrying; no rollback was attempted", alias.tag.TagStr(), alias.output)
}
