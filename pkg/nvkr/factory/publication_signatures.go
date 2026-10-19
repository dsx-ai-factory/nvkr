// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

// publicationOutputSubjects derives the independent signing subjects only from
// control documents admitted by Inspect or InspectMetadata. The caller owns the
// route, Run and evidence policy; metadata alone issues no payload receipt.
func publicationOutputSubjects(reference string, inspection delivery.Inspection) ([]sign.Subject, error) {
	refs, err := delivery.OutputReferences(reference, inspection)
	if err != nil {
		return nil, err
	}
	subjects := make([]sign.Subject, 0, len(refs))
	for _, name := range sortedKeys(refs) {
		ref := refs[name]
		subject := sign.Subject{Ref: ref, Digest: ref[strings.LastIndexByte(ref, '@')+1:]}
		if err := subject.Validate(); err != nil {
			return nil, err
		}
		subjects = append(subjects, subject)
	}
	return subjects, nil
}

func signPublicationOutputs(ctx context.Context, signer sign.OCISigner, reference string,
	inspection delivery.Inspection, predicate []byte, signing *definition.Signing, stdout io.Writer) error {
	subjects, err := publicationOutputSubjects(reference, inspection)
	if err != nil {
		return err
	}
	for _, subject := range subjects {
		record, err := signer.SignAndAttest(ctx, subject, predicate)
		if err != nil {
			return err
		}
		if err := record.Validate(); err != nil {
			return err
		}
		if record.Ref != subject.Ref || record.Digest != subject.Digest || record.DryRun != (signing.Mode == SigningModeDryRun) {
			return e.New(e.CodeVerifyFailed, "deliverable signing record does not bind %s", subject.Ref)
		}
		fmt.Fprintf(stdout, "signed artifact/evidence (mode %s, dryRun %t): %s\n", signing.Mode, record.DryRun, subject.Ref)
	}
	return nil
}

func verifyPublicationOutputs(ctx context.Context, signer sign.OCISigner, reference string,
	inspection delivery.Inspection, signing *definition.Signing, stdout io.Writer) error {
	subjects, err := publicationOutputSubjects(reference, inspection)
	if err != nil {
		return err
	}
	for _, subject := range subjects {
		record, err := signer.Verify(ctx, subject, sign.VerifyPolicy{
			IdentityRegexp: signing.Identity, Issuer: signing.Issuer, RequireAttestation: true,
		})
		if err != nil {
			return err
		}
		if err := record.Validate(); err != nil {
			return err
		}
		if record.Ref != subject.Ref || record.Digest != subject.Digest ||
			record.DryRun != (signing.Mode == SigningModeDryRun) ||
			(!record.DryRun && (!record.SignatureVerified || !record.AttestationVerified)) {
			return e.New(e.CodeVerifyFailed, "deliverable %s lacks its required signature and provenance", subject.Ref)
		}
		fmt.Fprintf(stdout, "verified artifact/evidence (mode %s, dryRun %t): %s\n", signing.Mode, record.DryRun, subject.Ref)
	}
	return nil
}

func verifySBOMAttestations(ctx context.Context, signer sign.OCISigner, root string, inspection delivery.Inspection) error {
	if err := delivery.RequireAttestedEvidence(inspection); err != nil {
		return err
	}
	attester, ok := signer.(sign.SBOMAttester)
	if !ok {
		return e.New(e.CodeEngineCapability, "signer cannot verify SBOM attestations")
	}
	for _, object := range inspection.Root.Objects {
		if object.Evidence == nil {
			continue
		}
		_, bundle, err := delivery.ReadObject(ctx, root, object.Name)
		if err != nil {
			return err
		}
		if err := attester.VerifySBOM(ctx, object.Evidence.SubjectDigest, bundle); err != nil {
			return err
		}
	}
	return nil
}
