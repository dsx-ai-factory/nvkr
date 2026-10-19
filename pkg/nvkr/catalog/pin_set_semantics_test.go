// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestResolvedPinSetDeclarationAdmissionMatchesRegistry(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Source)
		valid  bool
	}{
		{"default values", func(*Source) {}, true},
		{"explicit values", func(s *Source) { s.PinSet.Format = "values" }, true},
		{"NVKE deploy", func(s *Source) {
			s.PinSet.Format = "nvke-deploy-v1"
			s.PinSet.Members["x"] = PinSetMember{Artifact: "x"}
		}, true},
		{"path escape", func(s *Source) { s.PinSet.File = "../versions.yaml" }, false},
		{"missing path", func(s *Source) { s.PinSet.File = "" }, false},
		{"unknown format", func(s *Source) { s.PinSet.Format = "future" }, false},
		{"action source", func(s *Source) { s.Kind = "github-action" }, false},
		{"empty members", func(s *Source) { s.PinSet.Members = nil }, false},
		{"ambiguous member", func(s *Source) { s.PinSet.Members["x"] = PinSetMember{Artifact: "x", Source: "x", Path: "x.version"} }, false},
		{"invalid value path", func(s *Source) { s.PinSet.Members["x"] = PinSetMember{Artifact: "x", Path: "x["} }, false},
		{"values chart identity", func(s *Source) {
			s.PinSet.Members["x"] = PinSetMember{Artifact: "x", Path: "x.version", Chart: &HelmChart{}}
		}, false},
		{"values accounted item", func(s *Source) { s.PinSet.Accounted = map[string]PinSetItem{"local": {Type: "manifest"}} }, false},
		{"NVKE with value path", func(s *Source) { s.PinSet.Format = "nvke-deploy-v1" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := Source{Repo: "https://example.invalid/pins", PinSet: &PinSet{
				File: "versions.yaml", Members: map[string]PinSetMember{"x": {Artifact: "x", Path: "x.version"}},
			}}
			tc.mutate(&source)
			component := goodPrebuilt()
			resolved := base(component)
			resolved.Sources = map[string]Source{"pins": source}
			registry := &registryDoc{Sources: resolved.Sources, Components: map[string]rawComponent{"x": {
				Platforms: component.Platforms, OutputType: component.OutputType, Prebuilt: component.Prebuilt, PinnedBy: "pins",
			}}}
			for _, check := range []struct {
				name string
				run  func() error
			}{
				{"registry", func() error { return validateRegistryDeclarations(registry) }},
				{"resolved", resolved.Validate},
				{"snapshot", func() error { _, err := resolved.SnapshotDigest(); return err }},
			} {
				t.Run(check.name, func(t *testing.T) {
					err := check.run()
					if tc.valid {
						if err != nil {
							t.Fatal(err)
						}
					} else {
						assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
					}
				})
			}
		})
	}
}
