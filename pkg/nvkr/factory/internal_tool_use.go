// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

const internalToolUseFile = "internal-tools.json"

// This is Run evidence, never a restorable execution capability or a Release
// member. Hosted transport authenticates its same-Run issuer; the live owner
// below is the only production writer. Attempt-specific terminal bindings stay
// here rather than contaminating reproducible component SBOMs.
type internalToolUseRecord struct {
	BootstrapInterpreterDigest string                             `json:"bootstrapInterpreterDigest"`
	BootstrapProgramDigest     string                             `json:"bootstrapProgramDigest"`
	SchemaVersion              string                             `json:"schemaVersion"`
	PlanDigest                 string                             `json:"planDigest"`
	Consumer                   string                             `json:"consumer"`
	Purpose                    string                             `json:"purpose"`
	Tool                       string                             `json:"tool"`
	Binding                    pipeline.ProducedExecutableBinding `json:"binding"`
	SubjectDigest              string                             `json:"subjectDigest"`
	SubjectSize                int64                              `json:"subjectSize"`
	Appliance                  string                             `json:"appliance"`
	Observation                string                             `json:"observation"`
	SBOM                       string                             `json:"sbom"`
}

// validateInternalToolUseEvidence checks descriptions inside already-admitted
// Run transport. It never restores the runtime or the live production owner.
func validateInternalToolUseEvidence(ctx context.Context, plan *Plan, node Node, directory string, artifacts map[string]Artifact, local func(string) (string, error)) error {
	path := filepath.Join(directory, internalToolUseFile)
	if len(node.InternalTools) == 0 {
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			return nil
		}
		return e.New(e.CodeVerifyFailed, "SBOM evidence contains an undeclared internal tool use")
	}
	if len(node.InternalTools) != 1 || node.Kind != NodeSBOM || !plan.hasCompiledDiskTarget(node.BuildTarget, artifacts) {
		return e.New(e.CodeEngineCapability, "internal tool evidence has no admitted consumer")
	}
	raw, err := readSmallFactoryFile(ctx, path, "internal tool use evidence")
	if err != nil {
		return e.Wrap(e.CodeProvenanceMissing, err, "required internal tool use is absent")
	}
	var record internalToolUseRecord
	if err := canonical.UnmarshalExact(bytes.TrimSuffix(raw, []byte{'\n'}), &record); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "decoding internal tool use")
	}
	if err := record.validate(); err != nil {
		return err
	}
	identity, err := plan.IdentityDigest()
	if err != nil {
		return err
	}
	var selected InternalToolPlan
	for _, candidate := range plan.InternalTools {
		if candidate.Name == node.InternalTools[0] {
			selected = candidate
		}
	}
	profile := plan.Environment.DiskInspectionProfiles[node.Platform]
	_, pinned, _ := strings.Cut(profile.Image, "@")
	if selected.Name == "" || record.PlanDigest != identity || record.Consumer != node.ID || record.Tool != selected.Name ||
		record.Binding.BuildDigest != selected.BuildDigest || record.Binding.Platform != selected.Platform ||
		record.Binding.Component != selected.Component || record.Appliance != pinned || profile.HelperTool != selected.Name ||
		record.BootstrapInterpreterDigest != profile.BootstrapInterpreterDigest || record.BootstrapProgramDigest != profile.BootstrapProgramDigest {
		return e.New(e.CodeVerifyFailed, "internal tool use differs from its exact planned consumer")
	}
	for _, evidence := range []struct{ id, digest string }{{selected.ObservationArtifactID, record.Observation}, {selected.SBOMArtifactID, record.SBOM}} {
		path, err := local(artifacts[evidence.id].Path)
		if err != nil {
			return err
		}
		actual, err := digestFactoryPath(ctx, path)
		if err != nil || actual != evidence.digest {
			return e.New(e.CodeVerifyFailed, "internal tool use changed its completed producer evidence")
		}
	}
	resultPath, err := local(artifacts[selected.ResultArtifactID].Path)
	if err != nil {
		return err
	}
	result, err := readProducerResult(ctx, resultPath)
	if err != nil {
		return err
	}
	if result.Component != selected.Component || result.SHA256 != record.Binding.Digest || result.Size != record.Binding.Size {
		return e.New(e.CodeVerifyFailed, "internal tool use changes its produced bytes")
	}
	for _, output := range plan.LifecycleOutputs {
		if output.Name != node.BuildTarget {
			continue
		}
		root := artifacts[output.ArtifactID]
		path, err := local(artifacts["result/"+output.Name+"/"+root.Component].Path)
		if err != nil {
			return err
		}
		subject, err := readProducerResult(ctx, path)
		if err != nil {
			return err
		}
		if subject.Component != root.Component || subject.SHA256 != record.SubjectDigest || subject.Size != record.SubjectSize {
			return e.New(e.CodeVerifyFailed, "internal tool use changed its consumer subject")
		}
		for _, component := range output.Components {
			raw, err := readFactorySBOMFile(ctx, filepath.Join(directory, component+".cdx.json"), "internal tool consumer evidence")
			if err != nil {
				return err
			}
			var bom sbom.BOM
			if err := canonical.UnmarshalExact(bytes.TrimSuffix(raw, []byte{'\n'}), &bom); err != nil {
				return e.Wrap(e.CodeVerifyFailed, err, "decoding internal tool consumer evidence")
			}
			matches := 0
			for _, property := range bom.Metadata.Props {
				if property.Name == "nvkr:diskInspectionHelperDigest" && property.Value == record.Binding.Digest {
					matches++
				}
			}
			if matches != 1 {
				return e.New(e.CodeVerifyFailed, "consumer evidence differs from its exact internal tool bytes")
			}
		}
		return nil
	}
	return e.New(e.CodePipelineDrift, "internal tool use has no delivery subject")
}

