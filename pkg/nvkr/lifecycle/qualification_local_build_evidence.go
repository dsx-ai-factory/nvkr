// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"context"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

// localBuildEvidence describes a retained local Run whose inputs were replayed
// and payloads captured. It does not claim authenticated execution or smoke.
// Blob digests bind exact accompanying bytes, not semantic structured identity.
type localBuildEvidence struct {
	SchemaVersion     string                     `json:"schemaVersion"`
	Lane              string                     `json:"lane"`
	Origin            string                     `json:"origin"`
	PolicyDigest      string                     `json:"policyDigest"`
	Attempt           string                     `json:"attempt"`
	DefinitionDigest  string                     `json:"definitionDigest"`
	EnvironmentDigest string                     `json:"environmentDigest"`
	PlanDigest        string                     `json:"planDigest"`
	ContentSubject    string                     `json:"contentSubject"`
	Inputs            []localEvidenceBlob        `json:"inputs"`
	OrdinaryRoot      string                     `json:"ordinaryRoot"`
	Outputs           []localBuildEvidenceOutput `json:"outputs"`
}

type localEvidenceBlob struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

type localBuildEvidenceOutput struct {
	Artifact        Artifact `json:"artifact"`
	TransportDigest string   `json:"transportDigest"`
}

// prepareLocalBuildEvidence returns bounded canonical bytes only. The local
// builder must sign these with its independently selected key, and consumers
// must fetch and recheck every referenced blob and payload. No receipt is issued.
func prepareLocalBuildEvidence(ctx context.Context, inputs *localQualificationInputs, capture *localQualificationCapture, policy LocalQualificationPolicy) ([]byte, error) {
	if ctx == nil || inputs == nil || inputs.plan == nil || capture == nil || capture.disks == nil {
		return nil, e.New(e.CodeUsage, "local build evidence requires replayed inputs and a complete capture")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "local build evidence canceled")
	}
	if err := policy.RequirePlan(inputs.plan); err != nil {
		return nil, err
	}
	if err := validateInspectedRun(&inputs.run); err != nil {
		return nil, err
	}
	if inputs.run.Status != RunComplete || inputs.run.Lane != factory.TierLocalExecution.String() || inputs.run.ContentSubject == "" {
		return nil, e.New(e.CodeGateUnmet, "local build evidence requires the complete unqualified Run")
	}
	// Refuse a description modified after input replay. The referenced bytes are
	// exactly what downstream consumers must replay, not a freshly serialized Plan.
	for _, pair := range []struct {
		raw   []byte
		value any
	}{{inputs.planBytes, inputs.plan}, {inputs.runBytes, inputs.run}} {
		expected, err := canonical.Marshal(pair.value)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(pair.raw, expected) {
			return nil, e.New(e.CodeVerifyFailed, "local evidence inputs changed after replay")
		}
	}
	if len(inputs.inputBytes) == 0 {
		return nil, e.New(e.CodeProvenanceMissing, "local build evidence lacks captured compiler inputs")
	}
	if len(capture.disks.members) > 0 {
		if err := validateLocalQualificationMaterials(ctx, []byte(capture.materialBytes), inputs); err != nil {
			return nil, err
		}
	}
	policyDigest, err := policy.Digest()
	if err != nil {
		return nil, err
	}
	doc := localBuildEvidence{SchemaVersion: "nvkr.dev/local-build-evidence/v1", Lane: factory.TierLocalExecution.String(), Origin: "retained-local-run",
		PolicyDigest: policyDigest, Attempt: inputs.run.Attempt, DefinitionDigest: inputs.run.DefinitionDigest, EnvironmentDigest: inputs.run.EnvironmentDigest,
		PlanDigest: inputs.run.PlanDigest, ContentSubject: inputs.run.ContentSubject, OrdinaryRoot: capture.ordinaryRoot,
		Inputs: []localEvidenceBlob{}, Outputs: []localBuildEvidenceOutput{}}
	for _, blob := range []struct {
		name string
		raw  []byte
	}{{"compile-inputs.json", inputs.inputBytes}, {"disk-materials.json", []byte(capture.materialBytes)}, {"lifecycle.json", inputs.runBytes}, {"plan.json", inputs.planBytes}} {
		if len(blob.raw) == 0 {
			continue
		}
		doc.Inputs = append(doc.Inputs, localEvidenceBlob{Name: blob.name, Digest: pipeline.FileDigest(blob.raw), Size: int64(len(blob.raw))})
	}
	ordinary := map[string]Artifact{}
	if capture.ordinary != nil {
		outputs, err := capture.ordinary.Outputs()
		if err != nil {
			return nil, err
		}
		for _, output := range outputs {
			output.OS, output.Arch, output.ChartVersion = "", "", ""
			ordinary[output.Name] = output
		}
		if !canonical.IsDigest(capture.ordinaryRoot) {
			return nil, e.New(e.CodeVerifyFailed, "local ordinary transport has no root identity")
		}
	} else if capture.ordinaryRoot != "" {
		return nil, e.New(e.CodeVerifyFailed, "local ordinary root has no captured payloads")
	}
	disks := 0
	for _, output := range inputs.run.Observed {
		transport := output.Digest
		if output.Kind == OutputDiskImage {
			owner := capture.disks.members[output.Name]
			if err := owner.RequireStandardProfile(ctx); err != nil {
				return nil, err
			}
			view := owner.Inspection()
			if output.Disk == nil || view.PayloadDigest != output.Digest || view.Size != output.Disk.Size || view.Encryption != output.Disk.Encryption || capture.materialBytes == "" {
				return nil, e.New(e.CodeVerifyFailed, "local disk evidence differs from the captured payload")
			}
			transport = view.ManifestDigest
			disks++
		} else {
			got, exists := ordinary[output.Name]
			wantRaw, err := canonical.Marshal(output)
			if err != nil {
				return nil, err
			}
			gotRaw, err := canonical.Marshal(got)
			if err != nil {
				return nil, err
			}
			if !exists || !bytes.Equal(wantRaw, gotRaw) {
				return nil, e.New(e.CodeVerifyFailed, "local ordinary evidence differs from captured membership")
			}
			delete(ordinary, output.Name)
		}
		doc.Outputs = append(doc.Outputs, localBuildEvidenceOutput{Artifact: output, TransportDigest: transport})
	}
	if len(ordinary) != 0 || disks != len(capture.disks.members) || (disks == 0 && capture.materialBytes != "") {
		return nil, e.New(e.CodeVerifyFailed, "local build evidence changes complete captured membership")
	}
	raw, err := canonical.Marshal(doc)
	if err != nil {
		return nil, err
	}
	if len(raw) > 4<<20 {
		return nil, e.New(e.CodeProvenanceMissing, "local build evidence exceeds its byte bound")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "local build evidence canceled")
	}
	return raw, nil
}
