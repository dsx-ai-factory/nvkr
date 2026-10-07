// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package pipeline

import (
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func TestAdmitVMImagePackerRequiresExactEnvironmentNetworkAdmission(t *testing.T) {
	node := testPackerContainerNode()
	node.Network = ir.NetworkDefault
	selection := VMImagePackerSelection{Subject: "plan/node", Node: node}
	session, err := NewNodeSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	if _, err := session.AdmitVMImagePacker(t.Context(), []VMImagePackerSelection{selection}, nil,
		"definitely-missing-runtime"); code(err) != e.CodeEngineCapability ||
		!strings.Contains(err.Error(), "no Environment network admission") {
		t.Fatalf("unadmitted network selection error = %v, want network admission failure", err)
	}

	environment := definition.Environment{
		APIVersion:       definition.EnvironmentAPIVersion,
		Kind:             definition.EnvironmentKind,
		Metadata:         definition.Metadata{Name: "fixture"},
		UtilityContainer: "example.test/utility@sha256:" + repeatHex("a"),
		UtilityPlatform:  "linux/amd64",
		Runtime:          definition.Runtime{BuildNetworkPolicy: definition.BuildNetworkProducerDeclared},
	}
	environmentDigest, err := environment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := AuthorizeVMImagePackerNetwork(
		environment, environmentDigest, []VMImagePackerSelection{selection},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.AdmitVMImagePacker(t.Context(), []VMImagePackerSelection{selection}, receipt,
		"definitely-missing-runtime"); code(err) != e.CodeEngineCapability ||
		!strings.Contains(err.Error(), "resolving container runtime") {
		t.Fatalf("admitted network selection error = %v, want runtime-resolution failure", err)
	}
	changed := selection
	changed.Node.Version = "1.0.1"
	if _, err := session.AdmitVMImagePacker(t.Context(), []VMImagePackerSelection{changed}, receipt,
		"definitely-missing-runtime"); code(err) != e.CodePipelineDrift {
		t.Fatalf("changed network selection error = %v, want %s", err, e.CodePipelineDrift)
	}
}

func TestAuthorizeVMImagePackerNetworkRejectsOfflineEnvironment(t *testing.T) {
	node := testPackerContainerNode()
	node.Network = ir.NetworkDefault
	environment := definition.Environment{
		APIVersion:       definition.EnvironmentAPIVersion,
		Kind:             definition.EnvironmentKind,
		Metadata:         definition.Metadata{Name: "fixture"},
		UtilityContainer: "example.test/utility@sha256:" + repeatHex("a"),
		UtilityPlatform:  "linux/amd64",
	}
	environmentDigest, err := environment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if receipt, err := AuthorizeVMImagePackerNetwork(environment, environmentDigest, []VMImagePackerSelection{{
		Subject: "plan/node", Node: node,
	}}); receipt != nil || code(err) != e.CodeEngineCapability {
		t.Fatalf("offline Environment network receipt/error = %#v/%v, want nil/%s", receipt, err, e.CodeEngineCapability)
	}
}

func TestAuthorizeVMImagePackerNetworkRejectsPlanEnvironmentMismatch(t *testing.T) {
	node := testPackerContainerNode()
	node.Network = ir.NetworkDefault
	environment := definition.Environment{
		APIVersion:       definition.EnvironmentAPIVersion,
		Kind:             definition.EnvironmentKind,
		Metadata:         definition.Metadata{Name: "fixture"},
		UtilityContainer: "example.test/utility@sha256:" + repeatHex("a"),
		UtilityPlatform:  "linux/amd64",
		Runtime:          definition.Runtime{BuildNetworkPolicy: definition.BuildNetworkProducerDeclared},
	}
	differentEnvironment := environment
	differentEnvironment.Metadata.Name = "different"
	differentDigest, err := differentEnvironment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := AuthorizeVMImagePackerNetwork(environment, differentDigest, []VMImagePackerSelection{{
		Subject: "plan/node", Node: node,
	}})
	if receipt != nil || code(err) != e.CodeUnverifiedRef || !strings.Contains(err.Error(), "differs") {
		t.Fatalf("mismatched Plan Environment receipt/error = %#v/%v, want nil/%s mismatch", receipt, err, e.CodeUnverifiedRef)
	}
}
