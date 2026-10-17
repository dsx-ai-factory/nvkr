// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package conformance

import (
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

func localDescriptionFixture() lifecycle.LocalDiagnosticDescription {
	d := artifactDescriptionFixture().Outputs[0]
	return lifecycle.LocalDiagnosticDescription{Lane: "unqualified-local-execution", Attempt: "local",
		Outputs: []delivery.PublicationOutput{{Output: delivery.Output{Name: d.Name, Kind: d.Kind,
			Digest: d.Subject, BuildDigest: d.BuildDigest, OS: "linux", Arch: "amd64"}}}}
}

func TestLocalConformanceImageBindings(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name  string
		refs  []LocalImageBinding
		alias bool
		code  e.Code
	}{
		{"local import alias", []LocalImageBinding{{"image", "registry.example.test/nvke:local"}}, true, ""},
		{"tag and held digest", []LocalImageBinding{{"image", "registry.example.test/nvke:local@" + digest}}, true, ""},
		{"implicit tagged digest", []LocalImageBinding{{"image", "nvke:local@" + digest}}, true, e.CodeUsage},
		{"exact tool image", []LocalImageBinding{{"image", "registry.example.test/nvke@" + digest}}, false, ""},
		{"unpinned tool", []LocalImageBinding{{"image", "registry.example.test/nvke:local"}}, false, e.CodeUnpinnedInput},
		{"wrong bytes", []LocalImageBinding{{"image", "registry.example.test/nvke@sha256:" + strings.Repeat("b", 64)}}, true, e.CodeVerifyFailed},
		{"unknown output", []LocalImageBinding{{"sibling", "registry.example.test/nvke:local"}}, true, e.CodeVerifyFailed},
		{"implicit registry", []LocalImageBinding{{"image", "nvke:local"}}, true, e.CodeUsage},
		{"duplicate", []LocalImageBinding{{"image", "registry.example.test/nvke:local"}, {"image", "registry.example.test/nvke:other"}}, true, e.CodeUsage},
		{"empty", nil, true, e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view, aliases, err := localView(localDescriptionFixture(), tc.refs, tc.alias)
			if tc.code != "" {
				requireCode(t, err, tc.code)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if view.Origin.InvocationID != "" || view.Inputs.ReleaseSubject != "" || aliases["image"] != tc.refs[0].Reference || view.Outputs[0].Reference != "registry.example.test/nvke@"+digest {
				t.Fatal("local alias acquired published authority or lost its held subject")
			}
		})
	}
}

func TestLocalConformanceRefusesCallerOwners(t *testing.T) {
	for _, owner := range []*lifecycle.LocalDiagnosticArtifacts{nil, {}} {
		producer, err := NewLocal(owner, owner, optionsFixture(), LocalBindings{})
		requireCode(t, err, e.CodeGateUnmet)
		if producer != nil {
			t.Fatal("caller-created local owner admitted")
		}
	}
	for _, product := range []bool{false, true} {
		options := optionsFixture()
		if product {
			options.Artifacts = &lifecycle.ArtifactCheckReceipt{}
		} else {
			options.ToolArtifacts = &lifecycle.ArtifactCheckReceipt{}
		}
		_, err := NewLocal(nil, nil, options, LocalBindings{})
		requireCode(t, err, e.CodeUsage)
	}
}

func TestLocalConformanceAliasStillChecksRuntimeBytes(t *testing.T) {
	options := optionsFixture()
	view, aliases, err := localView(localDescriptionFixture(), []LocalImageBinding{{"image", "registry.example.test/nvke:local"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	outputs, err := selectOutputs(view, options.Targets)
	if err != nil {
		t.Fatal(err)
	}
	for _, wrong := range []bool{false, true} {
		nodes, pods := deploymentFixture(t)
		pods[0][0].Spec.Containers[0].Image = aliases["image"]
		if wrong {
			pods[0][0].Status.ContainerStatuses[0].ImageID = "registry.example.test/nvke@sha256:" + strings.Repeat("b", 64)
		}
		_, _, err := evaluateDeploymentsWithAliases(options.Targets, outputs, nodes, pods, aliases)
		if wrong {
			requireCode(t, err, e.CodeVerifyFailed)
		} else if err != nil {
			t.Fatal(err)
		}
		_, _, publishedErr := evaluateDeployments(options.Targets, outputs, nodes, pods)
		requireCode(t, publishedErr, e.CodeVerifyFailed)
	}
}
