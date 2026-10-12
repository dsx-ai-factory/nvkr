// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"errors"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

// VisitEvidence supplies exact held bundles and their checked subject bindings.
// The callback cannot create publication or signature authority. No source
// pathname is reopened, so replacement cannot alter the visited evidence.
func (publication *Publication) VisitEvidence(ctx context.Context, visit func(context.Context, EvidenceBinding, []byte) error) error {
	if err := deliveryContext(ctx); err != nil {
		return err
	}
	if publication == nil || visit == nil {
		return e.New(e.CodeGateUnmet, "evidence visitation requires an open publication and visitor")
	}
	publication.mu.RLock()
	defer publication.mu.RUnlock()
	if publication.closed {
		return e.New(e.CodeGateUnmet, "publication is closed")
	}
	if err := RequireAttestedEvidence(publication.inspection); err != nil {
		return err
	}
	var failures []error
	for _, object := range publication.inspection.Root.Objects {
		if object.Evidence == nil {
			continue
		}
		found := false
		for _, file := range publication.files {
			if file.Digest() != object.BlobDigest {
				continue
			}
			bundle, err := file.ReadAllContext(ctx, sbom.MaxBundleBytes)
			if err != nil {
				failures = append(failures, e.Wrap(e.CodeVerifyFailed, err, "reading held evidence %q", object.Name))
			} else {
				binding := *object.Evidence
				binding.Outputs = append([]string(nil), binding.Outputs...)
				failures = append(failures, visit(ctx, binding, bundle))
			}
			found = true
			break
		}
		if !found {
			failures = append(failures, e.New(e.CodeProvenanceMissing, "held evidence %q is absent", object.Name))
		}
	}
	return errors.Join(failures...)
}
