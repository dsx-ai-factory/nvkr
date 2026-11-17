// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

var errPredicateBinding = errors.New("dry-run predicate byte binding differs")

// qualifyBindings checks diagnostic records; it issues no receipt or trust claim.
func qualifyBindings(ctx context.Context, work, reference string) error {
	plan, err := factory.LoadPlanContext(ctx, filepath.Join(work, "plan.json"))
	if err != nil {
		return err
	}
	digest, err := plan.Digest()
	if err != nil {
		return err
	}
	attempt := filepath.Join(work, ".nvkr/runs/quick")
	var run lifecycle.Run
	if err := readJSON(filepath.Join(attempt, "lifecycle.json"), &run); err != nil {
		return err
	}
	if run.Status != "complete" || run.Lane != "unqualified-local-execution" || run.PlanDigest != digest ||
		run.DefinitionDigest != plan.DefinitionDigest || run.EnvironmentDigest != plan.EnvironmentDigest ||
		len(run.Expected) != 1 || len(run.Observed) != 1 || run.Expected[0].Name != "hello" ||
		run.Observed[0].Name != "hello" || run.Observed[0].Kind != "executable" ||
		run.Expected[0].BuildDigest != run.Observed[0].BuildDigest {
		return fmt.Errorf("expected one complete hello Run bound to the compiled Plan")
	}
	content, err := delivery.ContentDigest(run.Observed)
	if err != nil {
		return err
	}
	inspection, err := delivery.Inspect(ctx, filepath.Join(work, "readback"))
	if err != nil {
		return err
	}
	metadata := inspection.Root.Release
	if metadata == nil || !strings.HasSuffix(reference, "@"+inspection.RootDigest) ||
		metadata.ContentSubject != content || run.ContentSubject != content || metadata.PlanDigest != digest ||
		metadata.DefinitionDigest != run.DefinitionDigest || metadata.EnvironmentDigest != run.EnvironmentDigest {
		return fmt.Errorf("recovered publication is not bound to the complete observed Run")
	}
	var execution factory.LocalRunResult
	if err := readJSON(filepath.Join(attempt, "run.json"), &execution); err != nil {
		return err
	}
	if execution.PlanDigest != digest || len(execution.Nodes) != len(plan.Graph.Nodes) {
		return fmt.Errorf("execution omitted planned operations")
	}
	completed := make(map[string]string)
	for _, node := range execution.Nodes {
		if completed[node.ID] != "" || node.Status != "ok" && node.Status != "cached" {
			return fmt.Errorf("operation %s was repeated, skipped or failed", node.ID)
		}
		completed[node.ID] = node.Kind
	}
	for _, node := range plan.Graph.Nodes {
		if completed[node.ID] != node.Kind {
			return fmt.Errorf("missing operation %s", node.ID)
		}
	}
	var index sbom.Index
	if err := readJSON(filepath.Join(attempt, "hello/sbom/index.json"), &index); err != nil {
		return err
	}
	produced, err := os.ReadFile(filepath.Join(attempt, "hello/out/hello/hello"))
	if err != nil {
		return err
	}
	recovered, err := os.ReadFile(filepath.Join(work, "extracted/hello"))
	if err != nil {
		return err
	}
	if !bytes.Equal(produced, recovered) || len(index.Entries) != 1 || index.Entries[0].ArtifactSHA != pipeline.FileDigest(recovered) || index.BuildDigest != run.Observed[0].BuildDigest {
		return fmt.Errorf("registry read-back differs from produced payload or SBOM subject")
	}
	var signed sign.SigningRecord
	var verified sign.VerificationRecord
	if err := readJSON(filepath.Join(attempt, "signatures/release.json"), &signed); err != nil {
		return err
	}
	if err := readJSON(filepath.Join(attempt, "verifications/release.json"), &verified); err != nil {
		return err
	}
	predicate, err := os.ReadFile(filepath.Join(attempt, "predicates/release.json"))
	if err != nil {
		return err
	}
	return checkPublication(reference, predicate, signed, verified)
}

func checkPublication(reference string, predicate []byte, signed sign.SigningRecord, verified sign.VerificationRecord) error {
	_, digest, ok := strings.Cut(reference, "@")
	if !ok || !canonical.IsDigest(digest) || signed.Ref != reference || verified.Ref != reference || signed.Digest != digest || verified.Digest != digest {
		return fmt.Errorf("publication/signing/verification subjects differ")
	}
	if !signed.DryRun || !verified.DryRun || verified.SignatureVerified || verified.AttestationVerified || len(signed.Ran) != 0 || len(verified.Ran) != 0 {
		return fmt.Errorf("quick E2E requires explicitly unsigned dry-run records")
	}
	if signed.Identity != "^quick-e2e-unsigned$" || signed.Issuer != "https://nvkr.dev/local" || verified.Identity != signed.Identity || verified.Issuer != signed.Issuer ||
		signed.PredicateType != sign.ProvenancePredicateType || verified.PredicateType != signed.PredicateType {
		return fmt.Errorf("dry-run signer policy differs")
	}
	if signed.PredicateSHA256 != pipeline.FileDigest(predicate) {
		return errPredicateBinding
	}
	return nil
}

func rejectChangedPredicate(ctx context.Context, work, reference string) (resultErr error) {
	path := filepath.Join(work, ".nvkr/runs/quick/predicates/release.json")
	original, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	// Restore even when the diagnostic rejects for an unexpected reason.
	defer func() { resultErr = errors.Join(resultErr, os.WriteFile(path, original, 0o644)) }()
	if err := os.WriteFile(path, append(bytes.Clone(original), '\n'), 0o644); err != nil {
		return err
	}
	err = qualifyBindings(ctx, work, reference)
	if !errors.Is(err, errPredicateBinding) {
		return fmt.Errorf("changed predicate was not rejected at its byte binding: %v", err)
	}
	fmt.Printf("[quick] negative proof: %v\n", err)
	if err := os.WriteFile(path, original, 0o644); err != nil {
		return err
	}
	return qualifyBindings(ctx, work, reference)
}

func readJSON(path string, value any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return err
	}
	return json.Unmarshal(raw, value)
}
