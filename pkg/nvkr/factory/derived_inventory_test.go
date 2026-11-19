// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/derivedoutput"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/provenance"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
	"github.com/google/go-containerregistry/pkg/registry"
)

func derivedInventoryWorkspace(t *testing.T, configure ...func(*authored.Environment)) (*Plan, map[string]Artifact) {
	t.Helper()
	definition, environment, stack := derivedTestInputs(t)
	for _, change := range configure {
		change(&environment)
	}
	plan, err := Compile(t.Context(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	_, artifacts := planIndex(plan)
	workspace := t.TempDir()
	for id, artifact := range artifacts {
		artifact.Path = filepath.Join(workspace, artifact.Path)
		artifacts[id] = artifact
		if err := os.MkdirAll(filepath.Dir(artifact.Path), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	input := artifacts["artifact/chart/containerd"]
	chart := filepath.Join(filepath.Dir(input.Path), "fixture-chart")
	if err := os.MkdirAll(filepath.Join(chart, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{"Chart.yaml": "apiVersion: v2\nname: fixture-chart\nversion: 1.2.3\n", "values.yaml": "image: {}\n", "templates/config.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: fixture\n"} {
		if err := os.WriteFile(filepath.Join(chart, name), []byte(raw), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := transportarchive.Pack(t.Context(), input.Path, filepath.Dir(chart), []string{"fixture-chart"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(input.Path)
	if err != nil {
		t.Fatal(err)
	}
	var instruction ir.Node
	for _, node := range plan.Builds[0].Graph.Nodes {
		if node.Kind == ir.KindBuild {
			instruction = node
		}
	}
	result := pipeline.ArtifactResult{Component: input.Component, OutputType: input.Kind, SHA256: pipeline.FileDigest(raw), Size: int64(len(raw)), Toolchain: instruction.Toolchain, BuilderType: instruction.BuilderType, Path: instruction.ArtifactPath}
	record, err := canonical.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifacts["result/chart/containerd"].Path, record, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := executePackage(t.Context(), plan, planNode(plan, "package/chart"), artifacts, io.Discard); err != nil {
		t.Fatal(err)
	}
	return plan, artifacts
}

func TestDerivedInventoryObservationAcceptsEquivalentCompression(t *testing.T) {
	plan, artifacts := derivedInventoryWorkspace(t)
	required := plan.LifecycleOutputs[1]
	if err := executePackage(t.Context(), plan, planNode(plan, "package/inventory"), artifacts, io.Discard); err != nil {
		t.Fatal(err)
	}
	original, raw, err := delivery.ReadArtifact(t.Context(), artifacts[required.ArtifactID].Path)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	tarBytes, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	writer, err := gzip.NewWriterLevel(&buffer, gzip.NoCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(tarBytes); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "inventory.tar.gz")
	if err := os.WriteFile(archive, buffer.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	artifact := artifacts[required.ArtifactID]
	artifact.Path = filepath.Join(t.TempDir(), "alternative")
	if _, err := delivery.BuildBundleArtifactWithRepresentation(t.Context(), artifact.Path, artifact.Name, archive, pipeline.FileDigest(buffer.Bytes()), publicationOCIRepresentation(plan)); err != nil {
		t.Fatal(err)
	}
	artifacts[required.ArtifactID] = artifact
	observed, err := ObserveLifecyclePayload(t.Context(), plan, required, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	if observed.Digest == original.Digest || observed.SemanticDigest != pipeline.FileDigest(buffer.Bytes()) {
		t.Fatal("observation did not preserve actual compressed transport identity")
	}
}

func TestDerivedInventoryLegacyDryRunSignThenVerify(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "")
	server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	defer server.Close()
	plan, artifacts := derivedInventoryWorkspace(t, func(environment *authored.Environment) {
		environment.Signing = dryRunSigningProfile()
		environment.Publication.RepositoryPrefix = strings.TrimPrefix(server.URL, "http://") + "/nvkr"
	})
	if PerOutputPublication(plan) {
		t.Fatal("fixture must exercise legacy publication")
	}
	verifyNode := planNode(plan, "verify-publication/release")
	if !containsString(verifyNode.Inputs, "predicate/release") {
		t.Fatal("verification predicate is not a declared carrier")
	}
	if err := executeSBOM(t.Context(), plan, planNode(plan, "chart/sbom"), artifacts, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"package/inventory", "package/release"} {
		if err := executePackage(t.Context(), plan, planNode(plan, id), artifacts, io.Discard); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
	reference, err := delivery.Push(t.Context(), artifacts["package/release"].Path, plan.Targets["release"].Ref,
		delivery.RegistryOptions{PlainHTTP: true, Anonymous: true})
	if err != nil {
		t.Fatal(err)
	}
	writeReferenceFile(t, artifacts["publication/release"].Path, reference)
	if err := executeSign(t.Context(), plan, planNode(plan, "sign/release"), artifacts, productionOCISigner, io.Discard); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(artifacts["predicate/release"].Path)
	if err != nil {
		t.Fatal(err)
	}
	var predicate provenance.Predicate
	if err := canonical.UnmarshalExact(bytes.TrimSpace(raw), &predicate); err != nil {
		t.Fatal(err)
	}
	if predicate.RunDetails.Builder.ID != provenance.LocalBuilderID || predicate.BuildDefinition.BuildType != provenance.BuildTypeWrappedBuild {
		t.Fatal("legacy predicate lost local builder or aggregate derived classification")
	}
	for _, tc := range []struct {
		name string
		omit bool
	}{
		{"exact local predicate", false},
		{"missing derived material", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := predicate
			if tc.omit {
				candidate.BuildDefinition.ResolvedDependencies = nil
			}
			encoded, err := canonical.Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if err := writeFactoryFile(t.Context(), artifacts["predicate/release"].Path, encoded, 0o600); err != nil {
				t.Fatal(err)
			}
			output := artifacts["verification/release"]
			output.Path = filepath.Join(t.TempDir(), "verification.json")
			artifacts[output.ID] = output
			err = executeVerifyPublication(t.Context(), plan, verifyNode, artifacts, productionOCISigner, io.Discard)
			if tc.omit {
				assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
				assertNotWritten(t, output.Path)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			verified, err := os.ReadFile(output.Path)
			if err != nil {
				t.Fatal(err)
			}
			var record sign.VerificationRecord
			if err := canonical.UnmarshalExact(verified, &record); err != nil {
				t.Fatal(err)
			}
			if !record.DryRun || record.SignatureVerified || record.AttestationVerified {
				t.Fatal("local diagnostic overclaimed signature verification")
			}
		})
	}
}

func TestDerivedInventoryObservationRejectsArtifactSubstitution(t *testing.T) {
	plan, artifacts := derivedInventoryWorkspace(t)
	required := plan.LifecycleOutputs[1]
	if err := executePackage(t.Context(), plan, planNode(plan, "package/inventory"), artifacts, io.Discard); err != nil {
		t.Fatal(err)
	}
	original := artifacts[required.ArtifactID]
	for _, tc := range []struct {
		name string
		edit func(*Artifact)
	}{
		{"name", func(a *Artifact) { a.Name = "foreign" }},
		{"id", func(a *Artifact) { a.ID = "package/chart" }},
		{"producer", func(a *Artifact) { a.Producer = "package/chart" }},
		{"media", func(a *Artifact) { a.MediaType = "application/foreign" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := original
			tc.edit(&candidate)
			artifacts[required.ArtifactID] = candidate
			_, err := ObserveLifecyclePayload(t.Context(), plan, required, artifacts)
			assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
		})
	}
}

func TestDerivedInventoryPublicationPreservesMembershipAndEvidence(t *testing.T) {
	plan, artifacts := derivedInventoryWorkspace(t)
	if err := executePackage(t.Context(), plan, planNode(plan, "package/inventory"), artifacts, io.Discard); err != nil {
		t.Fatal(err)
	}
	required := plan.LifecycleOutputs[1]
	node := Node{Inputs: []string{required.ArtifactID}}
	evidence, err := releaseEvidenceObjectsForOutputs(t.Context(), plan, node, artifacts, []LifecycleOutputPlan{required})
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 1 || evidence[0].Evidence.Stage != "packaging" || evidence[0].Evidence.Outputs[0] != required.Name {
		t.Fatal("derived output lacks its own packaging inventory")
	}
	observed, err := ObserveLifecyclePayload(t.Context(), plan, required, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	outputs := []delivery.Output{{Name: required.Name, Kind: required.Kind, BuildDigest: required.BuildDigest, Digest: observed.Digest, Derived: required.Derived.Clone()}}
	objects, cleanup, err := collectPublicationObjects(t.Context(), plan, artifacts[required.ArtifactID], []Artifact{artifacts[required.ArtifactID]}, evidence, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := bindPublicationOutputObjects(t.Context(), plan, node, artifacts, objects, outputs); err != nil {
		t.Fatal(err)
	}
	content, err := delivery.ContentDigest(outputs)
	if err != nil {
		t.Fatal(err)
	}
	planDigest, err := plan.IdentityDigest()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "publication")
	result, err := delivery.BuildPublicationRoot(t.Context(), root, delivery.PublicationRequest{Objects: objects, Release: &delivery.ReleaseMetadata{DefinitionDigest: plan.DefinitionDigest, EnvironmentDigest: plan.EnvironmentDigest, PlanDigest: planDigest, ContentSubject: content}})
	if err != nil {
		t.Fatal(err)
	}
	publication, err := delivery.OpenPublication(t.Context(), root, result.RootDigest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = publication.Close() }()
	description, err := publication.Description("localhost:5000/nvkr/releases@" + result.RootDigest)
	if err != nil {
		t.Fatal(err)
	}
	if len(description.Outputs) != 1 || len(description.Evidence) != 1 || !reflect.DeepEqual(description.Outputs[0].Derived, required.Derived) {
		t.Fatal("publication lost derived membership, materials or exact archive subject")
	}
	inventory, err := projectPackagedInventory(t.Context(), plan, required.Derived, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	defer server.Close()
	options := delivery.RegistryOptions{PlainHTTP: true, Anonymous: true}
	reference, err := delivery.Push(t.Context(), root, strings.TrimPrefix(server.URL, "http://")+"/nvkr/releases", options)
	if err != nil {
		t.Fatal(err)
	}
	checked, err := delivery.PullCheckedPublication(t.Context(), reference, t.TempDir(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer checked.Close()
	server.Close()
	if err := os.Rename(root, root+"-replaced"); err != nil {
		t.Fatal(err)
	}
	for _, owner := range []interface {
		CompareDerivedInventory(context.Context, string, []byte) error
	}{publication, checked} {
		for _, tc := range []struct {
			name     string
			output   string
			expected []byte
			code     e.Code
		}{
			{"exact", required.Name, inventory, ""},
			{"different document", required.Name, []byte("substituted"), e.CodeVerifyFailed},
			{"foreign output", "chart", inventory, e.CodeVerifyFailed},
		} {
			t.Run(tc.name, func(t *testing.T) {
				err := owner.CompareDerivedInventory(t.Context(), tc.output, tc.expected)
				if tc.code != "" {
					assertFactoryErrorCode(t, err, tc.code)
				} else if err != nil {
					t.Fatal(err)
				}
			})
		}
	}
	copyOf := *checked
	if err := copyOf.Close(); err != nil {
		t.Fatal(err)
	}
	assertFactoryErrorCode(t, checked.CompareDerivedInventory(t.Context(), required.Name, inventory), e.CodeGateUnmet)
}

func TestDerivedInventoryImageIndexCarriesNativeEvidence(t *testing.T) {
	plan, imageNode, artifacts := indexObservationFixture(t)
	if err := executePackage(t.Context(), plan, imageNode, artifacts, io.Discard); err != nil {
		t.Fatal(err)
	}
	plan.Definition.Outputs = append(plan.Definition.Outputs, authored.DefinitionOutput{Name: "inventory", Platform: "linux/amd64", Format: authored.OutputBundle, Derived: &derivedoutput.Selection{Encoding: derivedoutput.InventoryV1, Outputs: []string{"nvke-controller"}}})
	slices.SortFunc(plan.Definition.Outputs, func(a, b authored.DefinitionOutput) int { return strings.Compare(a.Name, b.Name) })
	plan = compileImageIndexFixture(t, plan, plan.Environment)
	_, compiled := planIndex(plan)
	node := planNode(plan, "package/inventory")
	transported := map[string]Artifact{}
	for _, id := range node.Inputs {
		artifact, ok := artifacts[id]
		if !ok {
			t.Fatalf("missing carrier %s", id)
		}
		transported[id] = artifact
	}
	output := compiled["package/inventory"]
	output.Path = filepath.Join(filepath.Dir(artifacts["package/nvke-controller"].Path), "inventory")
	transported[output.ID] = output
	if err := executePackage(t.Context(), plan, node, transported, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, required := range plan.LifecycleOutputs {
		if required.Name == "inventory" {
			if _, err := ObserveLifecyclePayload(t.Context(), plan, required, transported); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestDerivedInventoryAuthenticatesSiblingPackages(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, *Plan, map[string]Artifact)
		code   e.Code
	}{
		{"exact", nil, ""},
		{"missing result", func(_ *testing.T, _ *Plan, a map[string]Artifact) { delete(a, "result/chart/containerd") }, e.CodeReleaseIncomplete},
		{"foreign result", func(_ *testing.T, _ *Plan, a map[string]Artifact) {
			v := a["result/chart/containerd"]
			v.BuildTarget = "foreign"
			a[v.ID] = v
		}, e.CodeReleaseIncomplete},
		{"changed producer bytes", func(t *testing.T, _ *Plan, a map[string]Artifact) {
			if err := os.WriteFile(a["artifact/chart/containerd"].Path, []byte("changed"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, artifacts := derivedInventoryWorkspace(t)
			// Hosted derive work can only see the compiler-declared carrier set.
			transported := map[string]Artifact{}
			node := planNode(plan, "package/inventory")
			for _, id := range append(append([]string(nil), node.Inputs...), node.Outputs...) {
				transported[id] = artifacts[id]
			}
			artifacts = transported
			if tc.mutate != nil {
				tc.mutate(t, plan, artifacts)
			}
			err := executePackage(t.Context(), plan, planNode(plan, "package/inventory"), artifacts, io.Discard)
			if tc.code != "" {
				assertFactoryErrorCode(t, err, tc.code)
				if _, statErr := os.Lstat(artifacts["package/inventory"].Path); !os.IsNotExist(statErr) {
					t.Fatal("failed derive wrote output")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			projected, err := projectPackagedInventory(t.Context(), plan, plan.LifecycleOutputs[1].Derived, artifacts)
			if err != nil {
				t.Fatal(err)
			}
			want, err := derivedoutput.InventoryArchive(t.Context(), projected)
			if err != nil {
				t.Fatal(err)
			}
			_, got, err := delivery.ReadArtifact(t.Context(), artifacts["package/inventory"].Path)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("inventory differs from shared projection: %v", err)
			}
			if _, err := ObserveLifecyclePayload(t.Context(), plan, plan.LifecycleOutputs[1], artifacts); err != nil {
				t.Fatal(err)
			}
		})
	}
}
