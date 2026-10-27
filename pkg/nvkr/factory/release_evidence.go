// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

// releaseEvidenceObjects derives the complete required evidence membership from
// declared output packages and validated producer inventories. There is no caller
// switch to waive evidence. Packaging and finalization use this same join.
func releaseEvidenceObjects(ctx context.Context, plan *Plan, node Node, artifacts map[string]Artifact) ([]delivery.ObjectInput, error) {
	return releaseEvidenceObjectsForOutputs(ctx, plan, node, artifacts, selectedPublicationOutputs(plan, node))
}

func releaseEvidenceObjectsForOutputs(ctx context.Context, plan *Plan, node Node, artifacts map[string]Artifact, outputs []LifecycleOutputPlan) ([]delivery.ObjectInput, error) {
	boms := map[string]*sbom.BOM{}
	for _, id := range node.Inputs {
		artifact := artifacts[id]
		if artifact.Kind != "sbom-set" {
			continue
		}
		objects, err := sbomPublicationObjects(ctx, plan, artifact, artifacts, func(path string) (string, error) { return path, nil })
		if err != nil {
			return nil, err
		}
		for _, object := range objects {
			if object.Kind != "sbom" {
				continue
			}
			var bom sbom.BOM
			if err := canonical.UnmarshalExact(bytes.TrimSuffix(object.Bytes, []byte{'\n'}), &bom); err != nil {
				return nil, e.Wrap(e.CodeVerifyFailed, err, "decoding held SBOM")
			}
			build := ""
			for _, p := range bom.Metadata.Props {
				if p.Name == "nvkr:buildDigest" {
					build = p.Value
				}
			}
			key := artifact.BuildTarget + "/" + build + "/" + bom.Metadata.Component.Name
			if previous := boms[key]; previous != nil {
				return nil, e.New(e.CodeVerifyFailed, "ambiguous build inventory %q", key)
			}
			boms[key] = &bom
		}
	}
	var result []delivery.ObjectInput
	add := func(bom *sbom.BOM, output, platform, component, stage, kind, subject, input string) error {
		bound, err := sbom.ForSubject(bom, subject, stage, input)
		if err != nil {
			return err
		}
		raw, err := bound.Marshal()
		if err != nil {
			return err
		}
		digest, err := bound.Digest()
		if err != nil {
			return err
		}
		inspected := ""
		if input != subject {
			inspected = input
		}
		result = append(result, delivery.ObjectInput{Name: "sbom-" + strings.TrimPrefix(digest, "sha256:"), Kind: "sbom", ArtifactType: "application/vnd.cyclonedx+json", LayerMediaType: "application/vnd.cyclonedx+json", SemanticDigest: digest, Bytes: raw,
			Evidence: &delivery.EvidenceBinding{Platform: platform, Outputs: []string{output}, Component: component, Stage: stage, SubjectKind: kind, SubjectDigest: subject, InspectionInputDigest: inspected}})
		return nil
	}
	for _, output := range outputs {
		artifact, ok := artifacts[output.ArtifactID]
		if !ok || !containsString(node.Inputs, artifact.ID) {
			return nil, e.New(e.CodeProvenanceMissing, "release omits output %q", output.Name)
		}
		if output.ImageIndex != nil {
			observation, err := observeImageIndex(ctx, plan, output, artifact, artifacts)
			if err != nil {
				return nil, err
			}
			for i, child := range output.ImageIndex.Children {
				bom := boms[child.BuildTarget+"/"+child.BuildDigest+"/"+child.Component]
				if bom == nil {
					return nil, e.New(e.CodeProvenanceMissing, "release omits native inventory for %q/%q", output.Name, child.Platform)
				}
				observed := observation.ImageIndex.Children[i]
				if err := add(bom, output.Name, child.Platform, child.Component, "build-output", "oci-manifest", observed.ManifestDigest, observed.BuildSubject); err != nil {
					return nil, err
				}
			}
			continue
		}
		for _, component := range output.Components {
			bom := boms[output.Name+"/"+output.BuildDigest+"/"+component]
			if bom == nil {
				return nil, e.New(e.CodeProvenanceMissing, "release omits inventory for %q/%q", output.Name, component)
			}
			subject := "sha256:" + bom.Metadata.Component.Hashes[0].Content
			input := subject
			stage, kind := "build-output", "file"
			switch output.Kind {
			case "oci":
				image, err := inspectFactoryImage(ctx, plan, artifact)
				if err != nil {
					return nil, err
				}
				subject, kind = image.ManifestDigest, "oci-manifest"
			case "helm-oci", PackageBundle:
				stage, kind = "source-build", "source-archive"
			}
			if err := add(bom, output.Name, "", component, stage, kind, subject, input); err != nil {
				return nil, err
			}
		}
		if output.Kind != "helm-oci" && output.Kind != PackageBundle {
			continue
		}
		descriptor, payload, err := delivery.ReadArtifact(ctx, artifact.Path)
		if err != nil {
			return nil, err
		}
		// subject is the OCI manifest digest: the published identity this evidence
		// attests to. input is the digest of the actual scanned bytes (the chart
		// tarball layer), recorded as InspectionInputDigest since it differs from
		// the manifest-level subject.
		subject := descriptor.Digest
		input := pipeline.FileDigest(payload)
		// Archive inspection is internal/offline and executes neither scripts nor a scanner.
		temporary, err := os.MkdirTemp(filepath.Dir(artifact.Path), ".release-archive-sbom-*")
		if err != nil {
			return nil, e.Wrap(e.CodeInternal, err, "creating package SBOM workspace")
		}
		funcErr := func() error {
			defer func() { _ = os.RemoveAll(temporary) }()
			path := filepath.Join(temporary, "archive.tar.gz")
			if err := writeFactoryFile(ctx, path, payload, 0o600); err != nil {
				return err
			}
			payloadKind, subjectKind := "tarball", "helm-bundle"
			if output.Kind == PackageBundle {
				payloadKind, subjectKind = "bundle-archive", "bundle"
			}
			inventory, err := sbom.InspectDependencies(ctx, path, input, payloadKind, sbom.Scanner{})
			if err != nil {
				return err
			}
			bom, err := sbom.NewSubject(output.BuildDigest, output.Name, subject)
			if err != nil {
				return err
			}
			bom, err = sbom.WithDependencies(bom, inventory.Components, inventory.Method, inventory.ScannerDigest)
			if err != nil {
				return err
			}
			return add(bom, output.Name, "", output.Name, "packaging", subjectKind, subject, input)
		}()
		if funcErr != nil {
			return nil, funcErr
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	var unique []delivery.ObjectInput
	for _, object := range result {
		if len(unique) > 0 && unique[len(unique)-1].Name == object.Name {
			previous := &unique[len(unique)-1]
			left, right := *previous.Evidence, *object.Evidence
			left.Outputs, right.Outputs = nil, nil
			if !bytes.Equal(previous.Bytes, object.Bytes) || !reflect.DeepEqual(left, right) {
				return nil, e.New(e.CodeVerifyFailed, "conflicting shared evidence document")
			}
			previous.Evidence.Outputs = append(previous.Evidence.Outputs, object.Evidence.Outputs...)
			slices.Sort(previous.Evidence.Outputs)
			previous.Evidence.Outputs = slices.Compact(previous.Evidence.Outputs)
		} else {
			unique = append(unique, object)
		}
	}
	return unique, nil
}
