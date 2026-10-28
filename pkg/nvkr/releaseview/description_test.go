// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package releaseview

import (
	"bytes"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestDescriptionVersionedContract(t *testing.T) {
	publication := descriptionFixture()
	raw, err := Render(publication, Options{Format: "json"})
	if err != nil {
		t.Fatal(err)
	}
	const path = "testdata/release-description-v1.json"
	if os.Getenv("NVKR_UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, want) {
		t.Fatalf("versioned release description changed; review the contract before regenerating with NVKR_UPDATE_GOLDEN=1\n%s", raw)
	}
	got := decodeDescription(t, raw)
	if got.SchemaVersion != "nvkr.dev/release-description/v1" || got.TotalOutputs != 6 || len(got.Outputs) != 6 || len(got.Evidence) != 4 {
		t.Fatalf("lost version or complete membership: %+v", got)
	}
	if got.InspectionDetails.OutputSelection != "all" || got.SelectedOutput != "" {
		t.Fatalf("complete release was filtered: %+v", got)
	}
	// Every known publication kind is retained regardless of product/component
	// vocabulary. An APT repository must not disappear from an installer's view.
	kinds := make([]string, 0, len(got.Outputs))
	for _, output := range got.Outputs {
		kinds = append(kinds, output.Kind)
		if output.Name == "multi" {
			if output.OS != "" || output.Arch != "" || output.Digest != publication.Outputs[4].Digest ||
				len(output.ImageIndex.Children) != 2 || len(output.ImageIndex.RequiredPlatforms) != 2 {
				t.Fatalf("aggregate identity or complete child membership changed: %+v", output)
			}
		}
		if output.Name == "chart" && (output.ChartVersion != "8.2.1" || output.OS != "" || output.Arch != "") {
			t.Fatalf("chart version/platform description changed: %+v", output)
		}
	}
	if !reflect.DeepEqual(kinds, []string{"apt-repository", "helm-oci", "disk-image", "oci", "oci", "executable"}) {
		t.Fatalf("artifact inventory was narrowed: %v", kinds)
	}
	// Separate revisions from the same repository are legitimate descriptions,
	// including across native children of one aggregate. Keep every exact binding.
	for i, evidence := range got.Evidence {
		original := publication.Evidence[i]
		if evidence.Source.Repo != original.Source.Repo || evidence.Source.Commit != original.Source.Commit ||
			evidence.Binding.Component != original.Binding.Component || evidence.Binding.Stage != original.Binding.Stage ||
			evidence.Binding.SubjectDigest != original.Binding.SubjectDigest || evidence.Binding.Platform != original.Binding.Platform ||
			!reflect.DeepEqual(evidence.Binding.Outputs, original.Binding.Outputs) {
			t.Fatalf("source or subject binding changed: %+v", evidence)
		}
	}
	var topLevel map[string]json.RawMessage
	if err := json.Unmarshal(raw, &topLevel); err != nil {
		t.Fatal(err)
	}
	for _, unsupported := range []string{"source", "platform", "roles", "lane", "verified"} {
		if _, exists := topLevel[unsupported]; exists {
			t.Fatalf("description invented a product assumption: %s", unsupported)
		}
	}
}

func TestDescriptionInspectionClaims(t *testing.T) {
	for _, qualification := range []string{"qualified", "pipeline-attested", "static-unqualified", ""} {
		t.Run(qualification, func(t *testing.T) {
			publication := descriptionFixture()
			publication.DeclaredQualification = qualification
			raw, err := Render(publication, Options{Format: "json"})
			if err != nil {
				t.Fatal(err)
			}
			got := decodeDescription(t, raw)
			want := InspectionDetails{
				Scope: "metadata-only", MetadataClosure: "complete", OutputSelection: "all",
				Checks: InspectionChecks{
					MetadataIntegrity: "checked", PayloadIntegrity: "not-checked", Signatures: "not-checked",
					ConsumerPolicy: "not-checked", RegistryAvailability: "not-checked", InstallationCompatibility: "not-checked",
				},
			}
			if got.InspectionDetails != want || got.DeclaredQualification != qualification ||
				got.Inspection != "metadata inspection; payloads, signatures and policy not verified" {
				t.Fatalf("publisher claim changed inspection scope: %+v", got)
			}
		})
	}
}

func TestDescriptionFilteredInventoryKeepsOriginalEvidenceMembership(t *testing.T) {
	raw, err := Render(descriptionFixture(), Options{Format: "json", Output: "tools"})
	if err != nil {
		t.Fatal(err)
	}
	got := decodeDescription(t, raw)
	if got.SelectedOutput != "tools" || got.TotalOutputs != 6 || len(got.Outputs) != 1 || len(got.Evidence) != 1 ||
		got.InspectionDetails.MetadataClosure != "complete" || got.InspectionDetails.OutputSelection != "filtered" ||
		!reflect.DeepEqual(got.Evidence[0].Binding.Outputs, []string{"apt", "tools"}) {
		t.Fatalf("filter hid its scope or rewrote evidence membership: %+v", got)
	}
}

func TestDescriptionDeterministicAndIndependent(t *testing.T) {
	want, err := Render(descriptionFixture(), Options{Format: "json"})
	if err != nil {
		t.Fatal(err)
	}
	for seed := range uint64(16) {
		publication := descriptionFixture()
		random := rand.New(rand.NewPCG(seed, 42))
		random.Shuffle(len(publication.Outputs), func(i, j int) {
			publication.Outputs[i], publication.Outputs[j] = publication.Outputs[j], publication.Outputs[i]
		})
		random.Shuffle(len(publication.Evidence), func(i, j int) {
			publication.Evidence[i], publication.Evidence[j] = publication.Evidence[j], publication.Evidence[i]
		})
		for i := range publication.Outputs {
			output := &publication.Outputs[i]
			random.Shuffle(len(output.Components), func(i, j int) {
				output.Components[i], output.Components[j] = output.Components[j], output.Components[i]
			})
			random.Shuffle(len(output.BuildSubjects), func(i, j int) {
				output.BuildSubjects[i], output.BuildSubjects[j] = output.BuildSubjects[j], output.BuildSubjects[i]
			})
			if output.ImageIndex != nil {
				index := output.ImageIndex
				random.Shuffle(len(index.RequiredPlatforms), func(i, j int) {
					index.RequiredPlatforms[i], index.RequiredPlatforms[j] = index.RequiredPlatforms[j], index.RequiredPlatforms[i]
				})
				random.Shuffle(len(index.Children), func(i, j int) { index.Children[i], index.Children[j] = index.Children[j], index.Children[i] })
			}
		}
		for i := range publication.Evidence {
			outputs := publication.Evidence[i].Binding.Outputs
			random.Shuffle(len(outputs), func(i, j int) { outputs[i], outputs[j] = outputs[j], outputs[i] })
		}
		before, err := json.Marshal(publication)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Render(publication, Options{Format: "json"})
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("input ordering changed JSON for seed %d: %v\n%s", seed, err, got)
		}
		after, err := json.Marshal(publication)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("sorting mutated input for seed %d: %v", seed, err)
		}
	}
}