func (record internalToolUseRecord) validate() error {
	if record.SchemaVersion != "nvkr.dev/internal-tool-use/v1" || record.Consumer == "" || record.Tool == "" ||
		record.Purpose != "disk-inspection" || record.SubjectSize <= 0 || record.Binding.Size <= 0 ||
		record.Binding.Component == "" || (record.Binding.Platform != "linux/amd64" && record.Binding.Platform != "linux/arm64") {
		return e.New(e.CodeVerifyFailed, "internal tool use lacks its bounded exact identity")
	}
	for _, digest := range []string{record.PlanDigest, record.Binding.BuildDigest, record.Binding.Digest, record.Binding.TerminalDigest,
		record.SubjectDigest, record.Appliance, record.Observation, record.SBOM, record.BootstrapInterpreterDigest, record.BootstrapProgramDigest} {
		if !canonical.IsDigest(digest) {
			return e.New(e.CodeVerifyFailed, "internal tool use has an invalid exact-byte binding")
		}
	}
	return nil
}

// recordDiskInternalToolUse is called only after the original owner completes
// concrete reconciliation. The receipt retains these bytes privately until the
// package and evidence transaction commits them together.
func (owner *LocalRunResult) recordDiskInternalToolUse(ctx context.Context, plan *Plan, expected LifecycleOutputPlan, receipt *DiskEvidenceReceipt) ([]byte, error) {
	if receipt == nil || receipt.owner != owner || receipt.runtime == nil {
		return nil, e.New(e.CodeVerifyFailed, "internal tool use requires original reconciled evidence")
	}
	profile, err := owner.diskEvidenceProfile(ctx, plan, expected, receipt.runtime)
	if err != nil || profile.HelperTool == "" {
		return nil, err
	}
	tool, err := owner.borrowInternalExecutable(ctx, plan, profile.HelperTool)
	if err != nil {
		return nil, err
	}
	binding, err := receipt.runtime.AuthenticateProducedTool(ctx, profile.HelperTool, tool)
	if err != nil {
		return nil, err
	}
	appliance, err := receipt.runtime.RepositoryDigestContext(ctx)
	if err != nil {
		return nil, err
	}
	_, pinned, ok := strings.Cut(profile.Image, "@")
	if !ok || appliance != pinned {
		return nil, e.New(e.CodeVerifyFailed, "internal tool use changed its appliance")
	}
	digest, size := receipt.Subject()
	owner.diskMu.Lock()
	observation := owner.internalToolEvidence[profile.HelperTool][NodeVerify]
	sbom := owner.internalToolEvidence[profile.HelperTool][NodeSBOM]
	owner.diskMu.Unlock()
	record := internalToolUseRecord{SchemaVersion: "nvkr.dev/internal-tool-use/v1", PlanDigest: owner.PlanDigest,
		BootstrapInterpreterDigest: profile.BootstrapInterpreterDigest, BootstrapProgramDigest: profile.BootstrapProgramDigest,
		Consumer: expected.Name + "/sbom", Purpose: "disk-inspection", Tool: profile.HelperTool, Binding: binding,
		SubjectDigest: digest, SubjectSize: size, Appliance: appliance, Observation: observation, SBOM: sbom}
	if err := record.validate(); err != nil {
		return nil, err
	}
	return canonical.Marshal(record)
}
