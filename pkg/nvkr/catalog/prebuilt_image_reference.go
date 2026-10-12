// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/name"
)

// Reference syntax is independent of lock descriptions. Neither this admission
// nor a caller pin proves membership; acquisition authenticates actual bytes.
func validateAcquiredImageReference(component, ref string) error {
	if strings.Contains(ref, "{{") {
		// Existing renderRef/checkArchTokens own templates. Rendered admission is
		// mandatory again at lock, resolved, maintenance and executable boundaries.
		return nil
	}
	parsed, err := name.ParseReference(strings.TrimPrefix(ref, "oci://"))
	if err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "prebuilt image %q reference", component)
	}
	if pinned, ok := parsed.(name.Digest); ok && !canonical.IsDigest(pinned.DigestStr()) {
		return e.New(e.CodeUnverifiedRef, "prebuilt image %q requires a SHA-256 subject", component)
	}
	return nil
}

func validateImageBinding(component, ref, parent string, digests map[string]string) error {
	if err := validateAcquiredImageReference(component, ref); err != nil {
		return err
	}
	parsed, err := name.ParseReference(strings.TrimPrefix(ref, "oci://"))
	if err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "image binding %q", component)
	}
	if parent != "" && !canonical.IsDigest(parent) {
		return e.New(e.CodeUnverifiedRef, "image binding %q has an invalid parent subject", component)
	}
	if authored, ok := parsed.(name.Digest); ok {
		if parent != "" && parent != authored.DigestStr() {
			return e.New(e.CodeUnverifiedRef, "image binding %q changes the authored parent digest", component)
		}
		if parent == "" {
			for _, child := range digests {
				if child != authored.DigestStr() {
					return e.New(e.CodeUnverifiedRef, "image binding %q lacks its authored parent lock binding", component)
				}
			}
		}
	}
	return nil
}
