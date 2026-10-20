// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"fmt"
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
		{"tool source", func(s *Source) {
			s.Tool = &ToolSource{Format: "raw", URL: "https://example.invalid/tool", Platform: "linux/amd64", ExecutablePath: "tool"}
		}, false},
		{"empty members", func(s *Source) { s.PinSet.Members = nil }, false},
		{"empty member name", func(s *Source) {
			s.PinSet.Members = map[string]PinSetMember{"": {Artifact: "x", Path: "x.version"}}
		}, false},
		{"blank member name", func(s *Source) {
			s.PinSet.Members = map[string]PinSetMember{" \t": {Artifact: "x", Path: "x.version"}}
		}, false},
		{"leading member whitespace", func(s *Source) {
			s.PinSet.Members = map[string]PinSetMember{" x": {Artifact: "x", Path: "x.version"}}
		}, false},
		{"trailing member whitespace", func(s *Source) {
			s.PinSet.Members = map[string]PinSetMember{"x ": {Artifact: "x", Path: "x.version"}}
		}, false},
		{"ambiguous member", func(s *Source) { s.PinSet.Members["x"] = PinSetMember{Artifact: "x", Source: "x", Path: "x.version"} }, false},
		{"invalid value path", func(s *Source) { s.PinSet.Members["x"] = PinSetMember{Artifact: "x", Path: "x["} }, false},
		{"values chart identity", func(s *Source) {
			s.PinSet.Members["x"] = PinSetMember{Artifact: "x", Path: "x.version", Chart: &HelmChart{}}
		}, false},
		{"values accounted item", func(s *Source) { s.PinSet.Accounted = map[string]PinSetItem{"local": {Type: "manifest"}} }, false},
		{"NVKE with value path", func(s *Source) { s.PinSet.Format = "nvke-deploy-v1" }, false},
		{"NVKE unknown accounted type", func(s *Source) {
			s.PinSet = &PinSet{File: "deploy.yaml", Format: "nvke-deploy-v1",
				Members: map[string]PinSetMember{"x": {Artifact: "x"}}, Accounted: map[string]PinSetItem{"local": {Type: "future"}}}
		}, false},
		{"NVKE overlapping declarations", func(s *Source) {
			s.PinSet = &PinSet{File: "deploy.yaml", Format: "nvke-deploy-v1",
				Members: map[string]PinSetMember{"x": {Artifact: "x"}}, Accounted: map[string]PinSetItem{"x": {Type: "manifest"}}}
		}, false},
		{"NVKE blank accounted name", func(s *Source) {
			s.PinSet = &PinSet{File: "deploy.yaml", Format: "nvke-deploy-v1",
				Members: map[string]PinSetMember{"x": {Artifact: "x"}}, Accounted: map[string]PinSetItem{" ": {Type: "manifest"}}}
		}, false},
		{"NVKE padded accounted name", func(s *Source) {
			s.PinSet = &PinSet{File: "deploy.yaml", Format: "nvke-deploy-v1",
				Members: map[string]PinSetMember{"x": {Artifact: "x"}}, Accounted: map[string]PinSetItem{" local ": {Type: "manifest"}}}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := Source{Repo: "https://example.invalid/pins", PinSet: &PinSet{
				File: "versions.yaml", Members: map[string]PinSetMember{"x": {Artifact: "x", Path: "x.version"}},
			}}
			tc.mutate(&source)
			if source.Tool != nil {
				if err := validateToolSource(*source.Tool); err != nil {
					t.Fatalf("tool fixture must be valid without its pin set: %v", err)
				}
			}
			assertPinSetAdmissionMatchesRegistry(t, source, tc.valid)
		})
	}
}

func TestResolvedPinSetDeclarationMemberBounds(t *testing.T) {
	for _, tc := range []struct {
		name               string
		format             string
		members, accounted int
		valid              bool
	}{
		{"values at member limit", "values", maxPinSetMembers, 0, true},
		{"values over member limit", "values", maxPinSetMembers + 1, 0, false},
		{"NVKE at combined limit", "nvke-deploy-v1", 1, maxPinSetMembers - 1, true},
		{"NVKE over combined limit", "nvke-deploy-v1", 1, maxPinSetMembers, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set := &PinSet{File: "versions.yaml", Format: tc.format,
				Members: make(map[string]PinSetMember, tc.members), Accounted: make(map[string]PinSetItem, tc.accounted)}
			for index := range tc.members {
				name := fmt.Sprintf("member%d", index)
				member := PinSetMember{Artifact: name}
				if tc.format == "values" {
					member.Path = name + ".version"
				}
				set.Members[name] = member
			}
			for index := range tc.accounted {
				set.Accounted[fmt.Sprintf("local%d", index)] = PinSetItem{Type: "manifest"}
			}
			assertPinSetAdmissionMatchesRegistry(t, Source{Repo: "https://example.invalid/pins", PinSet: set}, tc.valid)
		})
	}
}

func assertPinSetAdmissionMatchesRegistry(t *testing.T, source Source, valid bool) {
	t.Helper()
	component := goodPrebuilt()
	// Keep zero-member fixtures otherwise valid, and give every member its own
	// reciprocal producer so ownership failures cannot mask a declaration limit.
	resolved := &Catalog{Sources: map[string]Source{"pins": source}, Components: map[string]Component{"independent": component}}
	registry := &registryDoc{Sources: resolved.Sources, Components: map[string]rawComponent{"independent": {
		Platforms: component.Platforms, OutputType: component.OutputType, Prebuilt: component.Prebuilt,
	}}}
	for _, member := range source.PinSet.Members {
		if member.Artifact != "" {
			resolved.Components[member.Artifact] = component
			registry.Components[member.Artifact] = rawComponent{
				Platforms: component.Platforms, OutputType: component.OutputType, Prebuilt: component.Prebuilt, PinnedBy: "pins",
			}
		}
	}
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
			if valid {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
			}
		})
	}
}
