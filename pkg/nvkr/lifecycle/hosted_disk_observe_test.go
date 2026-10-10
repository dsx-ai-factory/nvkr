// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// These transported records and fixture bundles exercise integrity/membership,
// not live reconciliation receipt issuance or cryptographic authentication.
func TestObserveHostedDiskOutputsChecksExactPublicationMembership(t *testing.T) {
	for _, test := range []struct {
		name string
		code e.Code
	}{
		{name: "exact"},
		{name: "substituted descriptor", code: e.CodeVerifyFailed},
		{name: "substituted disk producer", code: e.CodeVerifyFailed},
		{name: "zero matching members", code: e.CodeReleaseIncomplete},
		{name: "duplicate members", code: e.CodeVerifyFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			plan, slot, artifacts, observed := hostedDiskObservationFixture(t)
			wrapper := artifacts["package/"+slot.Name]
			arch := "amd64"
			envelopeOutput := observed
			disk := *observed.Disk
			envelopeOutput.Disk = &disk
			if test.name == "substituted descriptor" {
				arch = "arm64"
				// A valid alternate wrapper with the same raw bytes but a different
				// guest declaration must not replace the exact observed manifest.
				wrapper.Path = filepath.Join(t.TempDir(), "alternate-wrapper")
				_, err := delivery.BuildDiskArtifact(t.Context(), wrapper.Path, delivery.DiskArtifactRequest{
					Path: artifacts[slot.MemberArtifactID].Path, Digest: disk.PayloadDigest, Size: disk.Size,
					Platform: "linux/arm64", Format: "qcow2", Encryption: "none", ArtifactType: wrapper.MediaType,
				})
				if err != nil {
					t.Fatal(err)
				}
				descriptor, err := delivery.PayloadDescriptor(t.Context(), wrapper.Path)
				if err != nil {
					t.Fatal(err)
				}
				envelopeOutput.Digest = descriptor.Digest
			}
			if test.name == "substituted disk producer" {
				envelopeOutput.Disk.Producer = "sibling-producer"
			}
			if test.name == "zero matching members" {
				envelopeOutput.Name = "sibling-output"
			}
			request := hostedDiskPublicationRequest(t, wrapper.Path, arch, envelopeOutput)
			prepared := filepath.Join(t.TempDir(), "prepared")
			if _, err := delivery.BuildPublicationRoot(t.Context(), prepared, request); err != nil {
				t.Fatal(err)
			}
			attested := artifacts[slot.ArtifactID].Path
			if _, err := delivery.AttestEvidence(t.Context(), prepared, attested, fixtureSBOMAttestation(t)); err != nil {
				t.Fatal(err)
			}
			if test.name == "duplicate members" {
				duplicateHostedDiskMember(t, attested)
			}
			outputs, err := observeHostedOutputs(t.Context(), ".nvkr/factory", []requiredOutput{slot}, artifacts, plan)
			if test.code != "" {
				assertCode(t, err, test.code)
				if outputs != nil {
					t.Fatalf("rejected envelope exposed partial outputs: %+v", outputs)
				}
				guard := "publication substitutes observed output"
				if test.name == "zero matching members" {
					guard = "unique exact disk member"
				}
				if test.name == "duplicate members" {
					// The delivery owner rejects the hostile metadata before the
					// lifecycle's matches>1 defense can ever see a valid duplicate.
					guard = "duplicate release object name"
				}
				if !strings.Contains(err.Error(), guard) {
					t.Fatalf("wrong guard reached: %v, want %q", err, guard)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(outputs, []Artifact{observed}) {
				t.Fatalf("exact hosted disk observation = %+v, %v; want %+v", outputs, err, observed)
			}
			if observed.Digest == observed.Disk.PayloadDigest {
				t.Fatal("wrapper identity collapsed into raw QCOW2 identity")
			}
		})
	}
}

func hostedDiskObservationFixture(t *testing.T) (*factory.Plan, requiredOutput, map[string]factory.Artifact, Artifact) {
	t.Helper()
	prepared, _ := diskLifecycleFixtureWithProducer(t, "hosted-disk-membership", true)
	inputs, environment := prepared.plan.Definition.Clone(), prepared.plan.Environment.Clone()
	environment.Publication = testEnvironment().Publication
	environment.Signing = &Signing{Mode: factory.SigningModeCosign, Identity: "^builder$", Issuer: "issuer"}
	inputs.CosignTool = "cosign"
	pin := "sha256:" + repeat("1", 64)
	environment.DiskInspectionProfiles = map[string]definition.DiskInspectionProfile{"linux/amd64": {
		Image: "example.test/inspection@" + pin, InspectorDigest: pin, CodecDigest: pin, GuestfishDigest: pin,
		ScannerDigest: pin, PackageReaderDigest: pin, Resources: execution.ResourceRequest{
			CPUMillis: 1000, MemoryMiB: 2048, EphemeralStorageMiB: 2048},
		TimeoutSeconds: 60, MaxFilesystemBytes: 1 << 20, ScratchStorage: "private-volume",
	}}
	snapshot := prepared.plan.CatalogSnapshot
	root := snapshot.Components["nvke-node-non-fde"]
	packer := *root.Packer
	packer.AcquisitionArchive = "acquisitions.tar"
	root.Packer = &packer
	snapshot.Components["nvke-node-non-fde"] = root
	snapshot.Tools = map[string]catalog.ToolPin{"cosign": {Format: "raw", URL: "https://example.test/cosign",
		Platform: environment.UtilityPlatform, ExecutablePath: "cosign", ArchiveDigest: pin, ExecutableDigest: pin}}
	stack, err := catalog.ApplyStack(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := factory.Compile(t.Context(), inputs, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	run, err := PrepareRun(inputs, environment, plan, "hosted-disk-membership")
	if err != nil {
		t.Fatal(err)
	}
	slot := run.Expected[0]
	artifacts := make(map[string]factory.Artifact, len(plan.Graph.Artifacts))
	for _, artifact := range plan.Graph.Artifacts {
		artifacts[artifact.ID] = artifact
	}
	payload := make([]byte, 512)
	copy(payload, []byte{'Q', 'F', 'I', 0xfb})
	binary.BigEndian.PutUint32(payload[4:8], 3)
	binary.BigEndian.PutUint32(payload[20:24], 9)
	binary.BigEndian.PutUint64(payload[24:32], 1<<20)
	binary.BigEndian.PutUint32(payload[100:104], 104)
	raw := artifacts[slot.MemberArtifactID]
	if err := os.MkdirAll(filepath.Dir(raw.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(raw.Path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	record := artifacts[slot.ComponentResults[0]]
	writeCanonicalTestFile(t, record.Path, fixtureProducerResult(t, plan, raw, raw.Path))
	wrapper := artifacts["package/"+slot.Name]
	if _, err := delivery.BuildDiskArtifact(t.Context(), wrapper.Path, delivery.DiskArtifactRequest{
		Path: raw.Path, Digest: pipeline.FileDigest(payload), Size: int64(len(payload)), Platform: "linux/amd64",
		Format: "qcow2", Encryption: "none", ArtifactType: wrapper.MediaType,
	}); err != nil {
		t.Fatal(err)
	}
	expected := plan.LifecycleOutputs[0]
	observed, err := factory.ObserveDiskPublicationPayload(t.Context(), plan, expected, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	return plan, slot, artifacts, observed
}

func hostedDiskPublicationRequest(t *testing.T, wrapper, arch string, output Artifact) delivery.PublicationRequest {
	t.Helper()
	index, err := os.ReadFile(filepath.Join(wrapper, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	content, err := delivery.ContentDigest([]delivery.Output{output})
	if err != nil {
		t.Fatal(err)
	}
	request := delivery.PublicationRequest{Release: &delivery.ReleaseMetadata{
		DefinitionDigest: output.BuildDigest, EnvironmentDigest: output.BuildDigest,
		PlanDigest: output.Disk.PlanDigest, ContentSubject: content},
		Objects: []delivery.ObjectInput{{Name: output.Name, Kind: factory.PackageDiskImage, Path: wrapper, Layout: true,
			OS: "linux", Arch: arch,
			ArtifactType: delivery.MediaDiskImage, SemanticDigest: output.Disk.PayloadDigest, BlobDigest: pipeline.FileDigest(index),
			Output: &delivery.OutputBinding{Repository: output.Name, Format: factory.PackageDiskImage, BuildDigest: output.BuildDigest,
				Components: output.Components, Disk: output.Disk, BuildSubjects: []delivery.BuildSubject{{
					Component: output.Disk.Component, Digest: output.Disk.PayloadDigest}}}}}}
	bom, err := sbom.NewSubject(output.BuildDigest, output.Disk.Component, output.Disk.PayloadDigest)
	if err != nil {
		t.Fatal(err)
	}
	bom.Metadata.Component.Type = "operating-system"
	bom, err = sbom.ForSubject(bom, output.Disk.PayloadDigest, "build-output", output.Disk.PayloadDigest)
	if err != nil {
		t.Fatal(err)
	}
	bom, err = sbom.WithDependencies(bom, []sbom.Component{{Type: "library", Name: "fixture-installed-package", Version: "1"}},
		"disk-reconciled/v1", output.BuildDigest)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"nvkr:diskInspectionRuntimeDigest", "nvkr:diskInspectorDigest", "nvkr:diskCodecDigest"} {
		bom.Metadata.Props = append(bom.Metadata.Props, sbom.Property{Name: key, Value: output.BuildDigest})
	}
	sort.Slice(bom.Metadata.Props, func(i, j int) bool { return bom.Metadata.Props[i].Name < bom.Metadata.Props[j].Name })
	semantic, err := bom.Digest()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := bom.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	request.Objects = append(request.Objects, delivery.ObjectInput{Name: "sbom-" + strings.TrimPrefix(semantic, "sha256:"),
		Kind: "sbom", ArtifactType: "application/vnd.cyclonedx+json", LayerMediaType: "application/vnd.cyclonedx+json",
		SemanticDigest: semantic, Bytes: encoded, Evidence: &delivery.EvidenceBinding{Outputs: []string{output.Name},
			Component: output.Disk.Component, Stage: "build-output", SubjectKind: "file", SubjectDigest: output.Disk.PayloadDigest}})
	return request
}

// Rehash every edited OCI descriptor so the duplicate test reaches metadata
// validation, rather than failing an unrelated stale blob-integrity check.
func duplicateHostedDiskMember(t *testing.T, root string) {
	t.Helper()
	read := func(path string, into any) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, into); err != nil {
			t.Fatal(err)
		}
	}
	blob := func(pin string) string {
		return filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(pin, "sha256:"))
	}
	writeBlob := func(value any, descriptor *ocispec.Descriptor) {
		raw, err := canonical.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		descriptor.Digest, descriptor.Size = digest.Digest(pipeline.FileDigest(raw)), int64(len(raw))
		if err := os.WriteFile(blob(descriptor.Digest.String()), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var index ocispec.Index
	read(filepath.Join(root, "index.json"), &index)
	var manifest ocispec.Manifest
	read(blob(index.Manifests[0].Digest.String()), &manifest)
	var metadata delivery.PublicationRoot
	read(blob(manifest.Config.Digest.String()), &metadata)
	for _, object := range metadata.Objects {
		if object.Output != nil {
			metadata.Objects = append(metadata.Objects, object)
			break
		}
	}
	writeBlob(metadata, &manifest.Config)
	writeBlob(manifest, &index.Manifests[0])
	writeCanonicalTestFile(t, filepath.Join(root, "index.json"), index)
}
