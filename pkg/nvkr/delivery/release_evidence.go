// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"context"
	"reflect"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

type releaseEvidenceSlot struct{ output, platform, stage, component string }

func evidenceSlot(output string, binding EvidenceBinding) releaseEvidenceSlot {
	return releaseEvidenceSlot{output: output, platform: binding.Platform, stage: binding.Stage, component: binding.Component}
}

// verifyReleaseEvidence is shared by staged publication and consumption through
// Inspect. Every declared output derives its required evidence slots; metadata
// cannot waive a slot or substitute a document for another stage or subject.
func verifyReleaseEvidence(ctx context.Context, root string, publication PublicationRoot) error {
	return verifyReleaseEvidenceSources(ctx, root, publication, nil)
}

// sources receives descriptions from the same exact subject documents checked
// for release membership. Nothing escapes an inspection unless every slot passes.
func verifyReleaseEvidenceSources(ctx context.Context, root string, publication PublicationRoot, sources map[string]PublicationSource) error {
	if publication.Release == nil {
		return nil
	}
	expected := map[releaseEvidenceSlot]EvidenceBinding{}
	builds := map[releaseEvidenceSlot]string{}
	diskRoots := map[string]string{}
	diskModes := map[string]string{}
	for _, o := range publication.Objects {
		output := o.Output
		if output == nil {
			continue
		}
		if output.ImageIndex != nil {
			if err := validateImageIndexObject(o); err != nil {
				return err
			}
			for _, child := range output.ImageIndex.Children {
				binding := EvidenceBinding{Platform: child.Platform, Outputs: []string{o.Name}, Component: child.Component,
					Stage: "build-output", SubjectKind: "oci-manifest", SubjectDigest: child.ManifestDigest,
					InspectionInputDigest: child.BuildSubject}
				slot := evidenceSlot(o.Name, binding)
				expected[slot], builds[slot] = binding, child.BuildDigest
			}
			continue
		}
		subjects := map[string]string{}
		previous := ""
		for _, subject := range output.BuildSubjects {
			if subject.Component <= previous || !isDigest(subject.Digest) {
				return e.New(e.CodeVerifyFailed, "output build subjects must be sorted, unique and exact")
			}
			subjects[subject.Component] = subject.Digest
			previous = subject.Component
		}
		if len(subjects) != len(output.Components) {
			return e.New(e.CodeVerifyFailed, "output build subject membership is incomplete")
		}
		if output.Format == "disk-image" {
			if output.Disk == nil || subjects[output.Disk.Component] != output.Disk.PayloadDigest {
				return e.New(e.CodeVerifyFailed, "disk root build subject differs from exact QCOW2 payload")
			}
			diskRoots[o.Name] = output.Disk.Component
			diskModes[o.Name] = output.Disk.EvidenceMode
		}
		if output.Format == "executable" {
			manifest, err := flatManifest(ctx, root, o)
			if err != nil {
				return err
			}
			raw, err := readBlob(ctx, root, manifest.Config, 8<<20)
			if err != nil {
				return err
			}
			var bundle BinaryBundle
			if err := canonical.UnmarshalExact(raw, &bundle); err != nil {
				return e.Wrap(e.CodeVerifyFailed, err, "decoding release binary config")
			}
			if len(bundle.Entries) != len(subjects) {
				return e.New(e.CodeVerifyFailed, "binary bundle differs from release subjects")
			}
			for _, entry := range bundle.Entries {
				if subjects[entry.Name] != entry.SHA256 {
					return e.New(e.CodeVerifyFailed, "binary SBOM subject differs from delivered file")
				}
			}
		}
		for _, component := range output.Components {
			subject, ok := subjects[component]
			if !ok {
				return e.New(e.CodeVerifyFailed, "output component has no build subject")
			}
			binding := EvidenceBinding{Outputs: []string{o.Name}, Component: component, Stage: "build-output", SubjectKind: "file", SubjectDigest: subject}
			switch output.Format {
			case "oci":
				binding.SubjectKind = "oci-manifest"
				binding.SubjectDigest = o.DescriptorDigest
				// A built export is a distinct producer subject. An acquired
				// native manifest is already the delivered subject, so it has
				// no separate input binding (the shared SBOM subject rule).
				if subject != binding.SubjectDigest {
					binding.InspectionInputDigest = subject
				}
			case outputFormatHelm:
				binding.Stage = "source-build"
				binding.SubjectKind = "source-archive"
			case "executable", "apt-repository", "disk-image":
			default:
				return e.New(e.CodeVerifyFailed, "release has an unsupported output format")
			}
			slot := evidenceSlot(o.Name, binding)
			expected[slot], builds[slot] = binding, output.BuildDigest
		}
		if output.Format == outputFormatHelm {
			binding := EvidenceBinding{Outputs: []string{o.Name}, Component: o.Name, Stage: "packaging", SubjectKind: "helm-bundle",
				SubjectDigest: o.DescriptorDigest, InspectionInputDigest: o.BlobDigest}
			slot := evidenceSlot(o.Name, binding)
			expected[slot], builds[slot] = binding, output.BuildDigest
		}
	}
	seen := map[releaseEvidenceSlot]bool{}
	for _, object := range publication.Objects {
		if object.Evidence == nil {
			if object.Kind == "sbom" || object.Kind == "sbom-index" {
				return e.New(e.CodeVerifyFailed, "unbound release evidence object")
			}
			continue
		}
		binding := *object.Evidence
		if len(binding.Outputs) == 0 {
			return e.New(e.CodeVerifyFailed, "evidence has no output membership")
		}
		previousOutput := ""
		for _, outputName := range binding.Outputs {
			key := evidenceSlot(outputName, binding)
			want, ok := expected[key]
			single := binding
			single.Outputs = []string{outputName}
			single.DocumentDigest = ""
			if outputName <= previousOutput || !ok || seen[key] || !reflect.DeepEqual(single, want) || object.Kind != "sbom" || (object.ArtifactType != "application/vnd.cyclonedx+json" && object.ArtifactType != sbom.BundleMediaType) {
				return e.New(e.CodeVerifyFailed, "missing, duplicate or substituted evidence binding %v", key)
			}
			previousOutput = outputName
			seen[key] = true
		}
		manifest, err := flatManifest(ctx, root, object)
		if err != nil {
			return err
		}
		owner, err := evidenceOwner(publication.Objects, object.Evidence)
		if err != nil {
			return err
		}
		if manifest.Subject == nil || manifest.Subject.Digest != owner.DescriptorDigest || manifest.Subject.Size != owner.Size || manifest.Subject.MediaType != publishedManifestMediaType(owner) {
			return e.New(e.CodeVerifyFailed, "SBOM attachment is not bound to its associated deliverable")
		}
		if len(manifest.Layers) != 1 || manifest.Layers[0].MediaType != object.ArtifactType {
			return e.New(e.CodeVerifyFailed, "SBOM manifest has an invalid payload")
		}
		limit := int64(sbom.MaxDocumentBytes)
		if object.ArtifactType == sbom.BundleMediaType {
			limit = sbom.MaxBundleBytes
		}
		raw, err := readBlob(ctx, root, manifest.Layers[0], limit)
		if err != nil {
			return err
		}
		if object.ArtifactType == sbom.BundleMediaType {
			raw, err = sbom.AttestedDocument(raw, binding.SubjectDigest)
			if err != nil {
				return err
			}
		}
		if digestBytes(raw) != binding.DocumentDigest {
			return e.New(e.CodeVerifyFailed, "SBOM document digest differs from its release binding")
		}
		var bom sbom.BOM
		if err := canonical.UnmarshalExact(bytes.TrimSuffix(raw, []byte{'\n'}), &bom); err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "decoding release SBOM")
		}
		props := map[string]string{}
		for _, p := range bom.Metadata.Props {
			props[p.Name] = p.Value
		}
		// Missing disk-root and material provenance share the release's owning
		// gate, including the scanner pin required by generic BOM validation.
		// After authenticating document bytes, report missing provenance before
		// semantic/subject mismatch when both defects exist. The error identifies
		// the first failed gate, not the absence of any other integrity defect.
		// This grants no authority: the exact semantic/subject checks still follow.
		for _, outputName := range binding.Outputs {
			diskRoot := diskRoots[outputName]
			if diskRoot == "" {
				continue
			}
			if diskModes[outputName] == "build-provenance-v1" {
				if err := sbom.ValidateDiskBuildProvenance(&bom); err != nil {
					return err
				}
				continue
			}
			keys := []string{"nvkr:dependencyScannerDigest"}
			if diskRoot == binding.Component {
				if len(bom.Components) == 0 || props["nvkr:dependencyInspection"] != "disk-reconciled/v1" {
					return e.New(e.CodeProvenanceMissing, "disk root requires a full reconciled inventory")
				}
				keys = append(keys, "nvkr:diskInspectionRuntimeDigest", "nvkr:diskInspectorDigest", "nvkr:diskCodecDigest")
			} else if len(bom.Components) == 0 || props["nvkr:dependencyInspection"] != "disk-content/v1" {
				return e.New(e.CodeProvenanceMissing, "disk material requires its exact inspected content inventory")
			}
			for _, key := range keys {
				if !isDigest(props[key]) {
					return e.New(e.CodeProvenanceMissing, "disk inventory lacks an exact %s", key)
				}
			}
		}
		digest, err := bom.Digest()
		if err != nil || digest != object.SemanticDigest || object.Name != "sbom-"+strings.TrimPrefix(digest, "sha256:") || len(bom.Metadata.Component.Hashes) != 1 || "sha256:"+bom.Metadata.Component.Hashes[0].Content != binding.SubjectDigest || bom.Metadata.Component.Name != binding.Component {
			return e.New(e.CodeVerifyFailed, "SBOM document differs from its exact release subject")
		}
		for _, outputName := range binding.Outputs {
			if props["nvkr:buildDigest"] != builds[evidenceSlot(outputName, binding)] {
				return e.New(e.CodeVerifyFailed, "SBOM build is unbound")
			}
			diskRoot := diskRoots[outputName]
			method := props["nvkr:dependencyInspection"]
			if diskRoot == "" && (method == "disk-content/v1" || method == "disk-reconciled/v1") {
				return e.New(e.CodeVerifyFailed, "disk inspection method is not valid for a non-disk output")
			}
		}
		if props["nvkr:evidenceStage"] != binding.Stage || props["nvkr:inspectionInputDigest"] != binding.InspectionInputDigest {
			return e.New(e.CodeVerifyFailed, "SBOM build, stage or inspection input is unbound")
		}
		if binding.Stage == "packaging" && (len(bom.Components) == 0 || props["nvkr:dependencyInspection"] != "helm-manifests/v1") {
			return e.New(e.CodeProvenanceMissing, "final Helm package requires an inspected inventory")
		}
		if sources != nil {
			componentProperties := map[string]string{}
			for _, property := range bom.Metadata.Component.Properties {
				componentProperties[property.Name] = property.Value
			}
			if componentProperties["nvkr:fromSource"] == "true" {
				sources[object.Name] = PublicationSource{Repo: componentProperties["nvkr:repo"], Commit: componentProperties["nvkr:commit"]}
			}
		}
	}
	if len(seen) != len(expected) {
		return e.New(e.CodeProvenanceMissing, "release omits required stage-specific SBOM evidence")
	}
	return nil
}
