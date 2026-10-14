// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"io"
	"slices"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

type publicationExecutable struct {
	bundle BinaryBundle
	layer  *heldfile.File
}

// retainPublicationExecutables uses only the owner's private checked layout
// during admission. Later access streams the original held layer and never
// reopens either that temporary layout or a caller-controlled source path.
func retainPublicationExecutables(ctx context.Context, layout string, inspection Inspection, files []*heldfile.File) (map[string]publicationExecutable, error) {
	held := make(map[string]*heldfile.File, len(files))
	for _, file := range files {
		held[file.Digest()] = file
	}
	result := make(map[string]publicationExecutable)
	for _, object := range inspection.Root.Objects {
		if object.Output == nil || object.Output.Format != "executable" {
			continue
		}
		manifest := held[object.DescriptorDigest]
		if manifest == nil || manifest.Size() != object.Size {
			return nil, e.New(e.CodeVerifyFailed, "executable output %q has no held manifest", object.Name)
		}
		raw, err := manifest.ReadAllContext(ctx, 8<<20)
		if err != nil {
			return nil, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "reading held executable manifest"))
		}
		config, bundle, err := readBinaryBundleConfig(ctx, layout, raw)
		if err != nil {
			return nil, err
		}
		layer := held[config.Layers[0].Digest]
		if layer == nil || layer.Size() != config.Layers[0].Size {
			return nil, e.New(e.CodeVerifyFailed, "executable output %q has no held layer", object.Name)
		}
		// inspectOutputs already checked the complete archive, config identity,
		// platform and selected membership against these exact retained bytes.
		result[object.Name] = publicationExecutable{bundle: bundle, layer: layer}
	}
	return result, nil
}

// ExecutableBundle returns defensive metadata for one exact named executable
// output. It establishes checked payload membership, not execution, signature
// or qualification authority. InstallPath remains a deployment suggestion.
func (publication *Publication) ExecutableBundle(outputName string) (BinaryBundle, error) {
	if publication == nil || publication.publicationState == nil {
		return BinaryBundle{}, e.New(e.CodeGateUnmet, "publication is not open")
	}
	publication.mu.RLock()
	defer publication.mu.RUnlock()
	if publication.closed {
		return BinaryBundle{}, e.New(e.CodeGateUnmet, "publication is closed")
	}
	executable, exists := publication.executables[outputName]
	if !exists {
		return BinaryBundle{}, e.New(e.CodeVerifyFailed, "publication has no executable output named %q", outputName)
	}
	bundle := executable.bundle
	bundle.Entries = slices.Clone(bundle.Entries)
	return bundle, nil
}

// WriteExecutableMember streams one exact executable member from the retained
// publication archive. It checks the complete declared archive, not merely the
// selected member, through the same walker as extraction. Close waits for active
// streams. The caller owns destination and must bound any blocking Write; no
// goroutine is detached to interrupt it. Errors can leave partial bytes, which
// must be discarded rather than executed or treated as successful evidence.
func (publication *Publication) WriteExecutableMember(ctx context.Context, outputName, memberName string, destination io.Writer) (int64, error) {
	if err := deliveryContext(ctx); err != nil {
		return 0, err
	}
	if publication == nil || publication.publicationState == nil {
		return 0, e.New(e.CodeGateUnmet, "publication is not open")
	}
	if destination == nil {
		return 0, e.New(e.CodeUsage, "executable member destination is required")
	}
	publication.mu.RLock()
	defer publication.mu.RUnlock()
	if publication.closed {
		return 0, e.New(e.CodeGateUnmet, "publication is closed")
	}
	executable, exists := publication.executables[outputName]
	if !exists {
		return 0, e.New(e.CodeVerifyFailed, "publication has no executable output named %q", outputName)
	}
	index := slices.IndexFunc(executable.bundle.Entries, func(entry BinaryEntry) bool { return entry.Name == memberName })
	if index < 0 {
		return 0, e.New(e.CodeVerifyFailed, "executable output %q has no member named %q", outputName, memberName)
	}
	if executable.bundle.Entries[index].Mode&0o111 == 0 {
		return 0, e.New(e.CodeEngineCapability, "member %q has no executable permission", memberName)
	}
	var written int64
	err := walkBundleLayer(ctx, executable.layer, executable.bundle, func(entry BinaryEntry, payload io.Reader) error {
		if entry.Name != memberName {
			_, err := io.Copy(io.Discard, payload)
			return err
		}
		var err error
		written, err = io.Copy(destination, payload)
		return err
	})
	return written, err
}