func TestDescriptionDetachedNestedValues(t *testing.T) {
	publication := descriptionFixture()
	before, err := json.Marshal(publication)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Describe(publication, "")
	if err != nil {
		t.Fatal(err)
	}
	got.Outputs[0].Components[0] = "replacement"
	got.Outputs[0].BuildSubjects[0].Component = "replacement"
	got.Outputs[2].Disk.Producer = "replacement"
	got.Outputs[4].ImageIndex.RequiredPlatforms[0] = "replacement"
	got.Outputs[4].ImageIndex.Children[0].Component = "replacement"
	got.Evidence[0].Source.Commit = "replacement"
	got.Evidence[0].Binding.Outputs[0] = "replacement"
	after, err := json.Marshal(publication)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("projected values alias the source description: %v", err)
	}
}

func TestDescribeMatchesJSONAndRejectsAmbiguousSelection(t *testing.T) {
	for _, selection := range []string{"", "tools", "multi", "missing", "tools\n"} {
		t.Run(selection, func(t *testing.T) {
			got, err := Describe(descriptionFixture(), selection)
			if selection == "missing" || selection == "tools\n" {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != e.CodeUsage || !reflect.DeepEqual(got, Description{}) {
					t.Fatalf("invalid selection returned a partial view or wrong error: %+v, %v", got, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			raw, err := Render(descriptionFixture(), Options{Format: "json", Output: selection})
			if err != nil || !reflect.DeepEqual(got, decodeDescription(t, raw)) {
				t.Fatalf("typed and JSON views differ: %v", err)
			}
		})
	}
	publication := descriptionFixture()
	publication.Outputs = append(publication.Outputs, publication.Outputs[len(publication.Outputs)-1])
	got, err := Describe(publication, "tools")
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != e.CodeUsage || !reflect.DeepEqual(got, Description{}) {
		t.Fatalf("ambiguous selection returned a partial view or wrong error: %+v, %v", got, err)
	}
}

func TestDescriptionEmptyCollectionsAndUnavailableSources(t *testing.T) {
	for _, test := range []struct {
		name        string
		publication delivery.PublicationDescription
		fragments   []string
	}{
		{name: "empty inventory", fragments: []string{`"outputs": []`, `"evidence": []`}},
		{
			name: "missing source and build subjects",
			publication: delivery.PublicationDescription{
				Outputs: []delivery.PublicationOutput{{Output: delivery.Output{Name: "acquired"}}},
				Evidence: []delivery.PublicationEvidence{
					{Name: "missing", Binding: delivery.EvidenceBinding{Outputs: []string{"acquired"}}},
					{Name: "redacted", Source: &delivery.PublicationSource{}},
				},
			},
			fragments: []string{`"buildSubjects": []`, `"outputs": []`, `"source": {}`},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := Render(test.publication, Options{Format: "json"})
			if err != nil {
				t.Fatal(err)
			}
			for _, fragment := range test.fragments {
				if !bytes.Contains(raw, []byte(fragment)) {
					t.Fatalf("missing explicit empty collection or redacted source %s: %s", fragment, raw)
				}
			}
			if strings.Contains(string(raw), ": null") {
				t.Fatalf("empty presentation collections must be arrays: %s", raw)
			}
			got := decodeDescription(t, raw)
			if len(got.Evidence) > 0 && (got.Evidence[0].Source != nil || got.Evidence[1].Source == nil) {
				t.Fatalf("absent source and redacted source were conflated: %+v", got.Evidence)
			}
		})
	}
}

func decodeDescription(t *testing.T, raw []byte) Description {
	t.Helper()
	var result Description
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("invalid external release description: %v", err)
	}
	return result
}

func descriptionFixture() delivery.PublicationDescription {
	digest := func(value string) string { return "sha256:" + strings.Repeat(value, 64) }
	ref := func(name, value string) string { return "registry.example/product/" + name + "@" + digest(value) }
	subject := func(component, value string) delivery.BuildSubject {
		return delivery.BuildSubject{Component: component, Digest: digest(value)}
	}
	output := func(name, kind, value string, components ...string) delivery.Output {
		return delivery.Output{Name: name, Kind: kind, Digest: digest(value), Reference: ref(name, value), BuildDigest: digest("b"), Components: components}
	}
	apt := output("apt", "apt-repository", "1", "package")
	chart := output("chart", "helm-oci", "2", "application-chart")
	chart.ChartVersion = "8.2.1"
	disk := output("disk", "disk-image", "3", "base-system")
	disk.OS, disk.Arch = "linux", "arm64"
	disk.Disk = &delivery.DiskOutput{
		EvidenceMode: "build-provenance-v1", PayloadDigest: digest("4"), Producer: "disk-builder",
		Component: "base-system", Format: "qcow2", Encryption: "none", Size: 4096,
		PlanDigest: digest("c"), Outcome: "succeeded",
	}
	native := output("image", "oci", "5", "unfamiliar-service")
	native.OS, native.Arch = "linux", "arm64"
	aggregate := output("multi", "oci", "6", "multi-service")
	aggregate.ImageIndex = &delivery.ImageIndexBinding{
		RequiredPlatforms: []string{"linux/amd64", "linux/arm64"},
		Children: []delivery.ImageIndexChildBinding{
			{Platform: "linux/amd64", ManifestDigest: digest("7"), Size: 1024, BuildDigest: digest("8"), Component: "multi-service", BuildSubject: digest("9")},
			{Platform: "linux/arm64", ManifestDigest: digest("a"), Size: 1025, BuildDigest: digest("b"), Component: "multi-service", BuildSubject: digest("c")},
		},
	}
	tools := output("tools", "executable", "d", "cli", "package")
	tools.OS, tools.Arch = "linux", "amd64"
	result := delivery.PublicationDescription{
		Reference: ref("releases", "e"), RootDigest: digest("e"), DeclaredQualification: "pipeline-attested",
		ReferenceScope: "derived from supplied root reference; no registry availability claim",
		Metadata: delivery.ReleaseMetadata{
			InvocationID: "workflow-run-42-attempt-1", DefinitionDigest: digest("a"), EnvironmentDigest: digest("b"),
			PlanDigest: digest("c"), ContentSubject: digest("f"),
		},
		Outputs: []delivery.PublicationOutput{
			{Output: apt, ArtifactType: "application/vnd.nvkr.apt-repository.v1", BuildSubjects: []delivery.BuildSubject{subject("package", "1")}},
			{Output: chart, ArtifactType: "application/vnd.nvkr.helm-bundle.v1"},
			{Output: disk, ArtifactType: delivery.MediaDiskImage, BuildSubjects: []delivery.BuildSubject{subject("base-system", "2")}},
			{Output: native, ArtifactType: "application/vnd.oci.image.manifest.v1+json", BuildSubjects: []delivery.BuildSubject{subject("unfamiliar-service", "3")}},
			{Output: aggregate, ArtifactType: delivery.MediaOCIIndex},
			{Output: tools, ArtifactType: delivery.MediaBinaryBundle, BuildSubjects: []delivery.BuildSubject{subject("cli", "4"), subject("package", "1")}},
		},
	}
	for _, item := range []struct {
		name, component, platform, repo, commit, subject string
		outputs                                          []string
	}{
		{"sbom-image", "unfamiliar-service", "linux/arm64", "https://example.org/product.git", "1", "5", []string{"image"}},
		{"sbom-multi-amd64", "multi-service", "linux/amd64", "https://example.org/product.git", "2", "7", []string{"multi"}},
		{"sbom-multi-arm64", "multi-service", "linux/arm64", "https://example.org/product.git", "3", "a", []string{"multi"}},
		{"sbom-package", "package", "linux/amd64", "https://example.org/dependency.git", "4", "1", []string{"apt", "tools"}},
	} {
		kind := "oci-manifest"
		if item.component == "package" {
			kind = "file"
		}
		result.Evidence = append(result.Evidence, delivery.PublicationEvidence{
			Name: item.name, Kind: "sbom", Reference: ref(item.name, "f"), ArtifactType: "application/vnd.cyclonedx+json",
			ManifestDigest: digest("f"), BlobDigest: digest("e"), SemanticDigest: digest("d"),
			Binding: delivery.EvidenceBinding{
				Platform: item.platform, DocumentDigest: digest("d"), Outputs: slices.Clone(item.outputs),
				Component: item.component, Stage: "build-output", SubjectKind: kind, SubjectDigest: digest(item.subject),
				InspectionInputDigest: digest("c"),
			},
			Source: &delivery.PublicationSource{Repo: item.repo, Commit: strings.Repeat(item.commit, 40)},
		})
	}
	return result
}
