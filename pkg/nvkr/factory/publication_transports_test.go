// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestUnavailablePublicationTransportBeforeCatalogIO(t *testing.T) {
	for _, tc := range []struct {
		name       string
		transports []string
	}{
		{"APT only", []string{authored.PublicationTransportAPT}},
		{"both", []string{authored.PublicationTransportAPT, authored.PublicationTransportOCI}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			definition, environment := fixtureInputs("fixture", "cli", "")
			definition.Catalog.Base = "absent-catalog"
			environment.Publication = &authored.Publication{Transports: tc.transports}
			if len(tc.transports) > 1 {
				environment.Publication.RepositoryPrefix = "registry.example/team"
				environment.Publication.Anonymous = true
			}
			plan, err := CompileDocuments(t.Context(), filepath.Join(t.TempDir(), "definition.yaml"), definition, "environment.yaml", environment, "test")
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			if plan != nil || !strings.Contains(err.Error(), "publication transport apt") {
				t.Fatalf("unavailable transport reached catalog I/O or returned a Plan: %v", err)
			}
		})
	}
}

func TestPublicationTransportsBindEnvironmentWithoutChangingBuilds(t *testing.T) {
	original := imageIndexTestPlan(t)
	environment := original.Environment.Clone()
	environment.Publication.Transports = []string{authored.PublicationTransportOCI}
	changed := compileImageIndexFixture(t, original, environment)
	if changed.EnvironmentDigest == original.EnvironmentDigest || changed.DefinitionDigest != original.DefinitionDigest {
		t.Fatal("explicit transport selection was not independently bound to Environment identity")
	}
	if !reflect.DeepEqual(changed.Targets, original.Targets) || !reflect.DeepEqual(changed.Routes, original.Routes) {
		t.Fatal("explicit OCI selection changed legacy routes")
	}
	for i, build := range original.Builds {
		if build.BuildDigest != changed.Builds[i].BuildDigest {
			t.Fatal("transport selection changed native Build identity")
		}
	}
	for i, output := range original.LifecycleOutputs {
		if output.BuildDigest != changed.LifecycleOutputs[i].BuildDigest {
			t.Fatal("transport selection changed output Build identity")
		}
	}
	environment.Publication.Transports[0] = authored.PublicationTransportAPT
	if changed.Environment.Publication.Transports[0] != authored.PublicationTransportOCI {
		t.Fatal("compiler retained the caller's transport selection")
	}

	// A decoded or mutated Plan cannot evade admission at a later boundary.
	for _, tc := range []struct {
		name       string
		transports []string
	}{
		{"APT only", []string{authored.PublicationTransportAPT}},
		{"both", []string{authored.PublicationTransportAPT, authored.PublicationTransportOCI}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed.Environment.Publication.Transports = tc.transports
			assertFactoryErrorCode(t, changed.Validate(), e.CodeEngineCapability)
			bundle, err := RenderGitHub(t.Context(), changed)
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			if bundle != nil {
				t.Fatal("unavailable selected transport produced a generated bundle")
			}
			assertFactoryErrorCode(t, ExecuteNode(t.Context(), changed, "absent-node", io.Discard, io.Discard), e.CodeEngineCapability)
		})
	}
}
