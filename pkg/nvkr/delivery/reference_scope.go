// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"strconv"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/development"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/name"
)

// publicationOutputParent preserves an output's original transport namespace.
// Scope admission prevents credential redirection; it grants no authenticity.
func publicationOutputParent(root name.Reference, origin *PublicationOrigin, parent string) (string, error) {
	if origin == nil {
		return parent, nil
	}
	if err := ValidatePublicationReference(origin.Ref); err != nil {
		return "", e.Wrap(e.CodeVerifyFailed, err, "invalid output publication origin")
	}
	ref, err := name.NewDigest(origin.Ref, name.StrictValidation)
	if err != nil {
		return "", e.Wrap(e.CodeVerifyFailed, err, "parse output publication origin")
	}
	repository := ref.Context().Name()
	originalParent := repository[:strings.LastIndexByte(repository, '/')+1]
	if originalParent == parent {
		return parent, nil
	}
	prefix, _, found := strings.Cut(root.Context().Name(), "/runs/")
	if !found {
		return "", e.New(e.CodeVerifyFailed, "output publication origin is outside the release namespace")
	}
	namespace, err := development.ParseNamespace(prefix)
	if err != nil {
		return "", e.Wrap(e.CodeVerifyFailed, err, "output publication origin requires the same development run")
	}
	current, err := namespace.ParseReference(root.Context().Name())
	if err != nil {
		return "", e.Wrap(e.CodeVerifyFailed, err, "invalid development release namespace")
	}
	previous, err := namespace.ParseReference(repository)
	if err != nil || previous.RunID() != current.RunID() {
		return "", e.New(e.CodeVerifyFailed, "output publication origin is outside the same development run")
	}
	// The namespace parser already validated canonical positive uint64 IDs.
	currentAttempt, _ := strconv.ParseUint(current.Attempt(), 10, 64)
	previousAttempt, _ := strconv.ParseUint(previous.Attempt(), 10, 64)
	if previousAttempt > currentAttempt {
		return "", e.New(e.CodeVerifyFailed, "output publication origin is from a future attempt")
	}
	return originalParent, nil
}
